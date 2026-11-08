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
	schemaVersion = "3"
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
	ErrBadDifficulty   = errors.New("blok zorluğu zincir zorluğuyla eşleşmiyor")
	ErrInsufficientPoW = errors.New("iş ispatı (PoW) hedefi karşılanmıyor")
	ErrBadCoinbase     = errors.New("geçersiz coinbase işlemi")
	ErrTooManyTx       = errors.New("blokta çok fazla işlem var")
	ErrTimestampOld    = errors.New("blok zamanı son blokların medyanından eski")
	ErrTimestampFuture = errors.New("blok zamanı çok ileride")
	ErrParentPending   = errors.New("blok ebeveyni bekleme kuyruğunda")
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
	bits     uint32 // zincir genelinde sabit hedef (yeniden hedefleme ayrı bir PR'da)
	lastHash string
	log      *slog.Logger
	now      func() int64 // testlerde saat sabitlemek için
	rejected rejectCache
	pending  pendingBlocks // gelecek tarihli, gövdesi doğrulanmış bloklar (bkz. pending.go)

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
		if err := bc.commitBlock(genesis, newStateView(bc)); err != nil {
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
		bc.lastHash = string(data)
		bc.log.Info("mevcut zincir diskten yüklendi", "last_hash", bc.lastHash)
	}
	if tip, err := bc.getBlock(bc.lastHash); err == nil && tip.Timestamp > bc.now()+MaxFutureDrift {
		// Yerel saat zincir ucunun gerisinde: yeni bloklar gelecek tarihli görünecek.
		bc.log.Warn("yerel saat zincir ucunun gerisinde; saatinizi NTP ile doğrulayın",
			"uc_zamani", tip.Timestamp, "yerel", bc.now())
	}
	return bc, nil
}

// Bits zincirin sabit compact hedefidir.
func (bc *Blockchain) Bits() uint32 { return bc.bits }

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

// AddBlock işlemleri doğrular, geçersizleri ayıklar, madenci ödülünü ekler, bloğu kazar
// ve atomik olarak diske yazar.
func (bc *Blockchain) AddBlock(transactions []Transaction, minerAddress string) (*Block, error) {
	if !crypto.IsValidAddress(minerAddress) {
		return nil, fmt.Errorf("%w: madenci adresi", ErrBadReceiver)
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()

	prev, err := bc.getBlock(bc.lastHash)
	if err != nil {
		return nil, err
	}
	subsidy := BlockSubsidy(prev.Index+1, bc.params)

	view := newStateView(bc)
	var accepted []Transaction
	fees := int64(0)
	for i := range transactions {
		if len(accepted) == MaxTxPerBlock {
			bc.log.Warn("blok dolu, kalan işlemler bekletiliyor", "kalan", len(transactions)-i)
			break
		}
		tx := transactions[i]
		if err := tx.VerifyStateless(bc.params); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		// Ücret toplamı (ödül dahil) taşacaksa işlemi bloğa alma; aksi halde coinbase geçersiz olur.
		if subsidy+fees > math.MaxInt64-tx.Fee {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", "ücret toplamı taşar")
			continue
		}
		if err := view.apply(&tx); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		fees += tx.Fee
		accepted = append(accepted, tx)
	}

	coinbase := Transaction{
		Sender:     CoinbaseSender,
		Receiver:   minerAddress,
		Amount:     subsidy + fees,
		Nonce:      uint64(prev.Index + 1), // yükseklik: her coinbase'in kimliği benzersiz olur
		IsCoinbase: true,
	}

	mtp, err := bc.medianTimePast(prev)
	if err != nil {
		return nil, err
	}
	ts := bc.now()
	if ts <= mtp {
		// Yerel saat medyanın gerisinde: blok medyan+1 ile damgalanır. Saat FTL'den fazla
		// geride kaldıysa kendi bloğumuz da ErrTimestampFuture ile düşer; nedeni görünür olsun.
		// Aynı saniyede birkaç blok (regtest) olağandır; yalnızca belirgin geri kalma uyarılır.
		if mtp-ts >= bc.params.TargetSpacing {
			bc.log.Warn("yerel saat son blokların medyanının gerisinde; saatinizi NTP ile doğrulayın",
				"medyan", mtp, "yerel", ts)
		}
		ts = mtp + 1
	}

	newBlock := &Block{
		Index:        prev.Index + 1,
		Timestamp:    ts,
		Transactions: append([]Transaction{coinbase}, accepted...),
		PrevHash:     prev.Hash,
	}
	if err := newBlock.MineBlock(bc.bits); err != nil {
		return nil, err
	}

	// Kendi ürettiğimiz bloğu da ağdan gelen gibi doğrula: tek doğrulama yolu.
	if err := bc.acceptLocked(newBlock); err != nil {
		return nil, fmt.Errorf("üretilen blok doğrulanamadı: %w", err)
	}
	return newBlock, nil
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
	if err := bc.checkHeader(b); err != nil {
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}
	// Ucun üzerine kurulmayan blok için imza doğrulaması yapma: zincirdeki eski bir blok ya da
	// yanlış ebeveynli blok yeniden gönderilerek her seferinde ~1000 ML-DSA doğrulaması
	// yaptırılamasın. Önbelleğe alınmaz (ileride geçerli olabilir); durumlu kontrol yine yapılır.
	// Ebeveyni bekleme kuyruğunda olan blok da beklemeye alınır: senkronizasyon olmadığından
	// atılırsa geri gelmez ve düğüm, ebeveyn eklendikten sonra zincirden kopar. Uç ve kuyruk
	// aynı okuma kilidi altında okunur; ProcessPending ebeveyni arada zincire ekleyemez.
	bc.mu.RLock()
	tip := bc.lastHash
	onPending := b.PrevHash != tip && bc.pending.has(b.PrevHash)
	bc.mu.RUnlock()
	if b.PrevHash != tip && !onPending {
		bc.log.Debug("blok ucun üzerine kurulmuyor", "index", b.Index, "hash", b.Hash)
		return ErrBadPrevHash
	}
	// FTL yalnızca yerel saate bağlıdır; imza doğrulamasından önce bakılır. Böylece gelecek
	// tarihli bir blok tekrar tekrar gönderilerek ~1000 ML-DSA doğrulaması yaptırılamaz:
	// pencere dışındakiler ve dolu kuyruğa giremeyecek olanlar ucuzca düşer, kuyruğa
	// girenler bir kez doğrulanır.
	if ahead := b.Timestamp - bc.now(); ahead > MaxFutureDrift || onPending {
		wait := ErrTimestampFuture
		if ahead <= MaxFutureDrift {
			wait = ErrParentPending
		}
		if bc.pending.has(b.Hash) {
			return wait
		}
		// Kuyruktan tahliye edilen ya da yetim kalıp atılan blok yeniden doğrulatılamaz:
		// aksi halde sabit bir blok kümesi tahliye/budama döngüsüyle süresiz doğrulatılırdı.
		if bc.pending.wasDropped(b.Hash) {
			return wait
		}
		// Bu iki ret yolu aynı blokla tekrar tekrar tetiklenebilir; log taşkını olmasın diye Debug.
		if ahead > pendingWindow {
			bc.log.Debug("blok reddedildi: zaman çok ileride (saatinizi NTP ile doğrulayın)",
				"index", b.Index, "hash", b.Hash, "ileri_sn", ahead, "ftl_sn", MaxFutureDrift)
			return ErrTimestampFuture
		}
		if !bc.pending.canAdd(b.Timestamp) {
			bc.log.Debug("bekleme kuyruğu dolu, blok atıldı", "index", b.Index, "hash", b.Hash, "ileri_sn", ahead)
			return wait
		}
		if err := bc.checkBody(b); err != nil {
			bc.rejected.add(b.Hash)
			bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
			return err
		}
		queued := bc.pending.add(b)
		bc.log.Warn("blok beklemeye alındı, saat yetişince yeniden denenecek (saatinizi NTP ile doğrulayın)",
			"index", b.Index, "hash", b.Hash, "ileri_sn", ahead, "ftl_sn", MaxFutureDrift,
			"ebeveyn_beklemede", onPending, "kuyrukta", queued)
		return wait
	}
	bc.pending.remove(b.Hash)
	if err := bc.checkBody(b); err != nil {
		bc.rejected.add(b.Hash) // gövde Merkle köküyle özete bağlı: hata içeriğe özgü ve kalıcı
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	return bc.acceptStatefulLocked(b)
}

func (bc *Blockchain) acceptLocked(b *Block) error {
	if err := bc.checkStateless(b); err != nil {
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}
	return bc.acceptStatefulLocked(b)
}

func (bc *Blockchain) acceptStatefulLocked(b *Block) error {
	prev, err := bc.getBlock(bc.lastHash)
	if err != nil {
		return err
	}
	view := newStateView(bc)
	if err := bc.checkStateful(b, prev, view); err != nil {
		// Aynı ebeveyn üzerinde durum kuralı ihlali kalıcıdır (zincir yeniden düzenlenmiyor).
		// İndeks/önceki özet uyuşmazlığı ise ileride geçerli olabilir; önbelleğe alınmaz.
		if b.PrevHash == prev.Hash && b.Index == prev.Index+1 && isPermanentStatefulErr(err) {
			bc.rejected.add(b.Hash)
		}
		bc.log.Warn("blok reddedildi", "index", b.Index, "hash", b.Hash, "neden", err)
		return err
	}
	return bc.commitBlock(b, view)
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
	if b.Bits != bc.bits {
		return ErrBadDifficulty
	}
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
		len(cb.PublicKey) != 0 || len(cb.Signature) != 0 {
		return ErrBadCoinbase
	}
	for i := 1; i < len(b.Transactions); i++ {
		if err := b.Transactions[i].VerifyStateless(bc.params); err != nil {
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
	if err := view.credit(b.Transactions[0].Receiver, b.Transactions[0].Amount); err != nil {
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

// commitBlock bloğu ve view'deki tüm durum değişikliklerini tek bir atomik batch ile yazar.
func (bc *Blockchain) commitBlock(b *Block, view *stateView) error {
	// Genesis'in tek işlemi sıfır değerli coinbase'dir: bakiye yazılmaz (premine yok).
	blockData, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("blok serileştirilemedi: %w", err)
	}

	batch := new(leveldb.Batch)
	for addr, bal := range view.balances {
		batch.Put([]byte(prefixBalance+addr), []byte(strconv.FormatInt(bal, 10)))
	}
	for addr, n := range view.nonces {
		batch.Put([]byte(prefixNonce+addr), []byte(strconv.FormatUint(n, 10)))
	}
	batch.Put([]byte(b.Hash), blockData)
	batch.Put([]byte(keyLastHash), []byte(b.Hash))
	if b.Index == 0 {
		// Şema ve ağ kimliği genesis ile aynı atomik yazımda.
		batch.Put([]byte(keySchema), []byte(schemaVersion))
		batch.Put([]byte(keyGenesis), []byte(b.Hash))
	}

	if err := bc.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return fmt.Errorf("blok diske yazılamadı: %w", err)
	}
	bc.lastHash = b.Hash
	bc.log.Info("blok zincire eklendi", "index", b.Index, "hash", b.Hash, "tx_sayisi", len(b.Transactions))
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

// stateView diskteki duruma uygulanmamış değişiklikleri bellekte tutar.
// Aynı bloktaki işlemler kümülatif kontrol edilir (çift harcama koruması).
type stateView struct {
	bc       *Blockchain
	balances map[string]int64
	nonces   map[string]uint64
}

func newStateView(bc *Blockchain) *stateView {
	return &stateView{bc: bc, balances: map[string]int64{}, nonces: map[string]uint64{}}
}

func (v *stateView) balance(addr string) (int64, error) {
	if b, ok := v.balances[addr]; ok {
		return b, nil
	}
	return v.bc.readInt(prefixBalance + addr)
}

func (v *stateView) nonce(addr string) (uint64, error) {
	if n, ok := v.nonces[addr]; ok {
		return n, nil
	}
	n, err := v.bc.readInt(prefixNonce + addr)
	return uint64(n), err
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
