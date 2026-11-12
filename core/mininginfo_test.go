package core

import (
	"math"
	"math/big"
	"testing"
)

func mustMiningInfo(t *testing.T, bc *Blockchain, blocks int64) MiningInfo {
	t.Helper()
	m, err := bc.MiningInfo(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Uç genesis iken pencere boştur; hashrate yoktur; zorluk tabanda 1'dir.
func TestMiningInfoGenesis(t *testing.T) {
	bc := newTestChain(t)
	m := mustMiningInfo(t, bc, MiningInfoDefaultBlocks)
	next, err := bc.BitsAfter(bc.tip.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if m.TipHeight != 0 || m.WindowBlocks != 0 || m.HasRate || m.StaleBlocks != 0 || m.Work.Sign() != 0 ||
		m.NextBits != next || m.TargetSpacing != bc.params.TargetSpacing || m.DeepReorg {
		t.Fatalf("genesis: %+v", m)
	}
	if bc.params.Bits == bc.params.PowLimitBits && next == bc.params.PowLimitBits && m.Difficulty != 1 {
		t.Fatalf("taban zorluğu %v, beklenen 1", m.Difficulty)
	}
	for _, b := range []int64{0, -1, MiningInfoMaxBlocks + 1} {
		if _, err := bc.MiningInfo(b); err == nil {
			t.Fatalf("pencere %d kabul edildi", b)
		}
	}
}

// Pencere uç yüksekliğiyle sınırlanır; iş ve süre uç ile pencere başının birikmiş işi ve MTP'si
// farkıdır; hashrate yalnızca pencere en az MedianTimeSpan blok ve süre pozitifken verilir.
func TestMiningInfoWindowAndRate(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	for i := 0; i < 15; i++ {
		if _, err := bc.AddBlock(nil, miner); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ blocks, want int64 }{{20, 15}, {15, 15}, {12, 12}, {5, 5}} {
		m := mustMiningInfo(t, bc, c.blocks)
		if m.WindowBlocks != c.want || m.TipHeight != 15 {
			t.Fatalf("pencere %d: %d blok, uç %d", c.blocks, m.WindowBlocks, m.TipHeight)
		}
		bc.mu.RLock()
		start := bc.index[bc.main[15-c.want]]
		wantWork := new(big.Int).Sub(bc.tip.work, start.work)
		tipMTP, err1 := bc.mtpLocked(bc.tip.Hash, nil)
		startMTP, err2 := bc.mtpLocked(start.Hash, nil)
		bc.mu.RUnlock()
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if m.Work.Cmp(wantWork) != 0 || m.ElapsedSeconds != tipMTP-startMTP || m.StaleBlocks != 0 {
			t.Fatalf("pencere %d: iş %s (beklenen %s), süre %d (beklenen %d)", c.blocks, m.Work, wantWork,
				m.ElapsedSeconds, tipMTP-startMTP)
		}
		wantRate := c.want >= MedianTimeSpan && m.ElapsedSeconds > 0
		if m.HasRate != wantRate {
			t.Fatalf("pencere %d: hashrate var %v, beklenen %v (süre %d)", c.blocks, m.HasRate, wantRate, m.ElapsedSeconds)
		}
		if m.HasRate {
			ref, _ := new(big.Rat).SetFrac(wantWork, big.NewInt(m.ElapsedSeconds)).Float64()
			if math.Abs(m.Hashrate-ref) > 1e-9*ref ||
				math.Abs(m.MeanBlockSeconds-float64(m.ElapsedSeconds)/float64(c.want)) > 1e-12 {
				t.Fatalf("pencere %d: hashrate %v (beklenen %v), ortalama %v", c.blocks, m.Hashrate, ref, m.MeanBlockSeconds)
			}
		}
	}
	// Test saati her çağrıda ilerlediğinden 15 blokluk pencerede süre pozitiftir.
	if m := mustMiningInfo(t, bc, 15); !m.HasRate || m.Hashrate <= 0 {
		t.Fatalf("15 blokta hashrate yok: %+v", m)
	}
}

// Yan bloklar yalnızca pencerenin kapsadığı yüksekliklerde sayılır; reorg sonrası eski ana
// blok yan blok olur.
func TestMiningInfoStaleBlocks(t *testing.T) {
	bc := newTestChain(t)
	a, b := newKey(t).Address, newKey(t).Address
	g := genesisOf(t, bc)
	main := extend(t, bc, g, 4, a) // yükseklik 1..4
	mustAccept(t, bc, main...)
	s2 := blockOn(t, bc, main[0], b) // yükseklik 2, yan
	s4 := blockOn(t, bc, main[2], b) // yükseklik 4, eşit iş: ilk görülen kalır
	mustAccept(t, bc, s2, s4)
	if tipHash(t, bc) != main[3].Hash {
		t.Fatal("eşit işte uç değişti")
	}
	for _, c := range []struct{ blocks, want int64 }{{1, 1}, {2, 1}, {3, 2}, {4, 2}} {
		if m := mustMiningInfo(t, bc, c.blocks); m.StaleBlocks != c.want {
			t.Fatalf("pencere %d: %d yan blok, beklenen %d", c.blocks, m.StaleBlocks, c.want)
		}
	}

	s5 := blockOn(t, bc, s4, b)
	mustAccept(t, bc, s5)
	if tipHash(t, bc) != s5.Hash {
		t.Fatal("yan dal benimsenmedi")
	}
	for _, c := range []struct{ blocks, want int64 }{{1, 0}, {2, 1}, {4, 2}, {5, 2}} {
		if m := mustMiningInfo(t, bc, c.blocks); m.StaleBlocks != c.want {
			t.Fatalf("reorg sonrası pencere %d: %d yan blok, beklenen %d", c.blocks, m.StaleBlocks, c.want)
		}
	}

	// Ana zincirdeki bir yükseklikte saklanan blok sayısının 1'den az olması bozuk durumdur.
	bc.mu.Lock()
	saved := bc.perHeight[5]
	bc.perHeight[5] = 0
	bc.mu.Unlock()
	if _, err := bc.MiningInfo(1); err == nil {
		t.Fatal("tutarsız yükseklik sayacı kabul edildi")
	}
	bc.mu.Lock()
	bc.perHeight[5] = saved
	bc.mu.Unlock()
}
