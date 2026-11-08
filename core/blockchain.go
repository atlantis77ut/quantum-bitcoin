package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"quantum-bitcoin/crypto"
)

const (
	keyLastHash   = "lh"
	keySchema     = "schema"
	keyGenesis    = "genesis"
	prefixBalance = "bal_"
	prefixNonce   = "nonce_"

	// schemaVersion disk biçimi veya uzlaşma kodlaması uyumsuz değiştiğinde artırılır.
	// 2: taban birim (10^8), işlem ücreti, sabit başlık + Merkle kökü + compact hedef.
	// 3: ağ parametreleri, deterministik genesis, ağa bağlı imza alanı.
	// 4: blok indeksi (birikmiş iş), ana zincir yükseklik tablosu, blok başına undo kayıtları.
	// 5: ertelenmiş coinbase ödemesi (olgunluk), indekste coinbase alıcısı ve tutarı.
	// 6: indekste tam başlık (sürüm, Merkle kökü, nonce): başlıklar diskten okunmadan sunulur.
	// 7: işlem son kullanma yüksekliği (zaman damgası yerine), imza alanı v4, yeni genesis.
	// 8: ASERT zorluk ayarlaması; indekste çapa zamanı.
	schemaVersion = "8"
)

var (
	// ErrSchemaMismatch veri dizini bu sürümle uyumsuz bir biçimde yazılmış.
	ErrSchemaMismatch = errors.New("veri dizini şema sürümü uyumsuz; dizini silip zinciri yeniden başlatın")

	// ErrGenesisMismatch veri dizini başka bir ağın (başka bir genesis'in) zincirini içeriyor.
	ErrGenesisMismatch = errors.New("veri dizini başka bir ağa ait (genesis uyuşmuyor)")

	// ErrBadGenesis kodda üretilen genesis, ağ parametrelerindeki sabitle tutarsız.
	ErrBadGenesis = errors.New("genesis bloğu ağ parametreleriyle tutarsız")
)

var (
	ErrBadIndex        = errors.New("blok indeksi beklenen değil")
	ErrBadPrevHash     = errors.New("önceki blok özeti eşleşmiyor")
	ErrBadHash         = errors.New("blok özeti içerikle eşleşmiyor")
	ErrBadDifficulty   = errors.New("blok zorluğu beklenen hedefle (ASERT) eşleşmiyor")
	ErrOrphanTooEasy   = errors.New("ebeveyni bilinmeyen blok ucun hedefinden çok kolay")
	ErrInsufficientPoW = errors.New("iş ispatı (PoW) hedefi karşılanmıyor")
	ErrBadCoinbase     = errors.New("geçersiz coinbase işlemi")
	ErrTooManyTx       = errors.New("blokta çok fazla işlem var")
	ErrTimestampOld    = errors.New("blok zamanı son blokların medyanından eski")
	ErrTimestampFuture = errors.New("blok zamanı çok ileride")
	ErrBadNonce        = errors.New("işlem sırası (nonce) beklenen değil")
	ErrInsufficientBal = errors.New("yetersiz bakiye")
	ErrOverflow        = errors.New("bakiye taşması")
	ErrCorruptState    = errors.New("defter kaydı bozuk")
	ErrKnownInvalid    = errors.New("blok daha önce geçersiz bulundu")
)

// Blockchain LevelDB üzerinde tutulan zincir ve hesap durumudur.
// Tüm okuma/yazmalar mu ile korunur; P2P ve HTTP eşzamanlı erişebilir.
type Blockchain struct {
	mu       sync.RWMutex
	db       *leveldb.DB
	params   *Params
	bits     uint32 // başlangıç (çapa) hedefi; blok başına hedef ASERT ile hesaplanır
	lastHash string
	log      *slog.Logger
	now      func() int64 // testlerde saat sabitlemek için
	rejected rejectCache

	// Blok indeksi (bkz. chain.go). Hepsi mu ile korunur.
	index     map[string]*indexEntry // özet → indeks kaydı (ana ve yan bloklar)
	children  map[string][]string    // ebeveyn özeti → çocuk özetleri
	perHeight map[int64]int          // yükseklik → saklanan blok sayısı
	main      map[int64]string       // ana zincir: yükseklik → özet
	tip       *indexEntry
	deepReorg bool
	syncAllow map[string]struct{} // yan zincir sınırı uygulanmayacak bloklar (bkz. sync.go)

	mineChunk  int          // kazımda denetimler arası nonce sayısı; 0 ise mineChunk (testler küçültür)
	mineHook   func()       // testler için: her nonce diliminden önce çağrılır
	bodyChecks atomic.Int64 // checkBody çağrı sayısı; testler imza doğrulamasının tekrarlanmadığını ölçer
}

// NewBlockchain verilen ağ için veritabanını açar; zincir boşsa ağın sabit genesis bloğunu
// yazar. Genesis'i tanımsız ağ (lansman öncesi mainnet) başlatılamaz; başka bir ağın ya da
// eski şemanın veri dizini reddedilir.
func NewBlockchain(dbPath string, p *Params) (*Blockchain, error) {
	if p == nil {
		return nil, ErrNoGenesis
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	genesis, err := NewGenesisBlock(p)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	if _, err := TargetFromBits(p.Bits); err != nil {
		return nil, fmt.Errorf("%s başlangıç hedefi: %w", p.Name, err)
	}
	bc := &Blockchain{
		params: p,
		bits:   p.Bits,
		log:    slog.Default().With("component", "blockchain", "network", p.Name),
		now:    func() int64 { return time.Now().Unix() },

		index:     map[string]*indexEntry{},
		children:  map[string][]string{},
		perHeight: map[int64]int{},
		main:      map[int64]string{},
		syncAllow: map[string]struct{}{},
	}
	// Kodda üretilen genesis, sabitlenmiş özete eşit, kendi hedefine kazılmış ve biçimce
	// geçerli olmalı; aksi halde kodlama sessizce değişmiş demektir (ağ bölünmesi).
	if genesis.Hash != p.GenesisHash || bc.checkHeader(genesis) != nil ||
		!crypto.IsValidAddress(genesis.Transactions[0].Receiver) {
		return nil, fmt.Errorf("%w: %s (üretilen %s)", ErrBadGenesis, p.Name, genesis.Hash)
	}

	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		return nil, fmt.Errorf("veritabanı açılamadı: %w", err)
	}
	bc.db = db

	data, err := db.Get([]byte(keyLastHash), nil)
	switch {
	case errors.Is(err, leveldb.ErrNotFound):
		// "lh" yoksa dizin tamamen boş olmalı: yarım ya da yabancı bir dizinde kalan
		// bakiye/nonce kayıtları yeni zincirin durumuna karışmasın.
		it := db.NewIterator(nil, nil)
		nonEmpty := it.First()
		it.Release()
		if nonEmpty {
			db.Close()
			return nil, fmt.Errorf("%w: son blok kaydı yok ama dizin boş değil", ErrSchemaMismatch)
		}
		bc.log.Info("zincir boş, genesis bloğu yazılıyor", "genesis", genesis.Hash)
		if err := bc.writeGenesis(genesis); err != nil {
			db.Close()
			return nil, fmt.Errorf("genesis yazılamadı: %w", err)
		}
	case err != nil:
		db.Close()
		return nil, fmt.Errorf("son blok okunamadı: %w", err)
	default:
		schema, err := db.Get([]byte(keySchema), nil)
		if err != nil || string(schema) != schemaVersion {
			db.Close()
			if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
				return nil, fmt.Errorf("şema sürümü okunamadı: %w", err)
			}
			return nil, fmt.Errorf("%w (diskte %q, beklenen %q)", ErrSchemaMismatch, schema, schemaVersion)
		}
		stored, err := db.Get([]byte(keyGenesis), nil)
		if err != nil || string(stored) != p.GenesisHash {
			db.Close()
			return nil, fmt.Errorf("%w: diskte %q, %s genesis'i %s", ErrGenesisMismatch, stored, p.Name, p.GenesisHash)
		}
		if err := bc.loadIndex(); err != nil {
			db.Close()
			return nil, fmt.Errorf("blok indeksi okunamadı: %w", err)
		}
		tip, ok := bc.index[string(data)]
		if !ok || !bc.isMain(tip) {
			db.Close()
			return nil, fmt.Errorf("%w: uç indekste ya da ana zincirde yok", ErrCorruptState)
		}
		bc.tip = tip
		bc.lastHash = tip.Hash
		if err := bc.recoverBestLocked(); err != nil {
			db.Close()
			return nil, err
		}
		bc.log.Info("mevcut zincir diskten yüklendi", "last_hash", bc.lastHash, "yukseklik", bc.tip.Height,
			"indeks", len(bc.index))
	}
	if tip, err := bc.getBlock(bc.lastHash); err == nil && tip.Timestamp > bc.now()+MaxFutureDrift {
		// Yerel saat zincir ucunun gerisinde: yeni bloklar gelecek tarihli görünecek.
		bc.log.Warn("yerel saat zincir ucunun gerisinde; saatinizi NTP ile doğrulayın",
			"uc_zamani", tip.Timestamp, "yerel", bc.now())
	}
	return bc, nil
}

// Bits zincirin sabit compact hedefidir.
func (bc *Blockchain) Bits() uint32 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.params.nextBits(bc.tip.Height, bc.tip.Timestamp, bc.tip.AnchorTime)
}

// BitsAfter ebeveyni parentHash olan bloğun hedefidir (ebeveyn indekste olmalıdır).
func (bc *Blockchain) BitsAfter(parentHash string) (uint32, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.expectedBitsLocked(parentHash, nil)
}

// GetBalance bir adresin onaylanmış bakiyesini döndürür.
func (bc *Blockchain) GetBalance(address string) (int64, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.readInt(prefixBalance + address)
}

// GetNonce bir adresin son onaylanmış işlem sırasını döndürür; sonraki işlem bunun +1'i olmalı.
func (bc *Blockchain) GetNonce(address string) (uint64, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	v, err := bc.readInt(prefixNonce + address)
	return uint64(v), err
}

// GetLatestBlock zincirin ucundaki bloğu döndürür.
func (bc *Blockchain) GetLatestBlock() (*Block, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.getBlock(bc.lastHash)
}

// AddBlock işlemlerden bir blok şablonu kurar, onu zincir kilidi tutulmadan kazar ve
// zincire ekler (NewTemplate → Mine → SubmitBlock). Kazım sırasında uç değişirse
// ErrStaleTemplate döner; çağıran yeni şablonla yeniden dener.
func (bc *Blockchain) AddBlock(transactions []Transaction, minerAddress string) (*Block, error) {
	t, err := bc.NewTemplate(transactions, minerAddress)
	if err != nil {
		return nil, err
	}
	b, err := bc.Mine(t, nil)
	if err != nil {
		return nil, err
	}
	if err := bc.SubmitBlock(b); err != nil {
		return nil, err
	}
	return b, nil
}

// AcceptBlock ağdan gelen bir bloğu tam doğrulamadan geçirip zincire ekler.
// Pahalı, durumdan bağımsız kontroller (PoW, imzalar) yazma kilidi alınmadan yapılır;
// kalıcı olarak geçersiz bloklar önbelleğe alınır ki aynı blok tekrar tekrar
// gönderilerek CPU ve kilit tüketilemesin.
func (bc *Blockchain) AcceptBlock(b *Block) error {
	if bc.rejected.has(b.Hash) {
		return ErrKnownInvalid
	}
	// Başlık hataları önbelleğe alınmaz: özet yalnızca başlığı kapsar ve gövde ancak Merkle
	// kökü doğrulandıktan sonra özete bağlıdır. Aksi halde saldırgan dürüst bir başlığa çöp
	// gövde ekleyerek o özeti her yerde "geçersiz" diye zehirleyebilirdi.
	if err := bc.bitsGate(b); err != nil {
		bc.log.Debug("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}
	if err := bc.checkHeader(b); err != nil {
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}
	// Ebeveyne göre ucuz kontroller (bilinen blok, bilinmeyen/geçersiz ebeveyn, yükseklik,
	// medyan zaman, yan zincir sınırları) imza doğrulamasından önce okuma kilidiyle yapılır:
	// zincirdeki bir blok ya da yanlış ebeveynli blok yeniden gönderilerek ~1000 ML-DSA
	// doğrulaması yaptırılamasın. Ebeveyni bilinmeyen blok önbelleğe alınmaz; eksik atalar
	// başlık öncelikli senkronizasyonla eşlerden çekilir.
	bc.mu.RLock()
	_, perr := bc.precheckLocked(b)
	retry := false
	if e, ok := bc.index[b.Hash]; ok && errors.Is(perr, ErrKnownBlock) {
		retry = e.Status != statusInvalid && e.work.Cmp(bc.tip.work) > 0
	}
	bc.mu.RUnlock()
	if retry {
		bc.mu.Lock()
		defer bc.mu.Unlock()
		return bc.retryKnownLocked(b.Hash)
	}
	if perr != nil {
		if errors.Is(perr, ErrBadPrevHash) {
			bc.log.Debug("blok ebeveyni bilinmiyor", "index", b.Index, "hash", b.Hash)
		} else if !errors.Is(perr, ErrKnownBlock) {
			bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", perr)
		}
		return perr
	}
	// FTL yalnızca yerel saate bağlıdır; imza doğrulamasından önce bakılır ve önbelleğe
	// alınmaz: gelecek tarihli blok tekrar gönderilerek ML-DSA doğrulaması yaptırılamaz, saat
	// yetişince aynı blok bir sonraki senkronizasyon turunda eşten yeniden çekilir.
	if ahead := b.Timestamp - bc.now(); ahead > MaxFutureDrift {
		bc.log.Debug("blok reddedildi: zaman çok ileride (saatinizi NTP ile doğrulayın)",
			"index", b.Index, "hash", b.Hash, "ileri_sn", ahead, "ftl_sn", MaxFutureDrift)
		return ErrTimestampFuture
	}
	if err := bc.checkBody(b); err != nil {
		bc.rejected.add(b.Hash) // gövde Merkle köküyle özete bağlı: hata içeriğe özgü ve kalıcı
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	if err := bc.storeAndActivateLocked(b); err != nil {
		if !errors.Is(err, ErrKnownBlock) {
			bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		}
		return err
	}
	return nil
}

// orphanEasyShift: ebeveyni bilinmeyen blok ucun bir sonraki hedefinden en fazla 2^orphanEasyShift
// kat kolay hedef ilan edebilir.
const orphanEasyShift = 2

// bitsGate zorluk kapısıdır; PoW, özet ve Merkle kökünden önce yalnızca indekse bakar.
// Ebeveyn biliniyorsa hedef tam olarak beklenen değer olmalıdır. Bilinmiyorsa beklenen değer
// hesaplanamaz; blok ucun hedefinden en fazla 4 kat kolay bir hedef ilan etmelidir: aksi halde
// neredeyse bedava kazılan sahte "yetim" bloklar özet hesaplatıp her biri senkronizasyon
// turu tetiklerdi. Dürüst ama çok farklı hedefli bir dal (ör. uzun duraklamadan sonra)
// periyodik senkronizasyonla gelir.
func (bc *Blockchain) bitsGate(b *Block) error {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if parent, ok := bc.index[b.PrevHash]; ok {
		if b.Bits != bc.params.nextBits(parent.Height, parent.Timestamp, parent.AnchorTime) {
			return ErrBadDifficulty
		}
		return nil
	}
	claimed, err := TargetFromBits(b.Bits)
	if err != nil {
		return ErrBadBits
	}
	if claimed.Cmp(bc.params.powLimitTarget()) > 0 {
		return ErrOrphanTooEasy // ağ tabanından kolay hedef hiçbir zincirde geçerli olamaz
	}
	tip, err := TargetFromBits(bc.params.nextBits(bc.tip.Height, bc.tip.Timestamp, bc.tip.AnchorTime))
	if err != nil {
		return err
	}
	if claimed.Cmp(tip.Lsh(tip, orphanEasyShift)) > 0 {
		return ErrOrphanTooEasy
	}
	return nil
}

// checkStateless zincir durumuna bakmadan yapılabilen tüm kontrollerdir. Kilit gerektirmez.
func (bc *Blockchain) checkStateless(b *Block) error {
	if err := bc.checkHeader(b); err != nil {
		return err
	}
	return bc.checkBody(b)
}

// checkHeader başlığı ve gövdenin başlığa bağlılığını doğrular: sürüm, hedef, PoW, özet,
// işlem sayısı sınırı ve Merkle kökü. Buradaki hatalar önbelleğe alınmamalıdır.
func (bc *Blockchain) checkHeader(b *Block) error {
	if b.Version != BlockVersion {
		return ErrBadVersion
	}
	// Hedefin beklenen değeri ebeveyne bağlıdır ve precheckLocked'ta denetlenir; ağdan gelen
	// bloklarda AcceptBlock bunu PoW'dan önce yapar (kolay hedef ilan eden blok özet ve
	// Merkle kökü hesaplatamaz).
	if !MeetsTarget(b.Hash, b.Bits) {
		return ErrInsufficientPoW // ucuz kontrol önce: PoW'suz bloklar özet hesaplatamaz
	}
	if b.Hash != b.CalculateHash() { // kanonik olmayan PrevHash/MerkleRoot de burada düşer
		return ErrBadHash
	}
	if len(b.Transactions) == 0 {
		return ErrBadCoinbase
	}
	if len(b.Transactions)-1 > MaxTxPerBlock {
		return ErrTooManyTx // Merkle hesaplamadan önce: büyük gövdeyle CPU tüketilemesin
	}
	if b.MerkleRoot != b.ComputeMerkleRoot() {
		return ErrBadMerkleRoot
	}
	return nil
}

// checkBody coinbase biçimini, işlem imzalarını ve ödül + ücret toplamını doğrular.
// checkHeader'dan geçmiş blokta gövde özete bağlıdır; buradaki hatalar kalıcıdır.
func (bc *Blockchain) checkBody(b *Block) error {
	bc.bodyChecks.Add(1)
	// Ödül yüksekliğe bağlıdır; ağdan gelen yükseklik ödülden önce sınırlanır. Ebeveynle
	// tutarlılığı (Index == prev+1) checkStateful denetler.
	if b.Index < 1 {
		return ErrBadIndex
	}
	cb := &b.Transactions[0]
	if !cb.IsCoinbase || cb.Sender != CoinbaseSender || cb.Fee != 0 ||
		!crypto.IsValidAddress(cb.Receiver) || cb.Nonce != uint64(b.Index) ||
		len(cb.PublicKey) != 0 || len(cb.Signature) != 0 || cb.ExpiryHeight != 0 {
		return ErrBadCoinbase
	}
	// İki geçiş: önce bütün ucuz kontroller (biçim, son kullanma), sonra bütün ML-DSA
	// doğrulamaları. Tek geçişte 999 geçerli imzanın ardına konan tek bir süresi dolmuş
	// işlem, blok reddedilmeden önce 999 imza doğrulaması yaptırırdı.
	for i := 1; i < len(b.Transactions); i++ {
		tx := &b.Transactions[i]
		if err := tx.checkFormat(); err != nil {
			return fmt.Errorf("işlem %d: %w", i, err)
		}
		if err := tx.CheckExpiry(b.Index); err != nil {
			return fmt.Errorf("işlem %d: %w", i, err)
		}
	}
	for i := 1; i < len(b.Transactions); i++ {
		if err := b.Transactions[i].verifySignature(bc.params); err != nil {
			return fmt.Errorf("işlem %d: %w", i, err)
		}
	}
	// Ücretler yalnızca VerifyStateless'tan geçtikten (Fee >= 0) sonra toplanır.
	want, err := coinbaseValue(BlockSubsidy(b.Index, bc.params), b.Transactions[1:])
	if err != nil {
		return err
	}
	if cb.Amount != want {
		return fmt.Errorf("%w: tutar %d, beklenen %d (ödül + ücretler)", ErrBadCoinbase, cb.Amount, want)
	}
	return nil
}

// coinbaseValue blok ödülü (BlockSubsidy) ile işlem ücretlerinin toplamını taşma
// kontrolüyle hesaplar. Ücretler negatifse ErrNegativeFee döner.
func coinbaseValue(subsidy int64, txs []Transaction) (int64, error) {
	total := subsidy
	for i := range txs {
		if txs[i].Fee < 0 {
			return 0, ErrNegativeFee
		}
		if total > math.MaxInt64-txs[i].Fee {
			return 0, fmt.Errorf("%w: ücret toplamı", ErrOverflow)
		}
		total += txs[i].Fee
	}
	return total, nil
}

// checkStateful bloğu zincir ucuna göre kontrol eder ve işlemleri view üzerinde uygular
// (diske yazmadan). checkStateless'tan geçmiş bloklar için çağrılmalıdır.
func (bc *Blockchain) checkStateful(b *Block, prev *Block, view *stateView) error {
	if b.Index != prev.Index+1 {
		return ErrBadIndex
	}
	if b.PrevHash != prev.Hash {
		return ErrBadPrevHash
	}
	mtp, err := bc.medianTimePast(prev)
	if err != nil {
		return err
	}
	if b.Timestamp <= mtp {
		return ErrTimestampOld
	}
	if b.Timestamp > bc.now()+MaxFutureDrift {
		return ErrTimestampFuture
	}
	// Bu bloğun coinbase'i hemen alacaklandırılmaz (olgunluk); CoinbaseMaturity blok önceki
	// atanın coinbase'i işlemlerden önce alacaklandırılır, böylece aynı blokta harcanabilir.
	if err := bc.creditMaturedLocked(view, b.Index, prev.Hash); err != nil {
		return err
	}
	for i := 1; i < len(b.Transactions); i++ {
		if err := view.apply(&b.Transactions[i]); err != nil {
			return fmt.Errorf("işlem %d: %w", i, err)
		}
	}
	return nil
}

// isPermanentStatefulErr, aynı ebeveyn üzerinde sonucu zamanla değişmeyecek kural
// ihlallerini ayırır. ErrTimestampFuture önbelleğe alınmaz: saati biraz geride olan
// düğümler bloğu kalıcı reddedip ağı bölerdi. Disk/okuma hataları ve ErrCorruptState
// geçicidir; dürüst bir bloğu kalıcı olarak dışlamamalıdır.
func isPermanentStatefulErr(err error) bool {
	for _, perm := range []error{ErrTimestampOld, ErrBadNonce, ErrInsufficientBal, ErrOverflow, ErrNonPositiveAmount, ErrNegativeFee} {
		if errors.Is(err, perm) {
			return true
		}
	}
	return false
}

// rejectCache kalıcı olarak geçersiz blok özetlerini sınırlı boyutta tutar.
type rejectCache struct {
	mu    sync.Mutex
	set   map[string]struct{}
	order []string
}

const rejectCacheSize = 4096

func (c *rejectCache) has(h string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.set[h]
	return ok
}

func (c *rejectCache) add(h string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil {
		c.set = map[string]struct{}{}
	}
	if _, ok := c.set[h]; ok {
		return
	}
	if len(c.order) >= rejectCacheSize {
		delete(c.set, c.order[0])
		c.order = c.order[1:]
	}
	c.set[h] = struct{}{}
	c.order = append(c.order, h)
}

// writeGenesis boş veritabanına genesis bloğunu, indeks kaydını, yükseklik tablosunu, şemayı
// ve ağ kimliğini tek bir atomik batch ile yazar. Genesis'in tek işlemi sıfır değerli
// coinbase'dir: bakiye yazılmaz (premine yok).
func (bc *Blockchain) writeGenesis(g *Block) error {
	e, err := newIndexEntry(g, nil)
	if err != nil {
		return err
	}
	e.Status = statusValid
	e.CbTo, e.CbValue = "", 0 // genesis hiçbir şey alacaklandırmaz (premine yok)
	data, err := json.Marshal(g)
	if err != nil {
		return fmt.Errorf("blok serileştirilemedi: %w", err)
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(g.Hash), data)
	if err := bc.putEntry(batch, e); err != nil {
		return err
	}
	batch.Put([]byte(heightKey(0)), []byte(g.Hash))
	batch.Put([]byte(keyLastHash), []byte(g.Hash))
	batch.Put([]byte(keySchema), []byte(schemaVersion))
	batch.Put([]byte(keyGenesis), []byte(g.Hash))
	if err := bc.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return err
	}
	bc.addToIndex(e)
	bc.main[0] = g.Hash
	bc.tip = e
	bc.lastHash = g.Hash
	return nil
}

// medianTimePast prev dahil son MedianTimeSpan bloğun zaman damgalarının medyanıdır.
func (bc *Blockchain) medianTimePast(prev *Block) (int64, error) {
	times := []int64{prev.Timestamp}
	cur := prev
	for len(times) < MedianTimeSpan && cur.Index > 0 {
		p, err := bc.getBlock(cur.PrevHash)
		if err != nil {
			return 0, err
		}
		times = append(times, p.Timestamp)
		cur = p
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], nil
}

func (bc *Blockchain) getBlock(hash string) (*Block, error) {
	data, err := bc.db.Get([]byte(hash), nil)
	if err != nil {
		return nil, fmt.Errorf("blok okunamadı (%s): %w", hash, err)
	}
	var block Block
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("%w: blok %s: %v", ErrCorruptState, hash, err)
	}
	return &block, nil
}

func (bc *Blockchain) readInt(key string) (int64, error) {
	data, err := bc.db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil // Hiç işlem görmemiş adres
	}
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrCorruptState, key)
	}
	return v, nil
}

func (bc *Blockchain) Close() error {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.db.Close()
}

// stateView henüz yazılmamış durum değişikliklerini bellekte tutar; okumalar base'den
// (disk ya da reorg sırasında bir overlay) yapılır. Aynı bloktaki işlemler kümülatif kontrol
// edilir (çift harcama koruması).
type stateView struct {
	bc       *Blockchain
	base     kvReader
	balances map[string]int64
	nonces   map[string]uint64
}

func newStateView(bc *Blockchain) *stateView {
	return &stateView{bc: bc, base: bc, balances: map[string]int64{}, nonces: map[string]uint64{}}
}

func (v *stateView) readInt(key string) (int64, error) {
	s, ok, err := v.base.getRaw(key)
	if err != nil || !ok {
		return 0, err // hiç işlem görmemiş adres
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s", ErrCorruptState, key)
	}
	return n, nil
}

func (v *stateView) balance(addr string) (int64, error) {
	if b, ok := v.balances[addr]; ok {
		return b, nil
	}
	return v.readInt(prefixBalance + addr)
}

func (v *stateView) nonce(addr string) (uint64, error) {
	if n, ok := v.nonces[addr]; ok {
		return n, nil
	}
	n, err := v.readInt(prefixNonce + addr)
	return uint64(n), err
}

// commitTo view'deki değişiklikleri overlay'e uygular ve geri alma kaydını döndürür: her
// anahtarın overlay'deki önceki değeri (yoksa nil; geri almada anahtar silinir, 0 yazılmaz).
func (v *stateView) commitTo(o *overlay) ([]undoItem, error) {
	vals := map[string]string{}
	for addr, bal := range v.balances {
		vals[prefixBalance+addr] = strconv.FormatInt(bal, 10)
	}
	for addr, n := range v.nonces {
		vals[prefixNonce+addr] = strconv.FormatUint(n, 10)
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	undo := make([]undoItem, 0, len(keys))
	for _, k := range keys {
		prev, ok, err := o.getRaw(k)
		if err != nil {
			return nil, err
		}
		item := undoItem{Key: k}
		if ok {
			p := prev
			item.Prev = &p
		}
		undo = append(undo, item)
		nv := vals[k]
		o.set(k, &nv)
	}
	return undo, nil
}

func (v *stateView) credit(addr string, amount int64) error {
	bal, err := v.balance(addr)
	if err != nil {
		return err
	}
	if amount < 0 || bal > math.MaxInt64-amount {
		return ErrOverflow
	}
	v.balances[addr] = bal + amount
	return nil
}

// apply imzası doğrulanmış bir transferi uygular: sıra, bakiye ve taşma kontrolü.
// Hata durumunda view değişmez.
func (v *stateView) apply(tx *Transaction) error {
	n, err := v.nonce(tx.Sender)
	if err != nil {
		return err
	}
	if tx.Nonce != n+1 {
		return fmt.Errorf("%w: beklenen %d, gelen %d", ErrBadNonce, n+1, tx.Nonce)
	}
	sb, err := v.balance(tx.Sender)
	if err != nil {
		return err
	}
	if tx.Amount <= 0 {
		return ErrNonPositiveAmount
	}
	if tx.Fee < 0 {
		return ErrNegativeFee
	}
	if tx.Amount > math.MaxInt64-tx.Fee {
		return fmt.Errorf("%w: tutar + ücret", ErrOverflow)
	}
	debit := tx.Amount + tx.Fee
	if sb < debit {
		return fmt.Errorf("%w: mevcut %d, istenen %d (tutar + ücret)", ErrInsufficientBal, sb, debit)
	}
	rb, err := v.balance(tx.Receiver)
	if err != nil {
		return err
	}
	if rb > math.MaxInt64-tx.Amount {
		return ErrOverflow
	}
	v.balances[tx.Sender] = sb - debit
	v.balances[tx.Receiver] = rb + tx.Amount
	v.nonces[tx.Sender] = n + 1
	return nil
}
