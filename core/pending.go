package core

import (
	"errors"
	"sync"
)

// Gelecek tarihli bloklar için bekleme kuyruğu. Senkronizasyon olmadığından eşler bir bloğu
// yalnızca bir kez gönderir; saati biraz geride olan dürüst düğüm, FTL'i az farkla aşan bir
// bloğu atarsa sonraki her bloğu ErrBadPrevHash ile reddedip zincirden kalıcı olarak kopardı.
// Kuyruk bu boşluğu senkronizasyon PR'ına kadar kapatan sınırlı bir köprüdür. Ebeveyni
// kuyrukta olan bloklar da beklemeye alınır; böylece saat 2×FTL'e kadar geride kalsa bile
// ardışık bloklar kaybolmaz (en fazla MaxPendingBlocks blok).
const (
	// MaxPendingBlocks kuyrukta tutulan en fazla blok sayısıdır (bellek: en fazla
	// MaxPendingBlocks × azami blok boyutu).
	MaxPendingBlocks = 8

	// pendingWindow: yalnızca yerel saatin en fazla 2×FTL ilerisindeki bloklar beklemeye
	// alınır; daha ilerisi imza doğrulaması yapılmadan, ucuzca reddedilir.
	pendingWindow = 2 * MaxFutureDrift
)

// pendingBlocks gövdesi doğrulanmış (checkBody'den geçmiş), uca doğrudan ya da kuyruktaki
// başka bir blok üzerinden bağlanan bekleyen bloklardır. Dolunca en uzak geleceğe ait blok düşer.
type pendingBlocks struct {
	mu     sync.Mutex
	blocks map[string]*Block

	// dropped tahliye edilen ya da yetim kalıp atılan blokların özetleridir (sınırlı FIFO).
	// Bu bloklar beklemeye yeniden alınmaz; saat yetiştikten sonra normal yoldan gelirlerse
	// her uç bloğu gibi doğrulanırlar.
	dropped rejectCache
}

func (q *pendingBlocks) wasDropped(hash string) bool { return q.dropped.has(hash) }

func (q *pendingBlocks) has(hash string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.blocks[hash]
	return ok
}

func (q *pendingBlocks) remove(hash string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.blocks, hash)
}

func (q *pendingBlocks) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.blocks)
}

// farthestLocked zaman damgası en ileride olan bloğu döndürür (eşitlikte büyük özet).
func (q *pendingBlocks) farthestLocked() *Block {
	var far *Block
	for _, p := range q.blocks {
		if far == nil || p.Timestamp > far.Timestamp ||
			(p.Timestamp == far.Timestamp && p.Hash > far.Hash) {
			far = p
		}
	}
	return far
}

// canAdd, gövde doğrulamasından önce ucuz ön kontroldür: kuyruk doluyken en uzak bloktan
// daha yakın olmayan blok için imza doğrulaması yaptırılmaz. Aksi halde kuyruğa giremeyen
// (ya da tahliye edilip yeniden gönderilen) bir blok her seferinde doğrulatılabilirdi.
func (q *pendingBlocks) canAdd(ts int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.blocks) < MaxPendingBlocks {
		return true
	}
	return ts < q.farthestLocked().Timestamp
}

// add bloğu kuyruğa ekler. Kuyruk doluyken yeni blok en uzak bloktan daha yakın gelecekteyse
// o bloğun yerini alır; değilse eklenmez. Eklendiyse true döner.
func (q *pendingBlocks) add(b *Block) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.blocks == nil {
		q.blocks = make(map[string]*Block)
	}
	if _, ok := q.blocks[b.Hash]; ok {
		return true
	}
	if len(q.blocks) >= MaxPendingBlocks {
		far := q.farthestLocked()
		if b.Timestamp >= far.Timestamp {
			return false
		}
		delete(q.blocks, far.Hash)
		q.dropped.add(far.Hash)
	}
	q.blocks[b.Hash] = b
	return true
}

// take blok indeksine (known) kuyruk üzerinden bağlanamayan blokları atar (ebeveyn tahliye
// edildi ya da hiç gelmedi), ardından ebeveyni indekste olan ve zamanı gelmiş
// (Timestamp ≤ limit) blokları kuyruktan çıkarıp döndürür.
func (q *pendingBlocks) take(known func(hash string) bool, limit int64) []*Block {
	q.mu.Lock()
	defer q.mu.Unlock()
	for h, b := range q.blocks {
		cur := b
		for depth := 0; cur != nil && !known(cur.PrevHash) && depth <= MaxPendingBlocks; depth++ {
			cur = q.blocks[cur.PrevHash]
		}
		if cur == nil || !known(cur.PrevHash) {
			delete(q.blocks, h)
			q.dropped.add(h)
		}
	}
	var ready []*Block
	for h, b := range q.blocks {
		if known(b.PrevHash) && b.Timestamp <= limit {
			ready = append(ready, b)
			delete(q.blocks, h)
		}
	}
	return ready
}

// ProcessPending zamanı gelmiş bekleyen blokları indekse ekler (gerekirse zincire geçer) ve
// kabul edilen blok sayısını döndürür. P2P sunucusu bunu düzenli aralıklarla çağırır. Bir blok
// eklenince onun kuyruktaki çocukları da aynı çağrıda denenir.
func (bc *Blockchain) ProcessPending() int {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	known := func(h string) bool { _, ok := bc.index[h]; return ok }
	added := 0
	for {
		ready := bc.pending.take(known, bc.now()+MaxFutureDrift)
		if len(ready) == 0 {
			return added
		}
		progressed := false
		for _, b := range ready {
			// Gövde kuyruğa alınmadan önce doğrulandı. Ebeveyne göre kontroller (yükseklik,
			// medyan zaman, yan zincir sınırları) ve durum kontrolleri burada yapılır.
			err := ErrTimestampFuture
			if b.Timestamp <= bc.now()+MaxFutureDrift {
				err = bc.storeAndActivateLocked(b)
			}
			if err != nil {
				if isTransientPendingErr(err) {
					bc.pending.add(b) // disk hatası, saat geri gitti vb.: sonraki turda yeniden dene
				}
				bc.log.Warn("bekleyen blok eklenemedi", "index", b.Index, "hash", b.Hash, "neden", err)
				continue
			}
			bc.log.Info("bekleyen blok kabul edildi", "index", b.Index, "hash", b.Hash)
			added++
			progressed = true
		}
		if !progressed {
			return added
		}
	}
}

// isTransientPendingErr, bekleyen bloğun atılmayıp yeniden denenmesi gereken hatalarıdır:
// kalıcı kural ihlalleri, yükseklik/ebeveyn uyuşmazlığı, bilinen ya da geçersiz blok ve
// depolama sınırı dışındaki her şey (ör. saatin geri gitmesi, disk hatası).
func isTransientPendingErr(err error) bool {
	for _, e := range []error{ErrBadIndex, ErrBadPrevHash, ErrKnownBlock, ErrKnownInvalid, ErrSideChainLimit} {
		if errors.Is(err, e) {
			return false
		}
	}
	return !isPermanentStatefulErr(err)
}
