package core

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

// Ağ tabanı geçerli olmalı, başlangıç hedefi tabandan kolay olamaz, mainnet tabanı 2^224'ten
// kolay olamaz.
func TestPowLimitValidate(t *testing.T) {
	bad := map[string]func(p *Params){
		"geçersiz taban":           func(p *Params) { p.PowLimitBits = 0x21010000 },
		"taban sıfır":              func(p *Params) { p.PowLimitBits = 0 },
		"başlangıç tabandan kolay": func(p *Params) { p.PowLimitBits = 0x1f00ffff },
	}
	for name, mut := range bad {
		p := *RegtestParams
		mut(&p)
		if err := p.Validate(); !errors.Is(err, ErrBadParams) {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := *MainnetParams
	m.Bits, m.PowLimitBits = 0x1c00ffff, 0x1d00ffff
	if err := m.Validate(); err != nil {
		t.Fatalf("2^224 tabanlı mainnet reddedildi: %v", err)
	}
	m.Bits, m.PowLimitBits = 0x1d00ffff, 0x1d01ffff
	if err := m.Validate(); !errors.Is(err, ErrBadParams) {
		t.Fatalf("2^224'ten kolay mainnet tabanı kabul edildi: %v", err)
	}
	// Aynı taban testnet'te serbesttir.
	tn := *TestnetParams
	if err := tn.Validate(); err != nil {
		t.Fatal(err)
	}
	if TestnetParams.PowLimitBits == TestnetParams.Bits {
		t.Fatal("testnet tabanı başlangıçla aynı (kullanıcı kararı: 2^8 kat kolay)")
	}
	if got, want := TestnetParams.powLimitTarget(), new(big.Int).Lsh(mustTarget(t, TestnetParams.Bits), 8); got.Cmp(want) != 0 {
		t.Fatalf("testnet tabanı %s, beklenen %s", got, want)
	}
}

func mustTarget(t *testing.T, bits uint32) *big.Int {
	t.Helper()
	x, err := TargetFromBits(bits)
	if err != nil {
		t.Fatalf("%08x: %v", bits, err)
	}
	return x
}

// ASERT küresel sınıra değil ağ tabanına kırpar; testnet'te hedef başlangıcın altına
// (daha kolaya) inebilir ama tabanı geçemez.
func TestASERTClampsToNetworkFloor(t *testing.T) {
	p := TestnetParams
	anchor := int64(1_800_000_000)
	onTime := anchor + p.TargetSpacing*499
	if got := p.nextBits(500, onTime+3*p.HalfLife, anchor); mustTarget(t, got).Cmp(mustTarget(t, p.Bits)) <= 0 {
		t.Fatalf("geride kalan testnet başlangıçtan kolaylaşmadı: %08x", got)
	}
	if got := p.nextBits(500, onTime+1000*p.HalfLife, anchor); got != p.PowLimitBits {
		t.Fatalf("kırpılmış hedef %08x, beklenen taban %08x", got, p.PowLimitBits)
	}
	q := *TestnetParams
	q.PowLimitBits = q.Bits
	if got := q.nextBits(500, onTime+1000*q.HalfLife, anchor); got != q.Bits {
		t.Fatalf("başlangıca eşit tabanda hedef %08x", got)
	}
}

// Ebeveyni bilinmeyen blok ağ tabanından kolay hedef ilan edemez (ucun 4 katı içinde olsa bile).
func TestOrphanEasierThanFloorRejected(t *testing.T) {
	p := *TestnetParams
	p.PowLimitBits = p.Bits // taban = başlangıç: 2 kat kolay hedef 4 kat sınırının içindedir
	bc, err := NewBlockchain(t.TempDir(), &p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	easier := bigToCompact(new(big.Int).Lsh(mustTarget(t, p.Bits), 1))
	orphan := &Block{Version: BlockVersion, Index: 5, PrevHash: strings.Repeat("ab", 32), Bits: easier}
	if err := bc.bitsGate(orphan); !errors.Is(err, ErrOrphanTooEasy) {
		t.Fatalf("tabandan kolay yetim: %v", err)
	}
	orphan.Bits = p.Bits
	if err := bc.bitsGate(orphan); err != nil {
		t.Fatalf("taban hedefli yetim: %v", err)
	}
}

// Lansman önerisi: başlangıç mainnet tavanına kırpılır, taban hedef cinsinden
// min(başlangıç · 2^16, tavan) olur ve başlangıçtan kolay ya da ona eşittir.
func TestLaunchTargets(t *testing.T) {
	ceiling := mustTarget(t, mainnetMaxPowLimitBits)
	for _, c := range []struct {
		h       float64
		capped  bool // taban tavana kırpılır
		atLimit bool // başlangıç da tavanda
	}{
		{1, true, true},      // tek CPU'dan bile yavaş: ikisi de tavanda
		{3e9, true, false},   // ~3 GH/s (GPU ölçeği): oran 2^16'nın altında kalır
		{1e15, false, false}, // 1 PH/s: taban tam olarak başlangıç · 2^16
	} {
		sb, fb, err := LaunchTargets(c.h, MainnetParams.TargetSpacing)
		if err != nil {
			t.Fatalf("%v: %v", c.h, err)
		}
		want, _ := BitsForHashrate(c.h, MainnetParams.TargetSpacing, mainnetMaxPowLimitBits)
		start, floor := mustTarget(t, sb), mustTarget(t, fb)
		if sb != want || (sb == mainnetMaxPowLimitBits) != c.atLimit {
			t.Fatalf("%v: başlangıç %08x, beklenen %08x", c.h, sb, want)
		}
		if floor.Cmp(start) < 0 || floor.Cmp(ceiling) > 0 {
			t.Fatalf("%v: taban %08x başlangıç %08x ile tavan arasında değil", c.h, fb, sb)
		}
		if c.capped {
			if fb != mainnetMaxPowLimitBits {
				t.Fatalf("%v: taban %08x, beklenen tavan", c.h, fb)
			}
		} else if new(big.Int).Lsh(start, 16).Cmp(floor) != 0 {
			t.Fatalf("%v: taban %08x başlangıç · 2^16 değil (%08x)", c.h, fb, sb)
		}
	}
	if _, _, err := LaunchTargets(0, 60); err == nil {
		t.Fatal("sıfır hashrate kabul edildi")
	}
}

func TestBitsForHashrate(t *testing.T) {
	limit := uint32(0x1d00ffff)
	h := 1e15 // 1 PH/s
	bits, err := BitsForHashrate(h, 60, limit)
	if err != nil {
		t.Fatal(err)
	}
	// hedef · h · spacing ≈ 2^256
	got, _ := new(big.Float).Mul(new(big.Float).SetInt(mustTarget(t, bits)), big.NewFloat(h*60)).Float64()
	if r := got / math.Ldexp(1, 256); r < 0.999 || r > 1.0001 {
		t.Fatalf("hedef oranı %v", r)
	}
	if b, err := BitsForHashrate(1, 60, limit); err != nil || b != limit {
		t.Fatalf("tabana kırpılmadı: %08x %v", b, err)
	}
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := BitsForHashrate(bad, 60, limit); err == nil {
			t.Errorf("%v hashrate kabul edildi", bad)
		}
	}
	if _, err := BitsForHashrate(h, 0, limit); err == nil {
		t.Error("sıfır blok aralığı kabul edildi")
	}
}
