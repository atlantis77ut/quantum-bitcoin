package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestTimestampRuleConstants(t *testing.T) {
	if MaxFutureDrift != 300 || pendingWindow != 600 || MedianTimeSpan != 11 {
		t.Fatalf("FTL %d, bekleme penceresi %d, MTP %d", MaxFutureDrift, pendingWindow, MedianTimeSpan)
	}
}

// futureBlock ucun üzerine, yerel saatin `ahead` saniye ilerisinde bir blok kazar.
func futureBlock(t *testing.T, bc *Blockchain, ahead int64, txs ...Transaction) *Block {
	t.Helper()
	prev, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Transaction{coinbaseWithFees(newKey(t).Address, txs...)}, txs...)
	b := &Block{Index: prev.Index + 1, Timestamp: bc.now() + ahead, Transactions: all, PrevHash: prev.Hash}
	mineAt(t, bc, b)
	return b
}

func fixedClock(bc *Blockchain, at int64) *int64 {
	clock := at
	bc.now = func() int64 { return clock }
	return &clock
}

// Pencere dışındaki gelecek tarihli blokta imza doğrulanmaz: geçersiz imzalı gövde bile
// ErrTimestampFuture ile ucuzca düşer ve hiçbir önbelleğe girmez.
func TestFarFutureBlockRejectedBeforeSignatures(t *testing.T) {
	bc := newTestChain(t)
	fixedClock(bc, 1_900_000_000)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	bad := signedTx(t, alice, bob.Address, Coin, 1)
	bad.Amount++ // imza artık geçersiz

	b := futureBlock(t, bc, pendingWindow+1, bad)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("hata %v, beklenen ErrTimestampFuture (imzadan önce)", err)
	}
	if bc.rejected.has(b.Hash) || bc.pending.len() != 0 {
		t.Fatal("pencere dışı blok önbelleğe ya da kuyruğa alındı")
	}

	// Pencere içinde gövde bir kez doğrulanır; geçersizse kalıcı ret önbelleğine girer.
	b2 := futureBlock(t, bc, MaxFutureDrift+1, bad)
	if err := bc.AcceptBlock(b2); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("pencere içi geçersiz gövde: %v", err)
	}
	if !bc.rejected.has(b2.Hash) || bc.pending.len() != 0 {
		t.Fatal("geçersiz gövdeli gelecek blok kuyruğa alındı ya da önbelleğe girmedi")
	}
}

// Pencere içindeki geçerli blok kuyrukta bekler, tekrar gönderilince yeniden doğrulanmaz ve
// saat yetişince ProcessPending ile zincire eklenir.
func TestPendingBlockAddedWhenClockCatchesUp(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tx := signedTxFee(t, alice, bob.Address, 3*Coin, 2, 1)
	b := futureBlock(t, bc, MaxFutureDrift+60, tx)

	checks := bc.bodyChecks.Load()
	for i := 0; i < 3; i++ {
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
			t.Fatalf("deneme %d: %v", i, err)
		}
	}
	if d := bc.bodyChecks.Load() - checks; d != 1 {
		t.Fatalf("gövde %d kez doğrulandı, beklenen 1", d)
	}
	if bc.pending.len() != 1 || bc.rejected.has(b.Hash) {
		t.Fatalf("kuyruk %d, önbellekte %v", bc.pending.len(), bc.rejected.has(b.Hash))
	}
	if n := bc.ProcessPending(); n != 0 {
		t.Fatalf("saat yetişmeden %d blok eklendi", n)
	}

	*clock += 60 // artık tam FTL sınırında
	if n := bc.ProcessPending(); n != 1 {
		t.Fatalf("eklenen %d, beklenen 1", n)
	}
	tip, _ := bc.GetLatestBlock()
	if tip.Hash != b.Hash || mustBalance(t, bc, bob.Address) != 3*Coin || bc.pending.len() != 0 {
		t.Fatal("bekleyen blok doğru uygulanmadı")
	}
	if n := bc.ProcessPending(); n != 0 {
		t.Fatalf("blok iki kez eklendi: %d", n)
	}
}

// Uç aynı yükseklikte başka bir blokla ilerlerse eski uca bağlı bekleyen blok yan blok olarak
// saklanır; işi uçtan fazla olmadığından (eşitlikte ilk görülen) uç değişmez.
func TestStalePendingBlockStoredAsSide(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	b := futureBlock(t, bc, MaxFutureDrift+30)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
		t.Fatal(err)
	}
	miner := newKey(t)
	*clock += 1
	if _, err := bc.AddBlock(nil, miner.Address); err != nil {
		t.Fatal(err)
	}
	*clock += 3600
	if n := bc.ProcessPending(); n != 1 || bc.pending.len() != 0 {
		t.Fatalf("kabul edilen %d, kuyruk %d", n, bc.pending.len())
	}
	tip, _ := bc.GetLatestBlock()
	if tip.Transactions[0].Receiver != miner.Address {
		t.Fatal("uç değişti")
	}
	if _, ok := bc.index[b.Hash]; !ok || bc.isMain(bc.index[b.Hash]) {
		t.Fatal("eski uca bağlı blok yan blok olarak saklanmadı")
	}
}

// Sınır değerleri: tam FTL doğrudan kabul edilir, tam 2×FTL kuyruğa girer, ötesi gövde
// doğrulanmadan düşer.
func TestFutureBoundaries(t *testing.T) {
	bc := newTestChain(t)
	fixedClock(bc, 1_900_000_000)
	if err := bc.AcceptBlock(futureBlock(t, bc, MaxFutureDrift)); err != nil {
		t.Fatalf("tam FTL: %v", err)
	}
	if err := bc.AcceptBlock(futureBlock(t, bc, pendingWindow)); !errors.Is(err, ErrTimestampFuture) || bc.pending.len() != 1 {
		t.Fatalf("tam 2×FTL: %v, kuyruk %d", err, bc.pending.len())
	}
	checks := bc.bodyChecks.Load()
	if err := bc.AcceptBlock(futureBlock(t, bc, pendingWindow+1)); !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("2×FTL+1: %v", err)
	}
	if bc.bodyChecks.Load() != checks || bc.pending.len() != 1 {
		t.Fatal("pencere dışı blok doğrulandı ya da kuyruğa girdi")
	}
}

// Kuyruk doluyken en uzak bloktan daha yakın olmayan blok imza doğrulaması yaptırmaz;
// daha yakın blok en uzağın yerini alır ve tahliye edilen blok yeniden gönderilince de
// doğrulanmaz.
func TestFullQueueDoesNotReverify(t *testing.T) {
	bc := newTestChain(t)
	fixedClock(bc, 1_900_000_000)
	var blocks []*Block
	for i := 0; i < MaxPendingBlocks; i++ {
		b := futureBlock(t, bc, MaxFutureDrift+10+int64(i))
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
			t.Fatal(err)
		}
		blocks = append(blocks, b)
	}
	if bc.pending.len() != MaxPendingBlocks {
		t.Fatalf("kuyruk %d", bc.pending.len())
	}
	checks := bc.bodyChecks.Load()
	far := futureBlock(t, bc, MaxFutureDrift+100)
	for i := 0; i < 3; i++ {
		if err := bc.AcceptBlock(far); !errors.Is(err, ErrTimestampFuture) {
			t.Fatal(err)
		}
	}
	if bc.bodyChecks.Load() != checks || bc.pending.has(far.Hash) {
		t.Fatal("dolu kuyruğa giremeyen blok doğrulandı ya da eklendi")
	}

	near := futureBlock(t, bc, MaxFutureDrift+1)
	if err := bc.AcceptBlock(near); !errors.Is(err, ErrTimestampFuture) || !bc.pending.has(near.Hash) {
		t.Fatalf("yakın blok kuyruğa girmedi: %v", err)
	}
	evicted := blocks[MaxPendingBlocks-1]
	if bc.pending.has(evicted.Hash) {
		t.Fatal("en uzak blok tahliye edilmedi")
	}
	checks = bc.bodyChecks.Load()
	if err := bc.AcceptBlock(evicted); !errors.Is(err, ErrTimestampFuture) || bc.bodyChecks.Load() != checks {
		t.Fatalf("tahliye edilen blok yeniden doğrulandı: %v", err)
	}
}

// Tahliye + yetim budama döngüsü: uca bağlı uzak X ve X'in yakın zamanlı çocukları kuyruğu
// doldurur, son çocuk X'i tahliye eder, sonraki turda çocuklar yetim kalıp atılır. Aynı
// bloklar yeniden gönderildiğinde hiçbiri yeniden doğrulanmamalı.
func TestEvictPruneCycleDoesNotReverify(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	fund(t, bc, newKey(t), 6) // MTP şimdiki zamana yakın
	x := futureBlock(t, bc, MaxFutureDrift+200)
	if err := bc.AcceptBlock(x); !errors.Is(err, ErrTimestampFuture) {
		t.Fatal(err)
	}
	var kids []*Block
	for i := 0; i < MaxPendingBlocks; i++ {
		c := &Block{Index: x.Index + 1, Timestamp: *clock + 10 + int64(i), PrevHash: x.Hash,
			Transactions: []Transaction{coinbaseTo(newKey(t).Address)}}
		mineAt(t, bc, c)
		if err := bc.AcceptBlock(c); !errors.Is(err, ErrParentPending) {
			t.Fatalf("çocuk %d: %v", i, err)
		}
		kids = append(kids, c)
	}
	if bc.pending.has(x.Hash) || !bc.pending.wasDropped(x.Hash) {
		t.Fatal("X tahliye edilmedi")
	}
	bc.ProcessPending()
	if bc.pending.len() != 0 {
		t.Fatalf("yetim çocuklar atılmadı: %d", bc.pending.len())
	}
	checks := bc.bodyChecks.Load()
	for _, b := range append([]*Block{x}, kids...) {
		bc.AcceptBlock(b)
	}
	if d := bc.bodyChecks.Load() - checks; d != 0 {
		t.Fatalf("atılan bloklar %d kez yeniden doğrulandı", d)
	}
}

// Ebeveyni kuyrukta olan bloklar atılmaz; ebeveyn eklenince aynı turda onlar da eklenir.
func TestPendingChildBlocks(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	// Medyan (MTP) şimdiki zamana yakın olsun: ileri tarihli iki blok sonrasında bile
	// zamanı geçerli bir çocuk (c2) MTP'den büyük kalabilsin.
	fund(t, bc, newKey(t), 6)
	parent := futureBlock(t, bc, MaxFutureDrift+60)
	if err := bc.AcceptBlock(parent); !errors.Is(err, ErrTimestampFuture) {
		t.Fatal(err)
	}
	child := func(p *Block, ts int64) *Block {
		b := &Block{Index: p.Index + 1, Timestamp: ts, PrevHash: p.Hash,
			Transactions: []Transaction{coinbaseTo(newKey(t).Address)}}
		mineAt(t, bc, b)
		return b
	}
	c1 := child(parent, parent.Timestamp+60) // kendisi de ileride
	if err := bc.AcceptBlock(c1); !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("ileri çocuk: %v", err)
	}
	c2 := child(c1, *clock+10) // zamanı geçerli ama ebeveyni beklemede
	if err := bc.AcceptBlock(c2); !errors.Is(err, ErrParentPending) {
		t.Fatalf("ebeveyni bekleyen çocuk: %v", err)
	}
	if bc.pending.len() != 3 {
		t.Fatalf("kuyruk %d, beklenen 3", bc.pending.len())
	}
	*clock += 60
	if n := bc.ProcessPending(); n != 1 {
		t.Fatalf("yalnızca ebeveynin zamanı geldi; eklenen %d", n)
	}
	*clock += 60
	if n := bc.ProcessPending(); n != 2 {
		t.Fatalf("iki çocuk eklenmeliydi; eklenen %d", n)
	}
	tip, _ := bc.GetLatestBlock()
	if tip.Hash != c2.Hash || bc.pending.len() != 0 {
		t.Fatal("çocuklar sırayla eklenmedi")
	}
}

// Geçici hata (burada: iki saat okuması arasında saatin geri gitmesi) bekleyen bloğu
// kaybettirmez; blok kuyruğa geri döner.
func TestPendingRequeuedOnTransientError(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	b := futureBlock(t, bc, MaxFutureDrift+60)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
		t.Fatal(err)
	}
	at := *clock + 60
	calls := 0
	bc.now = func() int64 {
		calls++
		if calls == 1 {
			return at // take: zamanı gelmiş görünür
		}
		return at - 100 // durumlu kontrol: saat geri gitti
	}
	if n := bc.ProcessPending(); n != 0 || !bc.pending.has(b.Hash) {
		t.Fatalf("eklenen %d, kuyrukta %v", n, bc.pending.has(b.Hash))
	}
	fixedClock(bc, at)
	if n := bc.ProcessPending(); n != 1 {
		t.Fatalf("yeniden denemede eklenen %d", n)
	}
}

// Kuyruk sınırlıdır; dolunca en uzak geleceğe ait blok düşer, daha uzak yeni blok eklenmez.
func TestPendingQueueBounded(t *testing.T) {
	var q pendingBlocks
	for i := 0; i < MaxPendingBlocks; i++ {
		if !q.add(&Block{Hash: fmt.Sprintf("h%d", i), PrevHash: "tip", Timestamp: int64(100 + i)}) {
			t.Fatalf("blok %d eklenmedi", i)
		}
	}
	if q.add(&Block{Hash: "far", PrevHash: "tip", Timestamp: 1000}) {
		t.Fatal("kuyruk doluyken daha uzak blok eklendi")
	}
	if !q.add(&Block{Hash: "near", PrevHash: "tip", Timestamp: 50}) {
		t.Fatal("daha yakın blok eklenmedi")
	}
	last := fmt.Sprintf("h%d", MaxPendingBlocks-1)
	if q.len() != MaxPendingBlocks || q.has(last) || !q.has("near") {
		t.Fatalf("tahliye yanlış: len %d, %s var mı %v", q.len(), last, q.has(last))
	}
	if !q.add(&Block{Hash: "near", PrevHash: "tip", Timestamp: 50}) || q.len() != MaxPendingBlocks {
		t.Fatal("aynı blok iki kez sayıldı")
	}
	if !q.wasDropped(last) {
		t.Fatal("tahliye edilen blok 'atıldı' kümesine yazılmadı")
	}
	ready := q.take(func(h string) bool { return h == "other" }, 1<<40)
	if len(ready) != 0 || q.len() != 0 {
		t.Fatal("eski uca bağlı bloklar atılmadı")
	}
	if !q.wasDropped("near") || !q.wasDropped("h0") {
		t.Fatal("yetim kalıp atılan bloklar 'atıldı' kümesine yazılmadı")
	}
}
