package network

import (
	"fmt"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// fundKey bc üzerinde yeni bir anahtara olgunlaşmış bir blok ödülü verir.
func fundKey(t *testing.T, bc *core.Blockchain) *crypto.KeyPair {
	t.Helper()
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, kp.Address); err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, int(core.RegtestParams.CoinbaseMaturity))
	return kp
}

// policyTx havuz politikasına uygun, sıradaki nonce'lu imzalı bir işlemdir.
func policyTx(t *testing.T, m *core.Mempool, from *crypto.KeyPair, amount int64) core.Transaction {
	t.Helper()
	p, err := m.Policy(from.Address)
	if err != nil {
		t.Fatal(err)
	}
	tx := core.Transaction{Receiver: minerAddr(t), Amount: amount, Fee: p.MinFee,
		ExpiryHeight: p.DefaultExpiryHeight, Nonce: p.NextNonce}
	if err := tx.Sign(from, core.RegtestParams); err != nil {
		t.Fatal(err)
	}
	return tx
}

func mempoolLen(t *testing.T, m *core.Mempool) int {
	t.Helper()
	n, err := m.Len()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Geçersiz işlem içeren grubun kalanı işlenmez ve gönderen adres grubu cezalanır; sınırı aşan
// grup bütünüyle bırakılır; yalnızca yeni kabul edilen işlem yeniden yayılır; hız sınırı uygulanır.
func TestHandleTxs(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	good := policyTx(t, ps.Mempool, alice, core.Coin)
	bad := good
	bad.Amount = 2 * core.Coin // imza artık tutmaz

	ps.handleTxs("eş", "kötü", []core.Transaction{bad, good})
	if n := mempoolLen(t, ps.Mempool); n != 0 || len(ps.txRelayCh) != 0 || ps.txOverflow.len() != 0 {
		t.Fatalf("geçersiz işlemden sonra grup işlendi: havuz %d, kuyruk %d", n, len(ps.txRelayCh))
	}
	ps.handleTxs("eş", "kötü", []core.Transaction{good}) // ceza süresinde
	if n := mempoolLen(t, ps.Mempool); n != 0 {
		t.Fatalf("cezalı grubun işlemi kabul edildi: havuz %d", n)
	}
	big := make([]core.Transaction, maxTxsPerMsg+1)
	for i := range big {
		big[i] = good
	}
	ps.handleTxs("eş", "büyük", big)
	if n := mempoolLen(t, ps.Mempool); n != 0 {
		t.Fatalf("sınırı aşan grup işlendi: havuz %d", n)
	}
	ps.handleTxs("eş", "iyi", []core.Transaction{good})
	ps.handleTxs("eş", "iyi", []core.Transaction{good}) // yineleme: yeniden yayılmaz
	if n := mempoolLen(t, ps.Mempool); n != 1 || len(ps.txRelayCh) != 1 || ps.txOverflow.len() != 0 {
		t.Fatalf("havuz %d, kuyruk %d; beklenen 1, 1", n, len(ps.txRelayCh))
	}
}

// Adres grubu başına işlem kovası tükenince reddeder, zamanla dolar; ceza süresi biter.
func TestTxGate(t *testing.T) {
	g := newTxGate()
	now := time.Unix(1_900_000_000, 0)
	g.now = func() time.Time { return now }
	if !g.allow("a", txRateBurst) || g.allow("a", 1) {
		t.Fatal("kova sınırı uygulanmadı")
	}
	if !g.allow("b", 1) {
		t.Fatal("başka grup etkilendi")
	}
	now = now.Add(time.Second)
	if !g.allow("a", txRatePerSec) || g.allow("a", 1) {
		t.Fatal("kova saniyede txRatePerSec dolmadı")
	}
	g.penalize("b")
	now = now.Add(badTxPenalty - time.Second)
	if g.allow("b", 1) {
		t.Fatal("ceza süresinde kabul edildi")
	}
	now = now.Add(time.Second)
	if !g.allow("b", 1) {
		t.Fatal("ceza süresi bitmedi")
	}
}

// Bir düğümün havuzuna giren işlem eşinin havuzuna yayılır.
func TestTxRelayReachesPeer(t *testing.T) {
	src := startNode(t, time.Hour)
	alice := fundKey(t, src.bc)
	dst := startNode(t, time.Hour, src.addr)
	dst.ps.TriggerSync()
	waitTip(t, dst.bc, src.bc.TipHash())
	src.ps.AddPeer(dst.addr)

	tx := policyTx(t, src.ps.Mempool, alice, core.Coin)
	if _, err := src.ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	src.ps.RelayTx(tx)

	deadline := time.Now().Add(10 * time.Second)
	for mempoolLen(t, dst.ps.Mempool) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("işlem eşin havuzuna ulaşmadı")
		}
		time.Sleep(20 * time.Millisecond)
	}
	txs, err := dst.ps.Mempool.Select(core.MaxTxPerBlock)
	if err != nil || len(txs) != 1 || txs[0].ID() != tx.ID() {
		t.Fatalf("eşin havuzu: %d işlem, %v", len(txs), err)
	}
}

// Tablo dolu ve kayıtların hepsi cezalıyken yeni grup yine de kabul edilir; tablo sınırı aşılmaz.
func TestTxGateFullTable(t *testing.T) {
	g := newTxGate()
	for i := 0; i < maxTxGateKeys+10; i++ {
		g.penalize(fmt.Sprint("kötü", i))
	}
	if len(g.buckets) > maxTxGateKeys {
		t.Fatalf("tablo %d kayda büyüdü", len(g.buckets))
	}
	if !g.allow("dürüst", 1) {
		t.Fatal("dolu tabloda yeni grup reddedildi")
	}
	if len(g.buckets) > maxTxGateKeys {
		t.Fatalf("tablo %d kayda büyüdü", len(g.buckets))
	}
}
