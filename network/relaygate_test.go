package network

import (
	"fmt"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

func amountTx(sender string, nonce uint64, amount int64) core.Transaction {
	tx := senderTx(sender, nonce)
	tx.Amount = amount
	return tx
}

// Yuvanın ilk yayını hemen gider; süre içinde gelen yer değiştirme bekletilir (son gelen kazanır)
// ve arkasındaki daha yüksek nonce'lar tutulur; süre dolunca nonce sırasıyla çıkar.
func TestRelayGateCooldown(t *testing.T) {
	g := newRelayGate()
	t0 := time.Unix(1_900_000_000, 0)
	a := amountTx("s", 5, 1)
	if !g.admit(&a, t0) {
		t.Fatal("yuvanın ilk yayını bekletildi")
	}
	a2, a3 := amountTx("s", 5, 2), amountTx("s", 5, 3)
	if g.admit(&a2, t0.Add(time.Second)) || g.admit(&a3, t0.Add(2*time.Second)) {
		t.Fatal("süre içindeki yer değiştirme hemen yayıldı")
	}
	b := amountTx("s", 6, 1)
	if g.admit(&b, t0.Add(3*time.Second)) {
		t.Fatal("bekleyen yuvanın arkasındaki nonce hemen yayıldı")
	}
	low, other := amountTx("s", 4, 1), amountTx("u", 5, 1)
	if !g.admit(&low, t0.Add(3*time.Second)) || !g.admit(&other, t0.Add(3*time.Second)) {
		t.Fatal("ilgisiz yuva bekletildi")
	}
	if got := g.due(t0.Add(10 * time.Second)); len(got) != 0 {
		t.Fatalf("süre dolmadan çıktı: %v", got)
	}
	t1 := t0.Add(relayReplaceCooldown)
	got := g.due(t1)
	if len(got) != 2 || got[0] != a3.ID() || got[1] != b.ID() {
		t.Fatalf("çıkan %v, beklenen [a3 b]", got)
	}
	if len(g.holds) != 0 {
		t.Fatal("boşalan gönderici kaydı kaldı")
	}
	a4 := amountTx("s", 5, 4)
	if g.admit(&a4, t1.Add(time.Second)) {
		t.Fatal("bekletilmiş yayından hemen sonraki yer değiştirme yayıldı")
	}
	g.due(t1.Add(relayReplaceCooldown)) // a4 çıkar
	a5 := amountTx("s", 5, 5)
	if !g.admit(&a5, t1.Add(2*relayReplaceCooldown)) {
		t.Fatal("süre dolduktan sonraki yer değiştirme bekletildi")
	}
}

// Kapı sınırlıdır: sınır aşılırsa bekletilmeyen kayıtlar unutulur (bekleyen yuvanınki süresi
// dolmadıkça korunur); süresi dolanlar silinir ve kaydı silinen bekleyen yuva çıkar.
func TestRelayGateBounds(t *testing.T) {
	g := newRelayGate()
	t0 := time.Unix(1_900_000_000, 0)
	p1, p2 := amountTx("x", 0, 1), amountTx("x", 0, 2)
	g.admit(&p1, t0)
	if g.admit(&p2, t0.Add(time.Millisecond)) {
		t.Fatal("yer değiştirme bekletilmedi")
	}
	for i := 1; len(g.last) < relayGateMax; i++ {
		tx := amountTx(fmt.Sprint("y", i), 0, 1)
		g.admit(&tx, t0)
	}
	extra := amountTx("z", 0, 1)
	if !g.admit(&extra, t0.Add(time.Second)) || len(g.last) > relayGateMax {
		t.Fatalf("sınır aşıldı: %d", len(g.last))
	}
	if _, ok := g.last[relaySlotKey{"x", 0}]; !ok {
		t.Fatal("bekleyen yuvanın kaydı unutuldu")
	}

	late := amountTx("w", 0, 1) // tavan doluyken: süresi dolanlar silinir
	g.admit(&late, t0.Add(relayReplaceCooldown+time.Second))
	if len(g.last) != 1 {
		t.Fatalf("süresi dolan kayıtlar silinmedi: %d", len(g.last))
	}
	if got := g.due(t0.Add(relayReplaceCooldown + time.Second)); len(got) != 1 || got[0] != p2.ID() {
		t.Fatalf("kaydı silinen bekleyen yuva çıkmadı: %v", got)
	}
}

// Havuzla: yer değiştirme süre içinde kanala gitmez; süre dolunca yalnızca son sürüm gider;
// bekletilirken kazılan sürüm yayılmaz.
func TestRelayGateFlush(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	t0 := time.Unix(1_900_000_000, 0)
	add := func(fee int64, at time.Time) core.Transaction {
		t.Helper()
		tx := signedPeerTx(t, alice, to, core.Coin, fee, p.NextNonce, p.DefaultExpiryHeight)
		if _, err := ps.Mempool.Add(tx); err != nil {
			t.Fatalf("ücret %d: %v", fee, err)
		}
		ps.relayTxAt(tx, at)
		return tx
	}
	add(p.MinFee, t0)
	add(2*p.MinFee, t0.Add(time.Second))
	rep2 := add(3*p.MinFee, t0.Add(2*time.Second))
	if len(ps.txRelayCh) != 1 {
		t.Fatalf("süre içinde yer değiştirme kanala gitti: %d", len(ps.txRelayCh))
	}
	t1 := t0.Add(relayReplaceCooldown)
	ps.flushRelayGate(t1)
	<-ps.txRelayCh
	if len(ps.txRelayCh) != 1 {
		t.Fatalf("süre dolunca %d yayın, beklenen 1", len(ps.txRelayCh))
	}
	if got := <-ps.txRelayCh; got.ID() != rep2.ID() {
		t.Fatal("son sürüm yayılmadı")
	}

	rep3 := add(4*p.MinFee, t1.Add(time.Second))
	if _, err := bc.AddBlock([]core.Transaction{rep3}, minerAddr(t)); err != nil {
		t.Fatal(err)
	}
	if n := mempoolLen(t, ps.Mempool); n != 0 { // havuz yeni uca uzlaşır
		t.Fatalf("kazılan işlem havuzda: %d", n)
	}
	ps.flushRelayGate(t1.Add(relayReplaceCooldown))
	if len(ps.txRelayCh) != 0 {
		t.Fatal("kazılan sürüm yayıldı")
	}
}

// Tutulan işlem kendi yuvası yakın zamanda yayıldıysa onun süresini de bekler: held yolu yuva
// sınırını atlatamaz.
func TestRelayGateHeldRespectsCooldown(t *testing.T) {
	g := newRelayGate()
	t0 := time.Unix(1_900_000_000, 0)
	a, b := amountTx("s", 5, 1), amountTx("s", 6, 1)
	if !g.admit(&a, t0) || !g.admit(&b, t0.Add(time.Second)) {
		t.Fatal("ilk yayınlar bekletildi")
	}
	a2, b2 := amountTx("s", 5, 2), amountTx("s", 6, 2)
	if g.admit(&a2, t0.Add(2*time.Second)) || g.admit(&b2, t0.Add(3*time.Second)) {
		t.Fatal("yer değiştirmeler hemen yayıldı")
	}
	if got := g.due(t0.Add(relayReplaceCooldown)); len(got) != 1 || got[0] != a2.ID() {
		t.Fatalf("çıkan %v, beklenen yalnızca a2", got)
	}
	if got := g.due(t0.Add(relayReplaceCooldown + time.Second)); len(got) != 1 || got[0] != b2.ID() {
		t.Fatalf("çıkan %v, beklenen b2", got)
	}
}
