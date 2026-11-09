package core

import (
	"math"
	"math/big"
	"testing"
)

// Takvimde (blok başına 60 sn) kazılan zincirde iş, blok sayısıyla orantılıdır ve geçen
// medyan zaman blok süresine yakındır; uç ve yan/bilinmeyen bloklar doğru raporlanır.
func TestConfirmationOnSchedule(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	miner := newKey(t).Address
	for i := 0; i < 30; i++ {
		*clock += testParams.TargetSpacing
		mine(t, bc, 1, miner)
	}
	w, err := blockWork(testParams.Bits)
	if err != nil {
		t.Fatal(err)
	}
	c, err := bc.ConfirmationOf(bc.main[1])
	if err != nil {
		t.Fatal(err)
	}
	if c.Blocks != 30 || c.Work.Cmp(new(big.Int).Mul(w, big.NewInt(30))) != 0 {
		t.Fatalf("ilk blok: %d onay, iş %s", c.Blocks, c.Work)
	}
	if got := bc.WorkInBlocks(c.Work); math.Abs(got-30) > 1e-9 {
		t.Fatalf("başlangıç bloğu cinsinden iş %v, beklenen 30", got)
	}
	// MTP(1. blok) = 1. bloğun damgası (genesis ile iki değerin büyüğü); MTP(uç) = son 11
	// damganın medyanı, uçtan 5 blok geride: 25 − 1 = 24 blok aralığı.
	if want := 24 * testParams.TargetSpacing; c.ElapsedSeconds != want {
		t.Fatalf("geçen medyan zaman %d sn, beklenen %d", c.ElapsedSeconds, want)
	}
	if c.DeepReorg || c.BelowMinWork {
		t.Fatalf("alarmlar: %+v", c)
	}
	tip, err := bc.ConfirmationOf(bc.tip.Hash)
	if err != nil || tip.Blocks != 1 || tip.Work.Cmp(w) != 0 || tip.ElapsedSeconds != 0 {
		t.Fatalf("uç: %+v, %v", tip, err)
	}
	for _, h := range []string{"yok", genesisOf(t, bc).Hash + "x"} {
		if c, err := bc.ConfirmationOf(h); err != nil || c.Blocks != 0 || c.Work.Sign() != 0 || c.ElapsedSeconds != 0 {
			t.Fatalf("bilinmeyen blok: %+v, %v", c, err)
		}
	}
	side := blockOn(t, bc, bc.mustMainBlock(t, 29), newKey(t).Address)
	mustAccept(t, bc, side)
	if c, err := bc.ConfirmationOf(side.Hash); err != nil || c.Blocks != 0 || c.Work.Sign() != 0 {
		t.Fatalf("yan blok: %+v, %v", c, err)
	}
	if g, err := bc.ConfirmationOf(bc.main[0]); err != nil || g.Blocks != 31 || g.Work.Cmp(bc.tip.work) != 0 {
		t.Fatalf("genesis: %+v, %v", g, err)
	}
}

// Saniyeler içinde kazılan bir blok seli çok sayıda "onay" üretir ama medyan zamanı ancak
// gerçek saat kadar ilerletir: zamana dayalı gösterge seli ayırt eder.
func TestConfirmationFloodHasLittleElapsedTime(t *testing.T) {
	bc := newTestChain(t)
	fixedClock(bc, 1_900_000_000) // saat durur: tüm bloklar aynı anda kazılır
	mine(t, bc, 30, newKey(t).Address)
	c, err := bc.ConfirmationOf(bc.main[1])
	if err != nil {
		t.Fatal(err)
	}
	if c.Blocks != 30 {
		t.Fatalf("onay %d", c.Blocks)
	}
	if c.ElapsedSeconds >= 60 {
		t.Fatalf("selde geçen medyan zaman %d sn (30 blok takvimde ~1740 sn)", c.ElapsedSeconds)
	}
}
