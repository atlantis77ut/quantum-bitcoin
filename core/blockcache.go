package core

import (
	"container/list"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"
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
//
// Kilitsiz çözme (feat/decode-outside-lock): okumanın kilit altındaki yarısı yalnızca indeksi,
// önbelleği ve kapıyı kullanır (startBlockReadLocked); kaçırmada disk okuması ve çözme zincir
// kilidi bırakıldıktan sonra yapılır (finishBlockRead), böylece bekleyen blok kabulü bir API
// okumasının çözmesini beklemez.

const (
	blockCacheMaxEntries = 8
	blockCacheMaxBytes   = 48 << 20 // girdi boyutlarının toplamı (blockMemSize)
)

// Bellek muhasebesi (feat/cache-accounting): girdi boyutu diskteki JSON boyutu ile çözülmüş
// bloğun bellek tahmininin büyüğüdür (blockMemSize). İmzalı işlemde çözülmüş boyut JSON'dan
// küçüktür (açık anahtar ve imza JSON'da base64, ~1,33 kat), imzasız coinbase'de birkaç bayt
// büyük olabilir; tahmin ayırıcı ek yükünü saymaz. Geçerli bloklarda bayt sınırına ulaşılmaz:
// uzlaşma kuralları (MaxTxPerBlock, adres biçimi, açık anahtar ve imza boyutu, sabit commitment)
// dolu bloğu ~1,8 MB JSON ile sınırlar, 8 × 1,8 MB < 48 MiB. Belleği girdi sayısı sınırlar,
// bayt sınırı bozuk ya da beklenmedik büyük kayda karşı ikinci sınırdır.

// blockMemSize çözülmüş bloğun ve işlem kimliklerinin yaklaşık bellek boyutudur: yapılar,
// dizgi ve bayt dilimi içerikleri (kapasite), kimlik başına 32 bayt.
func blockMemSize(b *Block, ids [][32]byte) int {
	n := int(unsafe.Sizeof(*b)) + len(b.PrevHash) + len(b.MerkleRoot) + len(b.Hash) + len(ids)*32
	for i := range b.Transactions {
		tx := &b.Transactions[i]
		n += int(unsafe.Sizeof(*tx)) + len(tx.Sender) + len(tx.Receiver) +
			cap(tx.PublicKey) + cap(tx.Signature) + cap(tx.Commitment)
	}
	return n
}

type blockCacheEntry struct {
	hash  string
	block *Block
	size  int

	// ids bloğun işlem kimlikleridir: diskten yüklenen blokta doğrulama sırasında hesaplanıp
	// put'ta atanır, yoksa ilk BlockTxIDs çağrısında bir kez hesaplanır.
	idsOnce sync.Once
	ids     [][32]byte
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

// peek girdiyi isabet/ıska sayacına ve LRU sırasına dokunmadan döndürür; yoksa nil.
func (c *blockCache) peek(hash string) *blockCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok {
		return el.Value.(*blockCacheEntry)
	}
	return nil
}

// put bloğu ekler; tek başına bütçeyi aşan blok saklanmaz. Sınırlar aşılırsa en eski çıkar.
// ids nil değilse bloğun işlem kimlikleridir ve girdiye atanır.
func (c *blockCache) put(hash string, b *Block, size int, ids [][32]byte) {
	if size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[hash]; ok { // eşzamanlı iki ıska aynı bloğu okumuş olabilir
		c.order.MoveToFront(el)
		return
	}
	e := &blockCacheEntry{hash: hash, block: b, size: size}
	if ids != nil {
		e.idsOnce.Do(func() { e.ids = ids })
	}
	c.items[hash] = c.order.PushFront(e)
	c.bytes += size
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		el := c.order.Back()
		e := el.Value.(*blockCacheEntry)
		c.order.Remove(el)
		delete(c.items, e.hash)
		c.bytes -= e.size
	}
}

// BlockTxIDs b'nin işlem kimliklerini (IDBytes, blok sırasıyla) döndürür. b, API okumasının
// döndürdüğü ve hâlâ önbellekte duran girdinin kendisiyse girdideki kimlikler döner (diskten
// yüklenen blokta doğrulamada hesaplanmıştır; yoksa ilk çağrıda bir kez hesaplanıp saklanır);
// değilse (önbellekte yok, eşzamanlı kaçırmada yarışı kaybetmiş ya da önbellek dışı yoldan gelmiş
// blok) her çağrıda hesaplanır ve saklanmaz. Hesaplama zincir ve önbellek kilidi tutulmadan
// yapılır; girdi bu sırada önbellekten düşse de çağıranın elindeki kimlikler geçerlidir. Dönen
// dilim paylaşılır ve salt okunurdur. Diskten yüklenen bloğun kimlik dilimi (blok başına
// ~8 KiB) girdi boyutuna sayılır (blockMemSize).
func (bc *Blockchain) BlockTxIDs(b *Block) [][32]byte {
	if e := bc.blockCache.peek(b.Hash); e != nil && e.block == b {
		e.idsOnce.Do(func() { e.ids = TxIDs(b.Transactions) })
		return e.ids
	}
	return TxIDs(b.Transactions)
}

// ReadGate önbellek kaçırmasında disk okumasından önce, bc.mu okuma kilidi altında çağrılır; tip,
// okunan blok uç ise doğrudur (yeni uç önbelleğe ilk API okumasında girer; uç önbellekten düşmüş
// de olabilir). Hata dönerse okuma o hatayla biter. Dönen release okuma bitince (hata yolları
// dahil) bir kez, zincir kilidi tutulmadan çağrılır. nil kapı her okumaya izin verir.
type ReadGate func(tip bool) (release func(), err error)

// blockRead bir gövde okumasının kilit altındaki yarısının sonucudur: önbellekte bulunan blok ya
// da kilit bırakıldıktan sonra diskten okunacak özet ve kapının release'i.
type blockRead struct {
	hash    string
	block   *Block // önbellekten; nil ise finishBlockRead diskten okur
	release func() // kaçırmada kapınınki; nil kapıda nil
}

// startBlockReadLocked hash'in gövdesini önbellekte arar (çağıran bc.mu'yu en az okuma için
// tutar). Kaçırmada gate'i (nil değilse) sorar; disk okumasını finishBlockRead'e bırakır.
func (bc *Blockchain) startBlockReadLocked(hash string, gate ReadGate) (blockRead, error) {
	if b, ok := bc.blockCache.get(hash); ok {
		return blockRead{hash: hash, block: b}, nil
	}
	r := blockRead{hash: hash}
	if gate != nil {
		release, err := gate(hash == bc.lastHash)
		if err != nil {
			return blockRead{}, err
		}
		r.release = release
	}
	return r, nil
}

// finishBlockRead okumayı bc.mu tutulmadan bitirir: kaçırmada gövdeyi diskten okur, çözer ve
// önbelleğe koyar; kapının release'ini (hata yolları dahil) bir kez çağırır. Diskten okunan
// gövde başlığa yeniden bağlanır (verifyLoadedBlock): tutmazsa ErrCorruptState döner ve önbelleğe
// girmez. Doğrulamada hesaplanan işlem kimlikleri girdiyle saklanır (BlockTxIDs).
//
// Kilitsiz okuma şu değişmeze dayanır: blok gövdesinin anahtarı (çıplak özet) hiç silinmez ve
// üzerine başka içerik yazılmaz (budama yok, saklanmayan yan bloklar hiç yazılmaz; durum
// anahtarları önekli). Kilit altında ana zincirde görülen blok kilitten sonra reorg'la ana
// zincirden düşse de gövdesi aynıdır; yanıt kilidin bırakıldığı anın durumudur. Budama ya da yan
// blok temizliği eklenirse bu okuma yeniden düşünülmelidir. bc.db Close'da yeniden atanmaz;
// kapanırken okuma leveldb.ErrClosed ile biter.
func (bc *Blockchain) finishBlockRead(r blockRead) (*Block, error) {
	if r.block != nil {
		return r.block, nil
	}
	if r.release != nil {
		defer r.release()
	}
	if bc.beforeBlockLoad != nil {
		bc.beforeBlockLoad()
	}
	hash := r.hash
	data, err := bc.db.Get([]byte(hash), nil)
	if err != nil {
		return nil, fmt.Errorf("blok okunamadı (%s): %w", hash, err)
	}
	var b Block
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("%w: blok %s: %v", ErrCorruptState, hash, err)
	}
	ids, err := verifyLoadedBlock(hash, &b)
	if err != nil {
		return nil, err
	}
	bc.blockCache.put(hash, &b, max(len(data), blockMemSize(&b, ids)), ids)
	return &b, nil
}

// verifyLoadedBlock diskten çözülen bloğu anahtarına yeniden bağlar ve işlem kimliklerini
// döndürür. Sırayla: gövdedeki özet alanı anahtar mı, başlığın özeti anahtar mı (kanonik olmayan
// PrevHash/MerkleRoot burada düşer), işlem sayısı sınırda mı (özetlemeden önce), işlemlerin
// Merkle kökü başlıktaki kök mü. Uzlaşma doğrulaması değildir (PoW, zaman, imza ve bakiye
// denetlenmez; blok kabul edilirken denetlendi); diskteki bozulmayı yakalar. Eksik veri alarmını
// tetiklemez (o uzlaşma yolunundur): çağıran bozuk durum hatası alır.
func verifyLoadedBlock(hash string, b *Block) ([][32]byte, error) {
	if b.Hash != hash {
		return nil, fmt.Errorf("%w: %s anahtarında %s bloğu", ErrCorruptState, hash, b.Hash)
	}
	if b.CalculateHash() != hash {
		return nil, fmt.Errorf("%w: %s bloğunun başlığı özetiyle tutmuyor", ErrCorruptState, hash)
	}
	if len(b.Transactions) == 0 || len(b.Transactions)-1 > MaxTxPerBlock {
		return nil, fmt.Errorf("%w: %s bloğunda %d işlem", ErrCorruptState, hash, len(b.Transactions))
	}
	ids := TxIDs(b.Transactions)
	root := merkleNodes(idLeaves(ids))
	if hex.EncodeToString(root[:]) != b.MerkleRoot {
		return nil, fmt.Errorf("%w: %s bloğunun işlemleri Merkle köküyle tutmuyor", ErrCorruptState, hash)
	}
	return ids, nil
}
