package core

import (
	"errors"
	"testing"
)

// İkinci okuma diskten değil önbellekten gelir (aynı paylaşılan blok döner, ıska sayısı
// artmaz); onay sayısı önbellekten değil indeksten gelir.
func TestBlockCacheServesAPIReads(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	b1, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, miner); err != nil {
		t.Fatal(err)
	}
	first, conf, err := bc.MainBlockByHash(b1.Hash)
	if err != nil || first.Hash != b1.Hash || conf != 2 {
		t.Fatalf("ilk okuma %v %d %v", first, conf, err)
	}
	misses := bc.blockCache.misses
	again, conf, err := bc.MainBlockByHeight(b1.Index)
	if err != nil || again != first || conf != 2 || bc.blockCache.misses != misses {
		t.Fatalf("önbellekten okuma %v %d %v (ıska %d → %d)", again, conf, err, misses, bc.blockCache.misses)
	}
	if _, err := bc.AddBlock(nil, miner); err != nil {
		t.Fatal(err)
	}
	if b, conf, err := bc.MainBlockByHash(b1.Hash); err != nil || b != first || conf != 3 {
		t.Fatalf("uç ilerledikten sonra onay %d %v", conf, err)
	}
	latest, err := bc.GetLatestBlock()
	if err != nil || latest.Index != b1.Index+2 {
		t.Fatalf("uç %v %v", latest, err)
	}
	if again, err := bc.GetLatestBlock(); err != nil || again != latest {
		t.Fatalf("uç önbellekten gelmedi: %v", err)
	}
}

// Önbellek giriş sayısıyla ve bayt bütçesiyle sınırlıdır; en az yakın zamanda kullanılan çıkar,
// bütçeden büyük blok saklanmaz.
func TestBlockCacheEviction(t *testing.T) {
	c := newBlockCache(2, 100)
	a, b, d := &Block{Hash: "a"}, &Block{Hash: "b"}, &Block{Hash: "d"}
	c.put("a", a, 40)
	c.put("b", b, 40)
	if got, ok := c.get("a"); !ok || got != a { // a en yeni olur
		t.Fatal("a bulunamadı")
	}
	c.put("d", d, 40) // sayı sınırı: b çıkar
	if _, ok := c.get("b"); ok {
		t.Fatal("en eski giriş çıkmadı")
	}
	if _, ok := c.get("a"); !ok {
		t.Fatal("yakın zamanda kullanılan giriş çıktı")
	}
	c.put("e", &Block{Hash: "e"}, 90) // bayt sınırı: d ve a çıkar
	if c.order.Len() != 1 || c.bytes != 90 {
		t.Fatalf("bayt sınırı: %d giriş, %d bayt", c.order.Len(), c.bytes)
	}
	c.put("f", &Block{Hash: "f"}, 101) // bütçeden büyük: saklanmaz
	if _, ok := c.get("f"); ok || c.bytes != 90 {
		t.Fatal("bütçeden büyük blok saklandı")
	}
	c.put("e", &Block{Hash: "e"}, 90) // yineleme sayılmaz
	if c.order.Len() != 1 || c.bytes != 90 {
		t.Fatalf("yineleme: %d giriş, %d bayt", c.order.Len(), c.bytes)
	}
}

// Anahtarı ile özeti tutmayan kayıt bozuk durum olarak reddedilir ve önbelleğe girmez.
func TestBlockCacheRejectsMismatchedHash(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	b1, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bc.db.Get([]byte(b2.Hash), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc.db.Put([]byte(b1.Hash), raw, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bc.MainBlockByHash(b1.Hash); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("yanlış özetli kayıt: %v", err)
	}
	if _, ok := bc.blockCache.get(b1.Hash); ok {
		t.Fatal("bozuk kayıt önbelleğe girdi")
	}
}
