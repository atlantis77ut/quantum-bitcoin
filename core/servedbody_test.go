package core

import (
	"encoding/json"
	"errors"
	"testing"
)

// Eşlere sunulan gövde (BlockByHash) başlığa yeniden bağlanır: işlemi değiştirilmiş (özet alanı
// sağlam) ya da başka anahtardaki gövde ErrCorruptState verir; sağlam ana zincir ve yan dal
// blokları sunulur.
func TestBlockByHashVerified(t *testing.T) {
	bc := newTestChain(t)
	mine(t, bc, 3, newKey(t).Address)
	genesis, err := bc.BlockByHash(bc.main[0])
	if err != nil {
		t.Fatal(err)
	}
	alt := blockOn(t, bc, genesis, newKey(t).Address) // yan dal (daha az iş)
	mustAccept(t, bc, alt)
	if bc.TipHash() == alt.Hash {
		t.Fatal("yan dal blok uç oldu")
	}
	for _, h := range []string{bc.main[1], bc.main[3], alt.Hash} {
		if b, err := bc.BlockByHash(h); err != nil || b.Hash != h {
			t.Fatalf("sağlam blok %s sunulmadı: %v", h, err)
		}
	}

	corrupt := func(key string, mutate func(*Block)) {
		t.Helper()
		raw, err := bc.db.Get([]byte(key), nil)
		if err != nil {
			t.Fatal(err)
		}
		var b Block
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		mutate(&b)
		data, err := json.Marshal(&b)
		if err != nil {
			t.Fatal(err)
		}
		if err := bc.db.Put([]byte(key), data, nil); err != nil {
			t.Fatal(err)
		}
	}
	corrupt(bc.main[2], func(b *Block) { b.Transactions[0].Amount++ })
	corrupt(alt.Hash, func(b *Block) { b.Transactions[0].Amount++ })
	raw1, err := bc.db.Get([]byte(bc.main[1]), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc.db.Put([]byte(bc.main[3]), raw1, nil); err != nil { // 3. bloğun anahtarında 1. blok
		t.Fatal(err)
	}
	for _, h := range []string{bc.main[2], alt.Hash, bc.main[3]} {
		if b, err := bc.BlockByHash(h); !errors.Is(err, ErrCorruptState) || b != nil {
			t.Fatalf("bozuk gövde %s: %v", h, err)
		}
	}
	if b, err := bc.BlockByHash(bc.main[1]); err != nil || b.Hash != bc.main[1] {
		t.Fatalf("sağlam blok bozulmadan sonra sunulmadı: %v", err)
	}
}
