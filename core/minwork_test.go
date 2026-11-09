package core

import (
	"errors"
	"math/big"
	"testing"
)

// mainAt ana zincirin h yüksekliğindeki indeks kaydıdır.
func mainAt(t *testing.T, bc *Blockchain, h int64) *indexEntry {
	t.Helper()
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	for _, e := range bc.index {
		if e.Height == h && bc.isMain(e) {
			return e
		}
	}
	t.Fatalf("ana zincirde %d yüksekliği yok", h)
	return nil
}

func chainWithMinWork(t *testing.T, w *big.Int) *Blockchain {
	t.Helper()
	p := *testParams
	p.MinChainWork = w
	bc, err := NewBlockchain(t.TempDir(), &p)
	if err != nil {
		t.Fatal(err)
	}
	clock := int64(1_900_000_000) // newTestChain ile aynı sabit saat: kaynak blokları gelecek tarihli olmasın
	bc.now = func() int64 { clock++; return clock }
	t.Cleanup(func() { bc.Close() })
	return bc
}

// Toplam işi en az zincir işine ulaşmayan başlık zincirinin gövdeleri indirilmez; ulaşan
// zincir, tek tek eşiğin altındaki ilk blokları dahil, uçtan uca benimsenir.
func TestMinChainWorkGatesSync(t *testing.T) {
	src := newTestChain(t)
	mine(t, src, 10, newKey(t).Address)

	high := chainWithMinWork(t, new(big.Int).Add(mainAt(t, src, 10).work, big.NewInt(1)))
	if !high.BelowMinChainWork() {
		t.Fatal("genesis'teki düğüm eşiğin üstünde sayıldı")
	}
	if err := syncFrom(t, high, src, MaxHeadersPerMsg); err != nil {
		t.Fatal(err)
	}
	if high.TipHash() == src.TipHash() || mainAt(t, high, 0).Hash != high.TipHash() {
		t.Fatal("eşiğin altındaki zincirin gövdeleri indirildi")
	}

	low := chainWithMinWork(t, mainAt(t, src, 5).work)
	if err := syncFrom(t, low, src, MaxHeadersPerMsg); err != nil {
		t.Fatal(err)
	}
	if low.TipHash() != src.TipHash() {
		t.Fatal("eşiği aşan zincir benimsenmedi")
	}
	if low.BelowMinChainWork() {
		t.Fatal("senkronizasyondan sonra düğüm eşiğin altında sayıldı")
	}

	// Kapı kapalıyken (0) davranış eskisi gibidir.
	if chainWithMinWork(t, nil).BelowMinChainWork() {
		t.Fatal("eşik 0 iken düğüm senkronize değil sayıldı")
	}
}

// Ağdan duyurulan blok, zinciri eşiğe ulaşmıyorsa bırakılır; ebeveyni bilinmeyen blok
// senkronizasyon yoluna bırakılır.
func TestRelayBelowMinWork(t *testing.T) {
	src := newTestChain(t)
	mine(t, src, 3, newKey(t).Address)
	block := func(h int64) *Block {
		b, err := src.BlockByHash(mainAt(t, src, h).Hash)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b1, b2 := block(1), block(2)

	dst := chainWithMinWork(t, mainAt(t, src, 3).work)
	if !dst.RelayBelowMinWork(b1) {
		t.Fatal("eşiğin altındaki blok kabul edilecekti")
	}
	if dst.RelayBelowMinWork(b2) {
		t.Fatal("ebeveyni bilinmeyen blok kapıda bırakıldı (senkronizasyonu tetiklemeli)")
	}
	exact := chainWithMinWork(t, mainAt(t, src, 1).work)
	if exact.RelayBelowMinWork(b1) {
		t.Fatal("eşiğe tam ulaşan blok bırakıldı")
	}
	if chainWithMinWork(t, nil).RelayBelowMinWork(b1) {
		t.Fatal("eşik 0 iken blok bırakıldı")
	}
	// AcceptBlock kapıya tabi değildir (senkronizasyon gövdeleri tek tek eşiğin altındadır).
	if err := dst.AcceptBlock(b1); err != nil {
		t.Fatalf("AcceptBlock eşiğe takıldı: %v", err)
	}
}

func TestMinChainWorkValidate(t *testing.T) {
	p := *testParams
	p.MinChainWork = big.NewInt(-1)
	if err := p.Validate(); !errors.Is(err, ErrBadParams) {
		t.Fatalf("negatif en az zincir işi: %v", err)
	}
	for _, q := range []*Params{MainnetParams, TestnetParams, RegtestParams} {
		if q.minChainWork().Sign() != 0 {
			t.Errorf("%s en az zincir işi 0 olmalı (lansman öncesi)", q.Name)
		}
	}
}
