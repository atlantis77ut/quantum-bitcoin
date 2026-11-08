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
	// sayısıdır (~25 MB, 60 sn'de ~69 gün). Uzlaşma kuralı değildir, ama açık bir sınırlamadır:
	// ilk MaxSyncHeaders başlığında ucun işini geçmeyen daha derin bir dal benimsenemez
	// (CLAUDE.md'de açık madde).
	MaxSyncHeaders = 100_000
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

// expectedBits ebeveyni parent olan bloğun taşıması gereken hedeftir. Şimdilik sabit;
// LWMA bu fonksiyonun yerine geçecek.
func (bc *Blockchain) expectedBits(parentHash string) uint32 { return bc.bits }

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

// BlockByHash indeksteki bir bloğun gövdesini döndürür. Özet indekste değilse veritabanı
// okunmaz (ağdan gelen özetle rastgele anahtar okunamasın).
func (bc *Blockchain) BlockByHash(hash string) (*Block, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if _, ok := bc.index[hash]; !ok {
		return nil, ErrUnknownBlock
	}
	return bc.getBlock(hash)
}

// Confirmations ana zincirdeki bir bloğun onay sayısını (uç = 1) döndürür; blok ana zincirde
// değilse 0. İkinci değer derin reorg alarmıdır: true ise onaylara güvenilmemelidir.
func (bc *Blockchain) Confirmations(hash string) (int64, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	e, ok := bc.index[hash]
	if !ok || !bc.isMain(e) {
		return 0, bc.deepReorg
	}
	return bc.tip.Height - e.Height + 1, bc.deepReorg
}

// mtpLocked ebeveyni parentHash olan bloğun medyan zaman sınırını hesaplar; atalar önce
// lookup'ta (henüz indekste olmayan başlıklar), sonra indekste aranır. medianTimePast ile
// aynı pencereyi kullanır (genesis dahil, en fazla MedianTimeSpan blok).
func (bc *Blockchain) mtpLocked(parentHash string, lookup func(string) (int64, int64, string, bool)) (int64, error) {
	get := func(h string) (ts, height int64, prev string, ok bool) {
		if lookup != nil {
			if ts, height, prev, ok = lookup(h); ok {
				return
			}
		}
		if e, found := bc.index[h]; found {
			return e.Timestamp, e.Height, e.Prev, true
		}
		return 0, 0, "", false
	}
	ts, height, prev, ok := get(parentHash)
	if !ok {
		return 0, fmt.Errorf("%w: medyan zaman için ata yok", ErrCorruptState)
	}
	times := []int64{ts}
	for len(times) < MedianTimeSpan && height > 0 {
		if ts, height, prev, ok = get(prev); !ok {
			return 0, fmt.Errorf("%w: medyan zaman için ata yok", ErrCorruptState)
		}
		times = append(times, ts)
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
}

type syncHeader struct {
	h    BlockHeader
	work *big.Int
}

func (bc *Blockchain) NewHeaderSync() *HeaderSync {
	return &HeaderSync{bc: bc, headers: map[string]*syncHeader{}, limit: MaxSyncHeaders}
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
	lookup := func(h string) (int64, int64, string, bool) {
		if x, ok := s.headers[h]; ok {
			return x.h.Timestamp, x.h.Index, x.h.PrevHash, true
		}
		return 0, 0, "", false
	}
	for i := range hs {
		h := &hs[i]
		if i > 0 && h.PrevHash != hs[i-1].Hash {
			return i, fmt.Errorf("%w: kesintili dizi", ErrBadHeaders)
		}
		var parentHeight int64
		var parentWork *big.Int
		if p, ok := s.headers[h.PrevHash]; ok {
			parentHeight, parentWork = p.h.Index, p.work
		} else if e, ok := bc.index[h.PrevHash]; ok {
			if e.Status == statusInvalid {
				return i, ErrKnownInvalid
			}
			parentHeight, parentWork = e.Height, e.work
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
		s.headers[h.Hash] = &syncHeader{h: *h, work: total}
		s.order = append(s.order, h.Hash)
		s.last, s.best = h.Hash, total
	}
	return len(hs), nil
}

// checkHeaderOnly bir başlığı gövdesiz doğrular. Okuma kilidi altında çağrılır.
func (bc *Blockchain) checkHeaderOnly(h *BlockHeader, parentHeight int64,
	lookup func(string) (int64, int64, string, bool)) error {
	if !IsHashHex(h.Hash) || !IsHashHex(h.PrevHash) || !IsHashHex(h.MerkleRoot) {
		return ErrBadHash
	}
	if h.Version != BlockVersion {
		return ErrBadVersion
	}
	if h.Bits != bc.expectedBits(h.PrevHash) {
		return ErrBadDifficulty
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

// MoreWork oturumun başlık zinciri ucun işini geçiyorsa true döner.
func (s *HeaderSync) MoreWork() bool {
	s.bc.mu.RLock()
	defer s.bc.mu.RUnlock()
	return s.best != nil && s.best.Cmp(s.bc.tip.work) > 0
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
	if s.best == nil || s.best.Cmp(bc.tip.work) <= 0 {
		return nil
	}
	var out []string
	for _, h := range s.order {
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
