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
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"quantum-bitcoin/crypto"
)

const (
	keyLastHash   = "lh"
	prefixBalance = "bal_"
	prefixNonce   = "nonce_"
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
	ErrBadNonce        = errors.New("işlem sırası (nonce) beklenen değil")
	ErrInsufficientBal = errors.New("yetersiz bakiye")
	ErrOverflow        = errors.New("bakiye taşması")
	ErrCorruptState    = errors.New("defter kaydı bozuk")
	ErrKnownInvalid    = errors.New("blok daha önce geçersiz bulundu")
)

// Blockchain LevelDB üzerinde tutulan zincir ve hesap durumudur.
// Tüm okuma/yazmalar mu ile korunur; P2P ve HTTP eşzamanlı erişebilir.
type Blockchain struct {
	mu         sync.RWMutex
	db         *leveldb.DB
	difficulty int
	lastHash   string
	log        *slog.Logger
	now        func() int64 // testlerde saat sabitlemek için
	rejected   rejectCache
}

// NewBlockchain veritabanını açar; zincir boşsa genesis bloğunu yazar.
func NewBlockchain(dbPath string, difficulty int) (*Blockchain, error) {
	if difficulty < 1 || difficulty > 64 {
		return nil, fmt.Errorf("geçersiz zorluk: %d", difficulty)
	}
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		return nil, fmt.Errorf("veritabanı açılamadı: %w", err)
	}

	bc := &Blockchain{
		db:         db,
		difficulty: difficulty,
		log:        slog.Default().With("component", "blockchain"),
		now:        func() int64 { return time.Now().Unix() },
	}

	data, err := db.Get([]byte(keyLastHash), nil)
	switch {
	case errors.Is(err, leveldb.ErrNotFound):
		bc.log.Info("zincir boş, genesis bloğu üretiliyor")
		genesis := NewGenesisBlock()
		if err := bc.commitBlock(genesis, newStateView(bc)); err != nil {
			db.Close()
			return nil, fmt.Errorf("genesis yazılamadı: %w", err)
		}
	case err != nil:
		db.Close()
		return nil, fmt.Errorf("son blok okunamadı: %w", err)
	default:
		bc.lastHash = string(data)
		bc.log.Info("mevcut zincir diskten yüklendi", "last_hash", bc.lastHash)
	}
	return bc, nil
}

// Difficulty zincirin sabit zorluk seviyesidir.
func (bc *Blockchain) Difficulty() int { return bc.difficulty }

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

	view := newStateView(bc)
	var accepted []Transaction
	fees := int64(0)
	for i := range transactions {
		if len(accepted) == MaxTxPerBlock {
			bc.log.Warn("blok dolu, kalan işlemler bekletiliyor", "kalan", len(transactions)-i)
			break
		}
		tx := transactions[i]
		if err := tx.VerifyStateless(); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		// Ücret toplamı (ödül dahil) taşacaksa işlemi bloğa alma; aksi halde coinbase geçersiz olur.
		if BlockReward+fees > math.MaxInt64-tx.Fee {
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
		Amount:     BlockReward + fees,
		IsCoinbase: true,
	}

	mtp, err := bc.medianTimePast(prev)
	if err != nil {
		return nil, err
	}
	ts := bc.now()
	if ts <= mtp {
		ts = mtp + 1
	}

	newBlock := &Block{
		Index:        prev.Index + 1,
		Timestamp:    ts,
		Transactions: append([]Transaction{coinbase}, accepted...),
		PrevHash:     prev.Hash,
	}
	newBlock.MineBlock(bc.difficulty)

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
	if err := bc.checkStateless(b); err != nil {
		// Yalnızca özeti içerikle doğrulanmış blokları önbelleğe al; aksi halde saldırgan
		// dürüst bir bloğun özetini taşıyan çöp blokla o özeti "geçersiz" diye zehirleyebilir.
		if !errors.Is(err, ErrBadDifficulty) && !errors.Is(err, ErrInsufficientPoW) && !errors.Is(err, ErrBadHash) {
			bc.rejected.add(b.Hash)
		}
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

// checkStateless zincir durumuna bakmadan yapılabilen kontrollerdir: zorluk, özet, PoW,
// işlem sayısı, coinbase biçimi ve tüm işlemlerin imzaları. Kilit gerektirmez.
func (bc *Blockchain) checkStateless(b *Block) error {
	if b.Difficulty != bc.difficulty {
		return ErrBadDifficulty
	}
	if !MeetsTarget(b.Hash, bc.difficulty) {
		return ErrInsufficientPoW // ucuz kontrol önce: PoW'suz bloklar özet hesaplatamaz
	}
	if b.Hash != b.CalculateHash() {
		return ErrBadHash
	}
	if len(b.Transactions) == 0 {
		return ErrBadCoinbase
	}
	if len(b.Transactions)-1 > MaxTxPerBlock {
		return ErrTooManyTx
	}
	cb := &b.Transactions[0]
	if !cb.IsCoinbase || cb.Sender != CoinbaseSender || cb.Fee != 0 ||
		!crypto.IsValidAddress(cb.Receiver) || cb.Nonce != 0 ||
		len(cb.PublicKey) != 0 || len(cb.Signature) != 0 {
		return ErrBadCoinbase
	}
	for i := 1; i < len(b.Transactions); i++ {
		if err := b.Transactions[i].VerifyStateless(); err != nil {
			return fmt.Errorf("işlem %d: %w", i, err)
		}
	}
	// Ücretler yalnızca VerifyStateless'tan geçtikten (Fee >= 0) sonra toplanır.
	want, err := coinbaseValue(b.Transactions[1:])
	if err != nil {
		return err
	}
	if cb.Amount != want {
		return fmt.Errorf("%w: tutar %d, beklenen %d (ödül + ücretler)", ErrBadCoinbase, cb.Amount, want)
	}
	return nil
}

// coinbaseValue blok ödülü ile işlem ücretlerinin toplamını taşma kontrolüyle hesaplar.
// Çağıran, tüm ücretlerin negatif olmadığını önceden doğrulamış olmalıdır.
func coinbaseValue(txs []Transaction) (int64, error) {
	total := BlockReward
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
	if b.Index == 0 {
		// Genesis güvenilir yerel kaynaktır; bakiyelerini doğrudan uygula.
		for _, tx := range b.Transactions {
			if err := view.credit(tx.Receiver, tx.Amount); err != nil {
				return err
			}
		}
	}

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
