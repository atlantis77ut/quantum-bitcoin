package network

import (
	"testing"

	"quantum-bitcoin/core"
)

// Geri yüklenen işlemler ilk senkronizasyondan önce yayılmaz; sonra yalnızca hâlâ havuzda olan
// geri yüklenenler, o anki sırayla ve bir kez taşmaya alınır.
func TestRestoredPoolRelay(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	src := NewP2PServer("127.0.0.1:0", bc)
	tx1 := policyTx(t, src.Mempool, alice, core.Coin)
	if _, err := src.Mempool.Add(tx1); err != nil {
		t.Fatal(err)
	}
	tx2 := policyTx(t, src.Mempool, alice, core.Coin)
	if _, err := src.Mempool.Add(tx2); err != nil {
		t.Fatal(err)
	}
	if saved, err := src.Mempool.Save(); err != nil || !saved {
		t.Fatalf("kayıt: %v %v", saved, err)
	}

	ps := NewP2PServer("127.0.0.1:0", bc)
	restored, skipped, err := ps.LoadMempool()
	if err != nil || restored != 2 || skipped != 0 {
		t.Fatalf("yükleme: %d %d %v", restored, skipped, err)
	}
	tx3 := policyTx(t, ps.Mempool, alice, core.Coin) // geri yüklenmedi
	if _, err := ps.Mempool.Add(tx3); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock([]core.Transaction{tx1}, minerAddr(t)); err != nil {
		t.Fatal(err)
	}

	if ps.restoredReady() {
		t.Fatal("ilk senkronizasyondan önce hazır sayıldı")
	}
	ps.syncRounds.Add(1)
	if !ps.restoredReady() {
		t.Fatal("senkronizasyondan sonra hazır sayılmadı")
	}
	if n := ps.enqueueRestored(); n != 1 {
		t.Fatalf("taşmaya alınan %d, beklenen 1 (kazılan ve yeni işlem hariç)", n)
	}
	if got := ps.txOverflow.take(10); len(got) != 1 || got[0] != tx2.ID() {
		t.Fatalf("taşma %v, beklenen %s", got, tx2.ID())
	}
	if len(ps.txRelayCh) != 0 {
		t.Fatal("geri yüklenen işlem kanala yazıldı")
	}
	if n := ps.enqueueRestored(); n != 0 {
		t.Fatalf("ikinci kez taşmaya alındı: %d", n)
	}
}

// Taşmaya sığmayan geri yüklenenler atılır (havuz eşitlemesine kalır); sayılmaz.
func TestRestoredPoolRelayOverflowFull(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	src := NewP2PServer("127.0.0.1:0", bc)
	tx1 := policyTx(t, src.Mempool, alice, core.Coin)
	if _, err := src.Mempool.Add(tx1); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Mempool.Save(); err != nil {
		t.Fatal(err)
	}
	ps := NewP2PServer("127.0.0.1:0", bc)
	if r, _, err := ps.LoadMempool(); err != nil || r != 1 {
		t.Fatalf("yükleme: %d %v", r, err)
	}
	ps.txOverflow.mu.Lock()
	for i := 0; i < txOverflowMax; i++ {
		ps.txOverflow.pushLocked(fakeID(uint64(i)))
	}
	before := ps.txOverflow.dropped
	ps.txOverflow.mu.Unlock()
	if n := ps.enqueueRestored(); n != 0 {
		t.Fatalf("dolu taşmaya alındı: %d", n)
	}
	ps.txOverflow.mu.Lock()
	defer ps.txOverflow.mu.Unlock()
	if ps.txOverflow.dropped != before+1 {
		t.Fatalf("atılan sayacı %d, beklenen %d", ps.txOverflow.dropped, before+1)
	}
}
