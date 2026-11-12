package core

import (
	"errors"
	"testing"
)

// Kazım zincir kilidini tutmaz: nonce araması sırasında yazma kilidi alınabilir.
func TestMineDoesNotHoldLock(t *testing.T) {
	bc := newTestChain(t)
	bc.mineChunk = 1
	calls := 0
	bc.mineHook = func() {
		calls++
		if !bc.mu.TryLock() {
			t.Error("kazım sırasında zincir kilidi tutuluyor")
			return
		}
		bc.mu.Unlock()
	}
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("kazım kancası çağrılmadı")
	}
}

// Uzun kazımda zaman damgası yerel saate tazelenir; hedef (yalnızca ebeveyne bağlı) değişmez
// ve blok geçerlidir.
func TestMineRefreshesTimestamp(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	bc.mineChunk = 1
	for attempt := 0; attempt < 50; attempt++ {
		tmpl, err := bc.NewTemplate(nil, newKey(t).Address)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		bc.mineHook = func() { calls++; *clock += 100 }
		b, err := bc.Mine(tmpl, nil)
		if err != nil {
			t.Fatal(err)
		}
		if calls < 2 {
			continue // ilk denemede bulundu: tazeleme olmadı, yeniden dene
		}
		if b.Timestamp <= tmpl.Block.Timestamp || b.Bits != tmpl.Block.Bits {
			t.Fatalf("damga %d → %d, hedef %08x → %08x", tmpl.Block.Timestamp, b.Timestamp, tmpl.Block.Bits, b.Bits)
		}
		if err := bc.SubmitBlock(b); err != nil {
			t.Fatalf("tazelenmiş blok reddedildi: %v", err)
		}
		return
	}
	t.Fatal("50 denemede çok dilimli kazım olmadı")
}

// Kazım sürerken uç değişirse şablon eskir: kazım durur, eskimiş blok teslim edilemez.
func TestMineStaleTemplate(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	old, err := bc.NewTemplate(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	minedOld, err := bc.Mine(old, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.Mine(old, nil); !errors.Is(err, ErrStaleTemplate) {
		t.Fatalf("eski şablon: %v, beklenen ErrStaleTemplate", err)
	}
	if err := bc.SubmitBlock(minedOld); !errors.Is(err, ErrStaleTemplate) {
		t.Fatalf("eski blok: %v, beklenen ErrStaleTemplate", err)
	}
}

// Durdurma kanalı kapandıysa kazım hemen biter.
func TestMineStops(t *testing.T) {
	bc := newTestChain(t)
	tmpl, err := bc.NewTemplate(nil, newKey(t).Address)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	close(stop)
	if _, err := bc.Mine(tmpl, stop); !errors.Is(err, ErrMiningStopped) {
		t.Fatalf("durdurulan kazım: %v", err)
	}
	if _, err := bc.NewTemplate(nil, "geçersiz"); !errors.Is(err, ErrBadReceiver) {
		t.Fatalf("geçersiz madenci adresi: %v", err)
	}
}
