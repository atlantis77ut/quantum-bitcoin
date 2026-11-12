package core

import (
	"math"
	"testing"
)

// HeaderByHash yalnızca ana zincirdeki blokları döndürür: reorg'da düşen blok ve bilinmeyen
// özet bulunmaz. HeadersRange ana zinciri yükseklik sırasıyla sayfalar, sınırları kırpar ve
// taşmaz.
func TestHeaderByHashAndRange(t *testing.T) {
	bc := openTestChain(t, t.TempDir())
	miner := newKey(t).Address
	g := genesisOf(t, bc)
	a := extend(t, bc, g, 2, miner)
	mustAccept(t, bc, a...)
	b := extend(t, bc, a[0], 3, miner) // a[1]'i düşüren daha uzun dal
	mustAccept(t, bc, b...)
	if tipHash(t, bc) != b[2].Hash {
		t.Fatal("uzun dala geçilmedi")
	}

	if _, ok := bc.HeaderByHash(a[1].Hash); ok {
		t.Fatal("reorg'da düşen blok ana zincir başlığı olarak döndü")
	}
	if _, ok := bc.HeaderByHash(genesisPrevHash); ok {
		t.Fatal("bilinmeyen özet bulundu")
	}
	h, ok := bc.HeaderByHash(b[1].Hash)
	if !ok || h.Hash != b[1].Hash || h.Index != 3 || h.PrevHash != b[0].Hash {
		t.Fatalf("başlık %+v, %v", h, ok)
	}
	if tip := bc.TipHeader(); tip.Hash != b[2].Hash || tip.Index != 4 {
		t.Fatalf("uç başlığı %+v", tip)
	}

	hdrs, tipHeight, tipHash := bc.HeadersRange(1, 10)
	if tipHeight != 4 || tipHash != b[2].Hash || len(hdrs) != 4 {
		t.Fatalf("aralık %d başlık, uç %d %s", len(hdrs), tipHeight, tipHash)
	}
	want := []string{a[0].Hash, b[0].Hash, b[1].Hash, b[2].Hash}
	for i, h := range hdrs {
		if h.Hash != want[i] || h.Index != int64(i+1) {
			t.Fatalf("başlık %d: %s", i, h.Hash)
		}
	}
	if hdrs, _, _ := bc.HeadersRange(0, 2); len(hdrs) != 2 || hdrs[0].Hash != g.Hash {
		t.Fatal("genesis'ten başlayan sayfa yanlış")
	}
	for _, c := range []struct {
		from  int64
		count int
	}{{5, 10}, {math.MaxInt64, math.MaxInt}, {-1, 10}, {0, 0}} {
		if hdrs, _, _ := bc.HeadersRange(c.from, c.count); len(hdrs) != 0 {
			t.Fatalf("from %d count %d: %d başlık", c.from, c.count, len(hdrs))
		}
	}
	if hdrs, _, _ := bc.HeadersRange(4, math.MaxInt); len(hdrs) != 1 || hdrs[0].Hash != b[2].Hash {
		t.Fatal("büyük count uçta kırpılmadı")
	}
}
