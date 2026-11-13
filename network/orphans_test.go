package network

import (
	"fmt"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

func senderTx(sender string, nonce uint64) core.Transaction {
	tx := fakeTx(nonce)
	tx.Sender = sender
	return tx
}

// Tampon toplamda ve gönderen başına sınırlıdır, dolunca en eski düşer, kimlik yinelenmez,
// süresi dolan düşer; take eşit nonce'u döndürür, küçükleri atar.
func TestOrphanPoolBounds(t *testing.T) {
	o := newOrphanPool()
	t0 := time.Unix(1_900_000_000, 0)
	for i := 0; i < orphanPerSender; i++ {
		if !o.add(senderTx("a", uint64(i+2)), t0) {
			t.Fatalf("%d. yetim alınmadı", i)
		}
	}
	if o.add(senderTx("a", 100), t0) || !o.senderFull("a") {
		t.Fatal("gönderen sınırı uygulanmadı")
	}
	if o.add(senderTx("a", 2), t0) {
		t.Fatal("aynı kimlik iki kez alındı")
	}
	first := senderTx("b0", 5)
	if !o.add(first, t0) || o.add(first, t0) {
		t.Fatal("yinelenen kimlik denetimi")
	}
	// Çıkarılıp yeniden eklenen kimliğin eski sıra kaydı canlı sayılmaz.
	if got := o.take("b0", 5); len(got) != 1 || !o.add(first, t0) || o.liveLocked(o.order[orphanPerSender]) {
		t.Fatalf("yeniden ekleme: sıra %d", len(o.order))
	}
	for i := 1; o.len() < orphanMax; i++ {
		o.add(senderTx(fmt.Sprint("b", i), 5), t0.Add(time.Second))
	}
	o.add(senderTx("c", 5), t0.Add(time.Second)) // en eski ("a"nın ilki) düşer
	if o.len() != orphanMax || !o.has(first.IDBytes()) {
		t.Fatalf("taşmada düşen yanlış: %d", o.len())
	}
	if got := o.take("a", 2); len(got) != 0 {
		t.Fatalf("düşen yetim döndü: %d", len(got))
	}
	if got := o.take("a", 4); len(got) != 1 || got[0].Nonce != 4 {
		t.Fatalf("take: %v", got)
	}
	if o.len() != orphanMax-2 { // 3 ve 4 çıkarıldı (2 taşmada düşmüştü)
		t.Fatalf("take sonrası %d", o.len())
	}

	left := o.expire(t0.Add(orphanTTL))
	if o.has(first.IDBytes()) || o.len() == 0 {
		t.Fatalf("süre: ilk kayıt düşmedi ya da hepsi düştü (%d)", o.len())
	}
	for _, s := range left {
		if s == "a" || s == "b0" {
			t.Fatalf("süresi dolan gönderen kaldı: %s", s)
		}
	}
	if o.expire(t0.Add(orphanTTL + time.Second)); o.len() != 0 || len(o.order) != 0 {
		t.Fatalf("süre sonunda tampon boş değil: %d / %d", o.len(), len(o.order))
	}
}

// Yer değiştiren gruplar: n+1 önce gelir ve tampona girer; n gelince ikisi de havuza girer ve
// sırayla (n, n+1) yayın kuyruğuna konur.
func TestOrphanReorderedTxs(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	tx1 := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce, p.DefaultExpiryHeight)
	tx2 := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+1, p.DefaultExpiryHeight)

	ps.handleTxs("eş", "g1", []core.Transaction{tx2})
	if ps.orphans.len() != 1 || mempoolLen(t, ps.Mempool) != 0 || !ps.txGate.allow("g1", 1) {
		t.Fatalf("yetim tamponlanmadı: tampon %d, havuz %d", ps.orphans.len(), mempoolLen(t, ps.Mempool))
	}
	ps.handleTxs("eş", "g2", []core.Transaction{tx1})
	if ps.orphans.len() != 0 || mempoolLen(t, ps.Mempool) != 2 {
		t.Fatalf("yetim kabul edilmedi: tampon %d, havuz %d", ps.orphans.len(), mempoolLen(t, ps.Mempool))
	}
	if len(ps.txRelayCh) != 2 {
		t.Fatalf("yayın kuyruğu %d", len(ps.txRelayCh))
	}
	if a, b := <-ps.txRelayCh, <-ps.txRelayCh; a.Nonce != tx1.Nonce || b.Nonce != tx2.Nonce {
		t.Fatalf("yayın sırası %d, %d", a.Nonce, b.Nonce)
	}
}

// Sahte imzalı yetim tampona girmez ve grubu cezalandırır; fonsuz gönderenin ya da sınırın
// ötesindeki yetim tampona girmez, cezalanmaz.
func TestOrphanRejects(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	forged := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+1, p.DefaultExpiryHeight)
	forged.Amount = 2 * core.Coin
	ps.handleTxs("eş", "g1", []core.Transaction{forged})
	if ps.orphans.len() != 0 || ps.txGate.allow("g1", 1) {
		t.Fatal("sahte imzalı yetim tamponlandı ya da grup cezalanmadı")
	}
	if e, ok := ps.rejects.get(forged.IDBytes()); !ok || e.class != rejectInvalid {
		t.Fatal("sahte imzalı yetim ret önbelleğinde değil")
	}

	carol, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	unfunded := signedPeerTx(t, carol, to, core.Coin, p.MinFee, 3, p.DefaultExpiryHeight)
	far := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+core.MempoolMaxPerSender, p.DefaultExpiryHeight)
	ps.handleTxs("eş", "g2", []core.Transaction{unfunded, far})
	if ps.orphans.len() != 0 || !ps.txGate.allow("g2", 1) {
		t.Fatalf("fonsuz ya da çok ileri yetim: tampon %d", ps.orphans.len())
	}
}

// Öncül bloktan gelirse süpürme yetimi havuza alır ve yayar; süresi dolan yetim düşer.
func TestOrphanSweep(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	tx1 := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce, p.DefaultExpiryHeight)
	tx2 := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+1, p.DefaultExpiryHeight)
	tx3 := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+3, p.DefaultExpiryHeight)
	now := time.Now()
	if !ps.bufferOrphan(tx2, now) || !ps.bufferOrphan(tx3, now) || ps.orphans.len() != 2 {
		t.Fatalf("yetimler tamponlanmadı: %d", ps.orphans.len())
	}
	if _, err := bc.AddBlock([]core.Transaction{tx1}, minerAddr(t)); err != nil {
		t.Fatal(err)
	}
	ps.sweepOrphans(now)
	if mempoolLen(t, ps.Mempool) != 1 || len(ps.txRelayCh) != 1 || ps.orphans.len() != 1 {
		t.Fatalf("süpürme: havuz %d, kuyruk %d, tampon %d", mempoolLen(t, ps.Mempool), len(ps.txRelayCh), ps.orphans.len())
	}
	ps.sweepOrphans(now.Add(orphanTTL))
	if ps.orphans.len() != 0 {
		t.Fatal("süresi dolan yetim düşmedi")
	}
}
