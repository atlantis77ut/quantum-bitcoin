package network

import (
	"fmt"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// Taşma sıralı, tekrarsız ve sınırlıdır; dolunca atılanları sayar.
func TestTxOverflowQueue(t *testing.T) {
	o := newTxOverflow()
	o.mu.Lock()
	for i := 0; i < txOverflowMax; i++ {
		if !o.pushLocked(fmt.Sprintf("%064x", i)) {
			t.Fatalf("%d. kimlik eklenemedi", i)
		}
	}
	if !o.pushLocked(fmt.Sprintf("%064x", 0)) || len(o.ids) != txOverflowMax {
		t.Fatal("tekrarlanan kimlik eklendi ya da reddedildi")
	}
	if o.pushLocked(fmt.Sprintf("%064x", txOverflowMax)) || o.dropped != 1 {
		t.Fatalf("dolu taşma kabul etti; atılan %d", o.dropped)
	}
	o.mu.Unlock()

	got := o.take(3)
	if len(got) != 3 || got[0] != fmt.Sprintf("%064x", 0) || got[2] != fmt.Sprintf("%064x", 2) {
		t.Fatalf("sıra bozuk: %v", got)
	}
	o.mu.Lock()
	if !o.pushLocked(fmt.Sprintf("%064x", 0)) || o.ids[len(o.ids)-1] != fmt.Sprintf("%064x", 0) {
		t.Fatal("çıkarılan kimlik yeniden eklenemedi")
	}
	o.mu.Unlock()
	// 5000 eklendi, 3 alındı, 1 yeniden eklendi: 4998 kalır.
	if n := len(o.take(txOverflowMax + 10)); n != txOverflowMax-2 || o.len() != 0 {
		t.Fatalf("hepsi alınamadı: %d, kalan %d", n, o.len())
	}
}

// fakeTx imzasız, yalnızca kimliği farklı bir işlemdir (RelayTx doğrulama yapmaz).
func fakeTx(n uint64) core.Transaction {
	return core.Transaction{Receiver: "x", Amount: 1, Nonce: n, ExpiryHeight: 1}
}

func fakeID(n uint64) string {
	tx := fakeTx(n)
	return tx.ID()
}

// Kuyruk dolunca işlem taşmaya girer; taşma boş değilken kuyrukta yer açılsa bile sonraki
// işlemler de taşmaya girer (sıra korunur).
func TestRelayTxOverflowKeepsOrder(t *testing.T) {
	ps := NewP2PServer(":0", newChain(t)) // döngüler çalışmıyor
	for i := 0; i < txRelayQueueLen; i++ {
		ps.RelayTx(fakeTx(uint64(i)))
	}
	if len(ps.txRelayCh) != txRelayQueueLen || ps.txOverflow.len() != 0 {
		t.Fatalf("kuyruk %d, taşma %d", len(ps.txRelayCh), ps.txOverflow.len())
	}
	over := fakeTx(txRelayQueueLen)
	ps.RelayTx(over)
	<-ps.txRelayCh // kuyrukta yer açıldı
	next := fakeTx(txRelayQueueLen + 1)
	ps.RelayTx(next)
	if len(ps.txRelayCh) != txRelayQueueLen-1 {
		t.Fatalf("taşma doluyken işlem kuyruğa girdi: %d", len(ps.txRelayCh))
	}
	got := ps.txOverflow.take(10)
	if len(got) != 2 || got[0] != over.ID() || got[1] != next.ID() {
		t.Fatalf("taşma sırası %v", got)
	}
	// Taşma boşalınca işlemler yine kuyruğa gider.
	ps.RelayTx(fakeTx(txRelayQueueLen + 2))
	if len(ps.txRelayCh) != txRelayQueueLen || ps.txOverflow.len() != 0 {
		t.Fatalf("boş taşmada kuyruk %d, taşma %d", len(ps.txRelayCh), ps.txOverflow.len())
	}
}

// Boşaltma bütçeyi aşmaz, havuzda olmayan kimlikleri atlar.
func TestRelayOverflowBudgetAndMissing(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer(":0", bc) // eş yok: yayın kimseye gitmez
	from := fundKey(t, bc)
	tx := policyTx(t, ps.Mempool, from, 5)
	if _, err := ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	ps.txOverflow.mu.Lock()
	ps.txOverflow.pushLocked(fakeID(1)) // havuzda yok
	ps.txOverflow.pushLocked(tx.ID())
	for i := 0; i < 3*maxTxsPerMsg; i++ {
		ps.txOverflow.pushLocked(fakeID(uint64(100 + i)))
	}
	ps.txOverflow.mu.Unlock()

	stop := make(chan struct{})
	if sent := ps.relayOverflow(maxTxsPerMsg+1, stop); sent != 1 {
		t.Fatalf("gönderilen %d, beklenen 1 (yalnızca havuzdaki)", sent)
	}
	if n := ps.txOverflow.len(); n != 3*maxTxsPerMsg+2-(maxTxsPerMsg+1) {
		t.Fatalf("bütçe aşıldı: kalan %d", n)
	}
	close(stop)
	if sent := ps.relayOverflow(txRatePerSec, stop); sent != 0 || ps.txOverflow.len() == 0 {
		t.Fatal("durdurulmuş döngü taşmayı boşalttı")
	}
}

// Taşmaya düşen havuz işlemi havuz eşitlemesini beklemeden eşe ulaşır.
func TestRelayOverflowReachesPeer(t *testing.T) {
	src := startNode(t, time.Hour)
	alice := fundKey(t, src.bc)
	dst := startNode(t, time.Hour, src.addr)
	dst.ps.TriggerSync()
	waitTip(t, dst.bc, src.bc.TipHash())
	// dst'nin ilk turu (ve içindeki ilk havuz çekmesi) işlem eklenmeden bitsin: sonraki tur bir
	// saat sonra, sonraki çekme 10 dk sonradır; işlem dst'ye ancak taşma yayınıyla ulaşabilir.
	deadline := time.Now().Add(10 * time.Second)
	for dst.ps.syncRounds.Load() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("dst'nin ilk senkronizasyon turu bitmedi")
		}
		time.Sleep(20 * time.Millisecond)
	}
	src.ps.AddPeer(dst.addr)

	tx := policyTx(t, src.ps.Mempool, alice, core.Coin)
	if _, err := src.ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	src.ps.txOverflow.mu.Lock()
	src.ps.txOverflow.pushLocked(tx.ID())
	src.ps.txOverflow.mu.Unlock()

	deadline = time.Now().Add(10 * time.Second)
	for mempoolLen(t, dst.ps.Mempool) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("taşmadaki işlem eşin havuzuna ulaşmadı")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if src.ps.txOverflow.len() != 0 {
		t.Fatal("taşma boşalmadı")
	}
}
