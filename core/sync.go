package core

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// Başlık öncelikli senkronizasyon (P2P PR'ı). Eşten önce başlıklar alınır ve gövdesiz
// doğrulanır (bağlantı, yükseklik, sürüm, hedef, PoW, özet, medyan zaman, FTL). Başlık
// zincirinin birikmiş işi ucunkini geçerse gövdeler indirilir; bu bloklar için yan zincir
// depolama sınırları (MaxSideForkDepth, MaxBlocksPerHeight) uygulanmaz. Böylece reorg
// derinliğine uzlaşma sınırı konmaz: daha çok iş taşıyan zincir ne kadar derinden
// çatallanırsa çatallansın benimsenebilir.
const (
	// MaxHeadersPerMsg bir başlık yanıtındaki en fazla başlık sayısıdır.
	MaxHeadersPerMsg = 2000

	// MaxLocatorHashes bir konum belirleyicideki en fazla özet sayısıdır.
	MaxLocatorHashes = 64

	// MaxSyncHeaders bir senkronizasyon oturumunun bellekte tuttuğu en fazla yeni başlık
	// sayısıdır (~5 MB, 60 sn'de ~14 gün). Bu pencerede ucun işini (ve en az zincir işini)
	// geçmeyen bir zincir ön-senkronizasyonla (presync.go) sabit bellekte sayılır ve eşiği
	// geçerse bu boyutta parçalarla yeniden indirilir; derinlik sınırı yoktur.
	MaxSyncHeaders = 20_000
)

var (
	ErrUnknownBlock = errors.New("blok bilinmiyor")
	ErrSyncLimit    = errors.New("senkronizasyon başlık sınırı aşıldı")
	ErrBadHeaders   = errors.New("başlık dizisi geçersiz")
)

// IsHashHex s'nin kanonik bir blok özeti (64 küçük harf hex) olup olmadığını söyler. Ağdan
// gelen her özet veritabanına ya da indekse ulaşmadan önce bununla denetlenmelidir: aksi halde
// "lh", "schema" ya da "bal_<adres>" gibi anahtarlar blok diye okunabilirdi.
func IsHashHex(s string) bool {
	_, ok := decodeHash(s)
	return ok
}

// BlockHeader bir bloğun gövdesiz başlığıdır; Hash başlığın SHA256d özetidir.
type BlockHeader struct {
	Version    uint32 `json:"version"`
	Index      int64  `json:"index"`
	Timestamp  int64  `json:"timestamp"`
	PrevHash   string `json:"prev_hash"`
	MerkleRoot string `json:"merkle_root"`
	Bits       uint32 `json:"bits"`
	Nonce      uint64 `json:"nonce"`
	Hash       string `json:"hash"`
}

func (h *BlockHeader) asBlock() *Block {
	return &Block{Version: h.Version, Index: h.Index, Timestamp: h.Timestamp, PrevHash: h.PrevHash,
		MerkleRoot: h.MerkleRoot, Bits: h.Bits, Nonce: h.Nonce, Hash: h.Hash}
}

func (e *indexEntry) header() BlockHeader {
	return BlockHeader{Version: e.Version, Index: e.Height, Timestamp: e.Timestamp, PrevHash: e.Prev,
		MerkleRoot: e.MerkleRoot, Bits: e.Bits, Nonce: e.Nonce, Hash: e.Hash}
}

// ancestor zorluk ve medyan zaman hesabı için bir atanın özetidir.
type ancestor struct {
	ts, height int64
	prev       string
	anchorTime int64 // atanın zincirindeki 1. bloğun zamanı (ASERT çapası; genesis'te 0)
}

// lookupFn henüz indekste olmayan atalar (senkronizasyon oturumu başlıkları) için aramadır.
type lookupFn func(hash string) (ancestor, bool)

// ancestorLocked bir atayı önce lookup'ta, sonra indekste arar. Kilit altında çağrılır.
func (bc *Blockchain) ancestorLocked(h string, lookup lookupFn) (ancestor, bool) {
	if lookup != nil {
		if a, ok := lookup(h); ok {
			return a, true
		}
	}
	if e, ok := bc.index[h]; ok {
		return ancestor{ts: e.Timestamp, height: e.Height, prev: e.Prev, anchorTime: e.AnchorTime}, true
	}
	return ancestor{}, false
}

// childAnchorTime ebeveyninin çapa zamanı parentAnchor olan, childHeight yüksekliğinde ve
// childTime zamanlı bloğun çapa zamanıdır: 1. blok kendi zamanıyla çapadır.
func childAnchorTime(childHeight, childTime, parentAnchor int64) int64 {
	if childHeight == 1 {
		return childTime
	}
	return parentAnchor
}

// expectedBitsLocked ebeveyni parentHash olan bloğun taşıması gereken hedeftir (ASERT,
// bkz. asert.go). Yalnızca ebeveyne bağlıdır; ata indekste ya da lookup'ta olmalıdır.
func (bc *Blockchain) expectedBitsLocked(parentHash string, lookup lookupFn) (uint32, error) {
	a, ok := bc.ancestorLocked(parentHash, lookup)
	if !ok {
		return 0, fmt.Errorf("%w: zorluk için ata yok", ErrCorruptState)
	}
	return bc.params.nextBits(a.height, a.ts, a.anchorTime), nil
}

// Locator ana zincirden bir konum belirleyici üretir: uçtan geriye ilk 10 blok tek tek,
// sonra adım ikiye katlanarak; genesis her zaman sondadır.
func (bc *Blockchain) Locator() []string {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	var loc []string
	step := int64(1)
	for h := bc.tip.Height; h > 0 && len(loc) < MaxLocatorHashes-1; h -= step {
		loc = append(loc, bc.main[h])
		if len(loc) >= 10 {
			step *= 2
		}
	}
	return append(loc, bc.main[0])
}

// HeadersAfter, konum belirleyicideki ana zincirde bulunan ilk özetten sonraki en fazla max
// ana zincir başlığını döndürür; hiçbiri bulunamazsa genesis'ten başlar. Başlıklar indeksten
// okunur, disk okuması yapılmaz.
func (bc *Blockchain) HeadersAfter(locator []string, max int) []BlockHeader {
	if max > MaxHeadersPerMsg {
		max = MaxHeadersPerMsg
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	start := int64(0)
	for i, h := range locator {
		if i >= MaxLocatorHashes {
			break
		}
		if e, ok := bc.index[h]; ok && bc.isMain(e) {
			start = e.Height
			break
		}
	}
	var out []BlockHeader
	for h := start + 1; h <= bc.tip.Height && len(out) < max; h++ {
		out = append(out, bc.index[bc.main[h]].header())
	}
	return out
}

// HeaderByHash ana zincirdeki bir bloğun başlığını indeksten (disk okuması olmadan)
// döndürür; ikinci değer, blok ana zincirde değilse (bilinmeyen, yan dal, yalnızca başlığı
// bilinen ya da geçersiz) false'tur.
func (bc *Blockchain) HeaderByHash(hash string) (BlockHeader, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	e, ok := bc.index[hash]
	if !ok || !bc.isMain(e) {
		return BlockHeader{}, false
	}
	return e.header(), true
}

// TipHeader ana zincirin ucundaki başlığı döndürür.
func (bc *Blockchain) TipHeader() BlockHeader {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.tip.header()
}

// HeadersRange from yüksekliğinden başlayarak en çok count (en çok MaxHeadersPerMsg) ana
// zincir başlığını artan sırayla ve aynı anlık görüntüdeki ucun yüksekliği ile özetini
// döndürür. from uçtan büyükse ya da count <= 0 ise liste boştur.
func (bc *Blockchain) HeadersRange(from int64, count int) ([]BlockHeader, int64, string) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	tipHeight := bc.tip.Height
	if from < 0 || from > tipHeight || count <= 0 {
		return nil, tipHeight, bc.tip.Hash
	}
	n := int64(min(count, MaxHeadersPerMsg))
	if n > tipHeight-from+1 { // from+n taşmasın diye çıkarma ile karşılaştırılır
		n = tipHeight - from + 1
	}
	out := make([]BlockHeader, 0, n)
	for h := from; h < from+n; h++ {
		out = append(out, bc.index[bc.main[h]].header())
	}
	return out, tipHeight, bc.tip.Hash
}

// BlockByHash indeksteki bir bloğun gövdesini döndürür. Özet indekste değilse veritabanı
// okunmaz (ağdan gelen özetle rastgele anahtar okunamasın).
func (bc *Blockchain) BlockByHash(hash string) (*Block, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if e, ok := bc.index[hash]; !ok || e.Status == statusInvalid {
		return nil, ErrUnknownBlock // geçersiz işaretli bloklar eşlere sunulmaz
	}
	return bc.getBlock(hash)
}

// MainBlockByHash ana zincirdeki bir bloğu gövdesiyle ve aynı anlık görüntüdeki onay
// sayısıyla (uç = 1) döndürür. Blok ana zincirde değilse (bilinmeyen, yan dal, geçersiz)
// ErrUnknownBlock döner ve veritabanı okunmaz.
func (bc *Blockchain) MainBlockByHash(hash string) (*Block, int64, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	e, ok := bc.index[hash]
	if !ok || !bc.isMain(e) {
		return nil, 0, ErrUnknownBlock
	}
	return bc.mainBlockLocked(e)
}

// MainBlockByHeight ana zincirde height yüksekliğindeki bloğu MainBlockByHash gibi döndürür;
// yükseklik [0, uç] dışındaysa ErrUnknownBlock.
func (bc *Blockchain) MainBlockByHeight(height int64) (*Block, int64, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if height < 0 || height > bc.tip.Height {
		return nil, 0, ErrUnknownBlock
	}
	e, ok := bc.index[bc.main[height]]
	if !ok {
		return nil, 0, ErrUnknownBlock
	}
	return bc.mainBlockLocked(e)
}

func (bc *Blockchain) mainBlockLocked(e *indexEntry) (*Block, int64, error) {
	b, err := bc.getBlock(e.Hash)
	if err != nil {
		return nil, 0, err
	}
	return b, bc.tip.Height - e.Height + 1, nil
}

// Confirmations ana zincirdeki bir bloğun onay sayısını (uç = 1) döndürür; blok ana zincirde
// değilse 0. İkinci değer derin reorg alarmıdır: true ise onaylara güvenilmemelidir.
func (bc *Blockchain) Confirmations(hash string) (int64, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	e, ok := bc.index[hash]
	if !ok || !bc.isMain(e) {
		return 0, bc.deepReorg != nil
	}
	return bc.tip.Height - e.Height + 1, bc.deepReorg != nil
}

// mtpLocked ebeveyni parentHash olan bloğun medyan zaman sınırını hesaplar; atalar önce
// lookup'ta (henüz indekste olmayan başlıklar), sonra indekste aranır. medianTimePast ile
// aynı pencereyi kullanır (genesis dahil, en fazla MedianTimeSpan blok).
func (bc *Blockchain) mtpLocked(parentHash string, lookup lookupFn) (int64, error) {
	a, ok := bc.ancestorLocked(parentHash, lookup)
	if !ok {
		return 0, fmt.Errorf("%w: medyan zaman için ata yok", ErrCorruptState)
	}
	times := []int64{a.ts}
	for len(times) < MedianTimeSpan && a.height > 0 {
		if a, ok = bc.ancestorLocked(a.prev, lookup); !ok {
			return 0, fmt.Errorf("%w: medyan zaman için ata yok", ErrCorruptState)
		}
		times = append(times, a.ts)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], nil
}

// HeaderSync tek bir eşle yürütülen bir senkronizasyon oturumunun başlık zinciridir. Başlıklar
// art arda gelen yanıtlarla eklenir ve tek bir kesintisiz zincir oluşturmalıdır.
type HeaderSync struct {
	bc      *Blockchain
	headers map[string]*syncHeader
	order   []string // indekste olmayan başlıklar, zincir sırasıyla
	last    string   // son eklenen başlığın özeti
	best    *big.Int // son başlığın birikmiş işi
	limit   int      // en fazla yeni başlık (MaxSyncHeaders; testler küçültür)

	// Ön-senkronizasyonla yetkilendirilmiş yeniden indirme (presync.go): yalnızca order[:authorized]
	// gövdeleri indirilebilir; authorized eşleşen son kontrol noktasına kadar ilerler.
	proof          *Presync
	authorized     int
	authorizedHash string // yetkilendirilen son başlık (bir sonraki parçanın başlangıcı)
	reachedFinal   bool
}

type syncHeader struct {
	h          BlockHeader
	work       *big.Int
	anchorTime int64
}

func (bc *Blockchain) NewHeaderSync() *HeaderSync {
	return &HeaderSync{bc: bc, headers: map[string]*syncHeader{}, limit: MaxSyncHeaders}
}

// SetLimit oturum penceresini değiştirir (ağ katmanı ve testler için; en az 1).
func (s *HeaderSync) SetLimit(n int) {
	if n > 0 {
		s.limit = n
	}
}

// Add bir başlık yanıtını doğrulayıp oturuma ekler ve eklenen başlık sayısını döndürür.
// Gelecek tarihli (FTL'i aşan) başlıkta durur ve ErrTimestampFuture döner: bu geçicidir,
// başlık geçersiz sayılmaz; önceki başlıklar oturumda kalır.
func (s *HeaderSync) Add(hs []BlockHeader) (int, error) {
	if len(hs) == 0 || len(hs) > MaxHeadersPerMsg {
		return 0, fmt.Errorf("%w: %d başlık", ErrBadHeaders, len(hs))
	}
	bc := s.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if s.last != "" && hs[0].PrevHash != s.last {
		return 0, fmt.Errorf("%w: önceki yanıtın devamı değil", ErrBadHeaders)
	}
	lookup := func(h string) (ancestor, bool) {
		if x, ok := s.headers[h]; ok {
			return ancestor{ts: x.h.Timestamp, height: x.h.Index, prev: x.h.PrevHash, anchorTime: x.anchorTime}, true
		}
		return ancestor{}, false
	}
	for i := range hs {
		h := &hs[i]
		if i > 0 && h.PrevHash != hs[i-1].Hash {
			return i, fmt.Errorf("%w: kesintili dizi", ErrBadHeaders)
		}
		matched, err := s.checkProof(h)
		if err != nil {
			return i, err
		}
		var parentHeight, parentAnchor int64
		var parentWork *big.Int
		if p, ok := s.headers[h.PrevHash]; ok {
			parentHeight, parentWork, parentAnchor = p.h.Index, p.work, p.anchorTime
		} else if e, ok := bc.index[h.PrevHash]; ok {
			if e.Status == statusInvalid {
				return i, ErrKnownInvalid
			}
			parentHeight, parentWork, parentAnchor = e.Height, e.work, e.AnchorTime
		} else {
			return i, ErrBadPrevHash
		}
		if e, ok := bc.index[h.Hash]; ok {
			if e.Status == statusInvalid {
				return i, ErrKnownInvalid
			}
			// Bilinen blok: özet başlığa bağlı olduğundan başlığın indeks kaydıyla tutarlı
			// olması yeter. Ebeveyn ve yükseklik eşleşmeli; böylece bilinen başlıklarla kurulan
			// dizi indekste kesin artan yükseklikte ilerler ve döngü kurulamaz.
			if e.Prev != h.PrevHash || e.Height != h.Index || e.Height != parentHeight+1 {
				return i, fmt.Errorf("%w: bilinen blokla tutarsız başlık", ErrBadHeaders)
			}
			s.last, s.best = h.Hash, e.work
			s.authorize(matched, h)
			continue
		}
		if _, ok := s.headers[h.Hash]; ok {
			return i, fmt.Errorf("%w: yinelenen başlık", ErrBadHeaders)
		}
		if len(s.headers) >= s.limit {
			return i, ErrSyncLimit
		}
		if err := bc.checkHeaderOnly(h, parentHeight, lookup); err != nil {
			return i, err
		}
		w, err := blockWork(h.Bits)
		if err != nil {
			return i, err
		}
		total := new(big.Int).Add(parentWork, w)
		s.headers[h.Hash] = &syncHeader{h: *h, work: total, anchorTime: childAnchorTime(h.Index, h.Timestamp, parentAnchor)}
		s.order = append(s.order, h.Hash)
		s.last, s.best = h.Hash, total
		s.authorize(matched, h)
	}
	return len(hs), nil
}

// authorize eşleşen bir kontrol noktasında oturumdaki tüm öneki yetkilendirir.
func (s *HeaderSync) authorize(matched bool, h *BlockHeader) {
	if !matched {
		return
	}
	s.authorized = len(s.order)
	s.authorizedHash = h.Hash
	if h.Index == s.proof.finalHeight {
		s.reachedFinal = true
	}
}

// checkHeaderOnly bir başlığı gövdesiz doğrular. Okuma kilidi altında çağrılır.
func (bc *Blockchain) checkHeaderOnly(h *BlockHeader, parentHeight int64, lookup lookupFn) error {
	if !IsHashHex(h.Hash) || !IsHashHex(h.PrevHash) || !IsHashHex(h.MerkleRoot) {
		return ErrBadHash
	}
	if h.Version != BlockVersion {
		return ErrBadVersion
	}
	want, err := bc.expectedBitsLocked(h.PrevHash, lookup)
	if err != nil {
		return err
	}
	if h.Bits != want {
		return ErrBadDifficulty // PoW'dan önce: kolay hedef ilan eden başlık özet hesaplatamaz
	}
	if !MeetsTarget(h.Hash, h.Bits) {
		return ErrInsufficientPoW
	}
	if h.Hash != h.asBlock().CalculateHash() {
		return ErrBadHash
	}
	if h.Index != parentHeight+1 {
		return ErrBadIndex
	}
	mtp, err := bc.mtpLocked(h.PrevHash, lookup)
	if err != nil {
		return err
	}
	if h.Timestamp <= mtp {
		return ErrTimestampOld
	}
	if h.Timestamp > bc.now()+MaxFutureDrift {
		return ErrTimestampFuture
	}
	return nil
}

// Full oturum başlık sınırına ulaştıysa true döner: gövdeler indirilmeli, kalan başlıklar
// sonraki oturumda (indirilen blokların üzerinden) gelir.
func (s *HeaderSync) Full() bool { return len(s.headers) >= s.limit }

// MoreWork oturumun başlık zinciri ucun işini geçiyor ve en az zincir işine ulaşıyorsa
// true döner (bkz. minwork.go).
func (s *HeaderSync) MoreWork() bool {
	s.bc.mu.RLock()
	defer s.bc.mu.RUnlock()
	if s.proof != nil {
		return s.bc.enoughWorkLocked(s.proof.finalWork)
	}
	return s.bc.enoughWorkLocked(s.best)
}

// Last son eklenen başlığın özetidir (bir sonraki başlık isteğinin konum belirleyicisi için).
func (s *HeaderSync) Last() string { return s.last }

// Len oturumdaki yeni başlık sayısıdır.
func (s *HeaderSync) Len() int { return len(s.headers) }

// Missing indekste henüz olmayan başlıkların özetlerini zincir sırasıyla döndürür ve bu
// bloklara yan zincir depolama sınırlarının uygulanmamasını sağlar. Yalnızca MoreWork true
// iken çağrılmalıdır: izin, doğrulanmış PoW'u ucunkini aşan bir başlık zincirine dayanır.
func (s *HeaderSync) Missing() []string {
	bc := s.bc
	bc.mu.Lock()
	defer bc.mu.Unlock()
	order := s.order
	if s.proof != nil {
		// Yetki ön-senkronizasyonun toplam işine dayanır; uç bu arada ilerlediyse yeniden denetlenir.
		if !bc.enoughWorkLocked(s.proof.finalWork) {
			return nil
		}
		order = s.order[:s.authorized]
	} else if !bc.enoughWorkLocked(s.best) {
		return nil
	}
	var out []string
	for _, h := range order {
		if _, ok := bc.index[h]; !ok {
			out = append(out, h)
		}
	}
	if len(bc.syncAllow)+len(out) > MaxSyncHeaders {
		bc.syncAllow = map[string]struct{}{} // eski oturumların izinleri düşer
	}
	for _, h := range out {
		bc.syncAllow[h] = struct{}{}
	}
	return out
}
