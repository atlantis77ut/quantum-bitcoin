package core

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"
)

// Blok gövdesi önbelleği (yalnızca API okumaları: MainBlockByHash, MainBlockByHeight,
// GetLatestBlock, TxByID ve kapılı adaşları, BlockReads). Blok, özetiyle içerik adreslidir: özet
// başlığı, başlık da Merkle kökü üzerinden işlemleri bağlar; bu yüzden bir özetin gövdesi hiç
// değişmez ve reorg'da ya da blok eklenince önbellek geçersiz kılınmaz. Ana zincirde olup olmama ve
// onay sayısı her istekte indeksten hesaplanır, önbellekten gelmez. Uzlaşma yolu (bağlama, reorg,
// geri alma) önbelleği kullanmaz ve her zaman diskten okur.
//
// Döndürülen *Block paylaşılır ve salt okunurdur: çağıran onu ya da dilimlerini değiştirmemelidir.
//
// Önbellek kaçırma kapısı (ReadGate, feat/old-block-reads): önbellekte olmayan bir gövde diskten
// okunup çözülür (dolu blokta ~2 MB JSON, ~13 ms). Farklı eski bloklarla dolaşan istemci önbelleği
// atlatır; istek sayan oran sınırı bu maliyeti görmez. API okumaları (Reads) bir kapı alır: kapı
// yalnızca kaçırmada, disk okumasından önce çağrılır, beklemez (zincir kilidi tutulurken
// beklenirse bekleyen bir yazar bütün kuyruğu beklerdi) ve hata dönerse disk okunmaz. Kapı
// zincire geri çağrı yapmamalıdır (bc.mu okuma kilidi altında çağrılır).

const (
	blockCacheMaxEntries = 8
	blockCacheMaxBytes   = 48 << 20 // kodlanmış (diskteki JSON) boyut toplamı
)

type blockCacheEntry struct {
	hash  string
	block *Block
	size  int
}

type blockCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	bytes      int
	order      *list.List // ön: en son kullanılan
	items      map[string]*list.Element
	hits       uint64
	misses     uint64
}

func newBlockCache(maxEntries, maxBytes int) *blockCache {
	return &blockCache{maxEntries: maxEntries, maxBytes: maxBytes, order: list.New(), items: map[string]*list.Element{}}
}

func (c *blockCache) get(hash string) (*Block, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[hash]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	c.order.MoveToFront(el)
	return el.Value.(*blockCacheEntry).block, true
}

// put bloğu ekler; tek başına bütçeyi aşan blok saklanmaz. Sınırlar aşılırsa en eski çıkar.
func (c *blockCache) put(hash string, b *Block, size int) {
	if size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok { // eşzamanlı iki ıska aynı bloğu okumuş olabilir
		c.order.MoveToFront(el)
		return
	}
	c.items[hash] = c.order.PushFront(&blockCacheEntry{hash: hash, block: b, size: size})
	c.bytes += size
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		el := c.order.Back()
		e := el.Value.(*blockCacheEntry)
		c.order.Remove(el)
		delete(c.items, e.hash)
		c.bytes -= e.size
	}
}

// ReadGate önbellek kaçırmasında disk okumasından önce çağrılır; tip, okunan blok uç ise doğrudur
// (yeni uç önbelleğe ilk API okumasında girer; uç önbellekten düşmüş de olabilir). Hata dönerse
// okuma o hatayla biter; dönen release okuma bitince (hata yolları dahil) çağrılır. nil kapı her
// okumaya izin verir.
type ReadGate func(tip bool) (release func(), err error)

// cachedBlockLocked hash'in gövdesini önbellekten ya da diskten döndürür (çağıran bc.mu'yu en
// az okuma için tutar). Diskten okunan gövdenin özeti hash ile tutmazsa önbelleğe girmez.
// Kaçırmada gate (nil değilse) disk okumasından önce sorulur.
func (bc *Blockchain) cachedBlockLocked(hash string, gate ReadGate) (*Block, error) {
	if b, ok := bc.blockCache.get(hash); ok {
		return b, nil
	}
	if gate != nil {
		release, err := gate(hash == bc.lastHash)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	data, err := bc.db.Get([]byte(hash), nil)
	if err != nil {
		return nil, fmt.Errorf("blok okunamadı (%s): %w", hash, err)
	}
	var b Block
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%w: blok %s: %v", ErrCorruptState, hash, err)
	}
	if b.Hash != hash {
		return nil, fmt.Errorf("%w: %s anahtarında %s bloğu", ErrCorruptState, hash, b.Hash)
	}
	bc.blockCache.put(hash, &b, len(data))
	return &b, nil
}
