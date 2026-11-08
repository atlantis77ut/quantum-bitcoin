package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Zincir seçimi (kullanıcı kararı, 2026-11-08): uzlaşmada saf "en çok birikmiş iş" kuralı
// geçerlidir, reorg derinliğine sınır yoktur. Eşit işte ilk görülen kalır; yalnızca kesin
// olarak daha fazla iş taşıyan zincire geçilir. ReorgAlarmDepth'ten derin bir reorg zinciri
// değiştirir ama yüksek sesle alarm verir (DeepReorgAlarm); sayısal değer LWMA ile dondurulur.
//
// Disk düzeni: blok gövdeleri ham 64 hex özet anahtarındadır; diğer anahtarların hepsi '_'
// içeren öneklerle başlar ve özetlerle çakışmaz.
const (
	prefixIndex  = "idx_"  // idx_<özet>  → indexEntry (JSON)
	prefixHeight = "h_"    // h_<16 hex yükseklik> → ana zincirdeki blok özeti
	prefixUndo   = "undo_" // undo_<özet> → bloğun dokunduğu anahtarların önceki değerleri

	// ReorgAlarmDepth bu kadar bloktan derin reorg'da alarm verilir (uzlaşma kuralı değildir).
	ReorgAlarmDepth = 100

	// Yan zincir depolama politikası (uzlaşma kuralı değil, DoS sınırı). Ucun bu kadar
	// gerisinden çatallanan ve uçtan fazla iş taşımayan, tek tek gelen yan bloklar saklanmaz;
	// daha derin reorg'lar başlık öncelikli senkronizasyonla gelir (bkz. sync.go).
	MaxSideForkDepth = 2 * ReorgAlarmDepth

	// MaxBlocksPerHeight bir yükseklikte saklanan en fazla blok sayısıdır (ana blok dahil);
	// uçtan fazla iş taşıyan aday bu sınıra takılmaz.
	MaxBlocksPerHeight = 4
)

const (
	statusStored  uint8 = 1 // gövde doğrulandı, durumca henüz bağlanmadı
	statusValid   uint8 = 2 // en az bir kez ana zincire bağlandı
	statusInvalid uint8 = 3 // durum kuralı ihlali ya da geçersiz bir ataya sahip
)

var (
	ErrKnownBlock     = errors.New("blok zaten biliniyor")
	ErrSideChainLimit = errors.New("yan zincir depolama sınırı aşıldı")
)

// indexEntry blok indeksindeki bir bloğun başlık özetidir. Bits ve Timestamp, ileride
// LWMA'nın ebeveyn zincirinden hedef hesaplayabilmesi için saklanır.
type indexEntry struct {
	Hash      string `json:"hash"`
	Prev      string `json:"prev"`
	Height    int64  `json:"height"`
	Bits      uint32 `json:"bits"`
	Timestamp int64  `json:"timestamp"`
	Work      string `json:"work"` // genesis dahil birikmiş iş, hex
	Status    uint8  `json:"status"`

	// Başlığın geri kalanı: eşlere başlık sunarken disk okumadan yeniden kurulur.
	Version    uint32 `json:"version"`
	MerkleRoot string `json:"merkle_root"`
	Nonce      uint64 `json:"nonce"`

	// Coinbase alıcısı ve tutarı (ödül + ücretler): olgunlaşınca gövde okunmadan
	// alacaklandırılır. Yalnızca gövdesi doğrulanıp saklanan bloklar için anlamlıdır.
	CbTo    string `json:"cb_to,omitempty"`
	CbValue int64  `json:"cb_value,omitempty"`

	work *big.Int
}

// blockWork bir bloğun iş katkısıdır: 2^256 / (hedef + 1).
func blockWork(bits uint32) (*big.Int, error) {
	t, err := TargetFromBits(bits)
	if err != nil {
		return nil, err
	}
	num := new(big.Int).Lsh(big.NewInt(1), 256)
	return num.Div(num, new(big.Int).Add(t, big.NewInt(1))), nil
}

func newIndexEntry(b *Block, parentWork *big.Int) (*indexEntry, error) {
	w, err := blockWork(b.Bits)
	if err != nil {
		return nil, err
	}
	total := new(big.Int).Add(parentWork, w)
	e := &indexEntry{Hash: b.Hash, Prev: b.PrevHash, Height: b.Index, Bits: b.Bits,
		Timestamp: b.Timestamp, Work: total.Text(16), Status: statusStored, work: total,
		Version: b.Version, MerkleRoot: b.MerkleRoot, Nonce: b.Nonce}
	if len(b.Transactions) > 0 {
		e.CbTo, e.CbValue = b.Transactions[0].Receiver, b.Transactions[0].Amount
	}
	return e, nil
}

// creditMaturedLocked, height yüksekliğindeki blok bağlanırken olgunlaşan coinbase'i
// (height − CoinbaseMaturity yüksekliğindeki atanın ödülü + ücretleri) view'e alacaklandırır.
// Ata parentHash'ten indeks üzerinden yürünerek bulunur; bc.main kullanılmaz, çünkü reorg
// sırasında ana zincir tablosu henüz eski dalı gösterir. Genesis hiçbir şey alacaklandırmaz.
func (bc *Blockchain) creditMaturedLocked(v *stateView, height int64, parentHash string) error {
	m := height - bc.params.CoinbaseMaturity
	if m < 1 {
		return nil
	}
	e, ok := bc.index[parentHash]
	for ok && e.Height > m {
		e, ok = bc.index[e.Prev]
	}
	if !ok || e.Height != m || e.CbTo == "" {
		return fmt.Errorf("%w: olgunlaşan coinbase atası yok (yükseklik %d)", ErrCorruptState, m)
	}
	if e.CbValue == 0 {
		return nil
	}
	return v.credit(e.CbTo, e.CbValue)
}

func heightKey(h int64) string { return prefixHeight + fmt.Sprintf("%016x", h) }

func (bc *Blockchain) putEntry(batch *leveldb.Batch, e *indexEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	batch.Put([]byte(prefixIndex+e.Hash), data)
	return nil
}

// addToIndex bellekteki indeks yapılarını günceller (diske yazım çağıranın batch'indedir).
func (bc *Blockchain) addToIndex(e *indexEntry) {
	bc.index[e.Hash] = e
	bc.children[e.Prev] = append(bc.children[e.Prev], e.Hash)
	if e.Status != statusInvalid {
		bc.perHeight[e.Height]++ // geçersiz bloklar yükseklik yuvası tüketmez
	}
}

// loadIndex blok indeksini ve ana zincir yükseklik tablosunu diskten belleğe okur.
func (bc *Blockchain) loadIndex() error {
	it := bc.db.NewIterator(util.BytesPrefix([]byte(prefixIndex)), nil)
	for it.Next() {
		var e indexEntry
		if err := json.Unmarshal(it.Value(), &e); err != nil {
			it.Release()
			return fmt.Errorf("%w: indeks kaydı: %v", ErrCorruptState, err)
		}
		w, ok := new(big.Int).SetString(e.Work, 16)
		if !ok {
			it.Release()
			return fmt.Errorf("%w: indeks işi %q", ErrCorruptState, e.Work)
		}
		e.work = w
		bc.addToIndex(&e)
	}
	it.Release()
	if err := it.Error(); err != nil {
		return err
	}
	it = bc.db.NewIterator(util.BytesPrefix([]byte(prefixHeight)), nil)
	for it.Next() {
		h, err := strconv.ParseInt(string(it.Key()[len(prefixHeight):]), 16, 64)
		if err != nil {
			it.Release()
			return fmt.Errorf("%w: yükseklik anahtarı", ErrCorruptState)
		}
		bc.main[h] = string(it.Value())
	}
	it.Release()
	return it.Error()
}

func (bc *Blockchain) isMain(e *indexEntry) bool { return bc.main[e.Height] == e.Hash }

// forkPoint e'nin ana zincirle ortak son atasını döndürür.
func (bc *Blockchain) forkPoint(e *indexEntry) (*indexEntry, error) {
	for !bc.isMain(e) {
		p, ok := bc.index[e.Prev]
		if !ok {
			return nil, fmt.Errorf("%w: indekste ata eksik (%s)", ErrCorruptState, e.Prev)
		}
		e = p
	}
	return e, nil
}

// precheckLocked imza doğrulamasından önce ebeveyne göre yapılan ucuz kontrollerdir:
// bilinen blok, geçersiz ebeveyn, yükseklik, medyan zaman ve yan zincir depolama sınırları.
// Okuma ya da yazma kilidi altında çağrılır.
func (bc *Blockchain) precheckLocked(b *Block) (*indexEntry, error) {
	if _, ok := bc.index[b.Hash]; ok {
		return nil, ErrKnownBlock
	}
	parent, ok := bc.index[b.PrevHash]
	if !ok {
		return nil, ErrBadPrevHash
	}
	if parent.Status == statusInvalid {
		bc.rejected.add(b.Hash)
		return nil, ErrKnownInvalid
	}
	if b.Index != parent.Height+1 {
		bc.rejected.add(b.Hash) // yükseklik ve ebeveyn özete bağlı: kalıcı
		return nil, ErrBadIndex
	}
	// Yan zincir sınırı yalnızca bellekteki indekse bakar: sınıra takılan blok önbelleğe
	// alınmadığından tekrar tekrar gönderilebilir, bu yolun ucuz kalması gerekir. Başlıkları
	// doğrulanmış, ucun işini aşan bir zincirin blokları (syncAllow) sınıra takılmaz.
	if _, allowed := bc.syncAllow[b.Hash]; !allowed && parent.Hash != bc.tip.Hash {
		e, err := newIndexEntry(b, parent.work)
		if err != nil {
			return nil, err
		}
		if e.work.Cmp(bc.tip.work) <= 0 {
			fork, err := bc.forkPoint(parent)
			if err != nil {
				return nil, err
			}
			if bc.tip.Height-fork.Height > MaxSideForkDepth || bc.perHeight[b.Index] >= MaxBlocksPerHeight {
				return nil, ErrSideChainLimit
			}
		}
	}
	mtp, err := bc.mtpLocked(parent.Hash, nil) // indeksten: disk okuması yok
	if err != nil {
		return nil, err
	}
	if b.Timestamp <= mtp {
		bc.rejected.add(b.Hash)
		return nil, ErrTimestampOld
	}
	return parent, nil
}

// storeAndActivateLocked gövdesi doğrulanmış bloğu indekse ve diske yazar; ucun işini
// geçiyorsa en çok iş taşıyan zincire geçer. Yazma kilidi altında çağrılır.
func (bc *Blockchain) storeAndActivateLocked(b *Block) error {
	parent, err := bc.precheckLocked(b)
	if err != nil {
		return err
	}
	e, err := newIndexEntry(b, parent.work)
	if err != nil {
		return err
	}
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("blok serileştirilemedi: %w", err)
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(b.Hash), data)
	if err := bc.putEntry(batch, e); err != nil {
		return err
	}
	if err := bc.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return fmt.Errorf("blok diske yazılamadı: %w", err)
	}
	bc.addToIndex(e)
	delete(bc.syncAllow, b.Hash)
	if e.work.Cmp(bc.tip.work) <= 0 {
		bc.log.Info("yan blok saklandı (uçtan fazla iş taşımıyor)", "index", b.Index, "hash", b.Hash)
		return nil
	}
	return bc.activateLocked(e)
}

// retryKnownLocked, indekste saklı ve uçtan fazla iş taşıyan bir blok (ör. geçiş geçici bir
// hatayla — disk, saat geri gitmesi — yarım kaldıysa) yeniden gönderildiğinde zincire geçişi
// yeniden dener. Gövde saklanmadan önce doğrulandığından imza doğrulaması yapılmaz.
func (bc *Blockchain) retryKnownLocked(hash string) error {
	e, ok := bc.index[hash]
	if !ok || e.Status == statusInvalid || e.work.Cmp(bc.tip.work) <= 0 {
		return ErrKnownBlock
	}
	return bc.activateLocked(e)
}

// markInvalidLocked bloğu ve indeksteki tüm torunlarını geçersiz işaretler.
func (bc *Blockchain) markInvalidLocked(batch *leveldb.Batch, e *indexEntry) error {
	stack := []*indexEntry{e}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.Status != statusInvalid {
			bc.perHeight[cur.Height]--
		}
		cur.Status = statusInvalid
		bc.rejected.add(cur.Hash)
		if err := bc.putEntry(batch, cur); err != nil {
			return err
		}
		for _, h := range bc.children[cur.Hash] {
			if c, ok := bc.index[h]; ok && c.Status != statusInvalid {
				stack = append(stack, c)
			}
		}
	}
	return nil
}

// activateLocked ana zinciri target'a taşır: çatal noktasına kadar geri sarar, yeni dalı
// bağlar ve bütün durum değişikliğini (bakiyeler, nonce'lar, undo kayıtları, yükseklik
// tablosu, uç) tek bir atomik batch ile yazar. Yeni daldaki bir blok durum kuralını ihlal
// ederse o blok ve torunları geçersiz işaretlenir; geçerli önek uçtan fazla iş taşıyorsa
// zincir öneke geçer, taşımıyorsa uç değişmez.
func (bc *Blockchain) activateLocked(target *indexEntry) error {
	fork, err := bc.forkPoint(target)
	if err != nil {
		return err
	}
	var branch []*indexEntry
	for e := target; e.Hash != fork.Hash; e = bc.index[e.Prev] {
		branch = append(branch, e)
	}
	for i, j := 0, len(branch)-1; i < j; i, j = i+1, j-1 {
		branch[i], branch[j] = branch[j], branch[i]
	}

	ov := newOverlay(bc)
	var disconnected []*indexEntry
	for e := bc.tip; e.Hash != fork.Hash; e = bc.index[e.Prev] {
		undo, err := bc.loadUndo(e.Hash)
		if err != nil {
			return err
		}
		for _, u := range undo {
			ov.set(u.Key, u.Prev)
		}
		disconnected = append(disconnected, e)
	}

	batch := new(leveldb.Batch)
	invalid := new(leveldb.Batch) // geçersizlik işaretleri, geçiş olsa da olmasa da yazılır
	newTip := fork
	var connected []*indexEntry
	var failErr error
	for _, e := range branch {
		b, err := bc.getBlock(e.Hash)
		if err != nil {
			return err
		}
		prev, err := bc.getBlock(e.Prev)
		if err != nil {
			return err
		}
		v := &stateView{bc: bc, base: ov, balances: map[string]int64{}, nonces: map[string]uint64{}}
		if err := bc.checkStateful(b, prev, v); err != nil {
			failErr = err
			if isPermanentStatefulErr(err) || errors.Is(err, ErrBadIndex) || errors.Is(err, ErrBadPrevHash) {
				if err := bc.markInvalidLocked(invalid, e); err != nil {
					return err
				}
				bc.log.Warn("blok durum kontrolünden geçmedi, geçersiz işaretlendi", "index", e.Height, "hash", e.Hash, "neden", err)
			}
			break
		}
		undo, err := v.commitTo(ov)
		if err != nil {
			return err
		}
		data, err := json.Marshal(undo)
		if err != nil {
			return err
		}
		batch.Put([]byte(prefixUndo+e.Hash), data)
		connected = append(connected, e)
		newTip = e
	}

	if invalid.Len() > 0 {
		if err := bc.db.Write(invalid, &opt.WriteOptions{Sync: true}); err != nil {
			return fmt.Errorf("indeks yazılamadı: %w", err)
		}
	}
	if failErr != nil && newTip.work.Cmp(bc.tip.work) <= 0 {
		return failErr // geçiş yok: uç ve durum değişmez
	}

	ov.writeTo(batch)
	for _, e := range disconnected {
		batch.Delete([]byte(prefixUndo + e.Hash))
	}
	for h := newTip.Height + 1; h <= bc.tip.Height; h++ {
		batch.Delete([]byte(heightKey(h)))
	}
	for _, e := range connected {
		batch.Put([]byte(heightKey(e.Height)), []byte(e.Hash))
		if e.Status != statusValid {
			cp := *e
			cp.Status = statusValid
			if err := bc.putEntry(batch, &cp); err != nil {
				return err
			}
		}
	}
	batch.Put([]byte(keyLastHash), []byte(newTip.Hash))
	if err := bc.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return fmt.Errorf("zincir güncellenemedi: %w", err)
	}

	// Disk yazıldı; bellekteki görünümü eşitle.
	for h := newTip.Height + 1; h <= bc.tip.Height; h++ {
		delete(bc.main, h)
	}
	for _, e := range connected {
		bc.main[e.Height] = e.Hash
		e.Status = statusValid
	}
	oldTip := bc.tip
	bc.tip = newTip
	bc.lastHash = newTip.Hash

	if n := len(disconnected); n > 0 {
		bc.log.Warn("reorg: daha çok iş taşıyan zincire geçildi", "geri_sarilan", n,
			"baglanan", len(connected), "eski_uc", oldTip.Hash, "yeni_uc", newTip.Hash, "catal", fork.Height)
		if n > ReorgAlarmDepth {
			bc.deepReorg = true
			bc.log.Error("DERİN REORG ALARMI: onayları durdurun ve operatöre danışın",
				"derinlik", n, "esik", ReorgAlarmDepth, "catal", fork.Height)
		}
	}
	for _, e := range connected {
		bc.log.Info("blok zincire eklendi", "index", e.Height, "hash", e.Hash)
	}
	return failErr
}

// TipHash ana zincirin ucundaki bloğun özetidir.
func (bc *Blockchain) TipHash() string {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.lastHash
}

// DeepReorgAlarm, düğüm ReorgAlarmDepth'ten derin bir reorg yaşadıysa true döner. Borsa
// entegrasyonları bu durumda onay bildirmeyi durdurmalıdır.
func (bc *Blockchain) DeepReorgAlarm() bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.deepReorg
}

// recoverBestLocked açılışta, indekste uçtan fazla iş taşıyan geçerli bir blok varsa
// (ör. blok saklandıktan sonra zincire geçmeden çökme) zinciri ona taşır.
func (bc *Blockchain) recoverBestLocked() error {
	var cands []*indexEntry
	for _, e := range bc.index {
		if e.Status != statusInvalid && e.work.Cmp(bc.tip.work) > 0 {
			cands = append(cands, e)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].work.Cmp(cands[j].work) > 0 })
	for _, e := range cands {
		if e.Status == statusInvalid || e.work.Cmp(bc.tip.work) <= 0 {
			continue
		}
		if err := bc.activateLocked(e); err != nil {
			bc.log.Warn("açılışta zincire geçilemedi", "hash", e.Hash, "neden", err)
		}
	}
	return nil
}

// undoItem bir anahtarın bloktan önceki değeridir; Prev nil ise anahtar yoktu.
type undoItem struct {
	Key  string  `json:"k"`
	Prev *string `json:"p"`
}

func (bc *Blockchain) loadUndo(hash string) ([]undoItem, error) {
	data, err := bc.db.Get([]byte(prefixUndo+hash), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: undo kaydı yok (%s): %v", ErrCorruptState, hash, err)
	}
	var undo []undoItem
	if err := json.Unmarshal(data, &undo); err != nil {
		return nil, fmt.Errorf("%w: undo kaydı (%s): %v", ErrCorruptState, hash, err)
	}
	return undo, nil
}

// kvReader durum anahtarlarını okur; ok=false anahtarın olmadığı anlamına gelir.
type kvReader interface {
	getRaw(key string) (val string, ok bool, err error)
}

func (bc *Blockchain) getRaw(key string) (string, bool, error) {
	data, err := bc.db.Get([]byte(key), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(data), true, nil
}

// overlay diske yazılmamış anahtar değişikliklerini tutar; nil değer silinmiş anahtardır.
type overlay struct {
	base kvReader
	kv   map[string]*string
}

func newOverlay(base kvReader) *overlay { return &overlay{base: base, kv: map[string]*string{}} }

func (o *overlay) getRaw(key string) (string, bool, error) {
	if v, ok := o.kv[key]; ok {
		if v == nil {
			return "", false, nil
		}
		return *v, true, nil
	}
	return o.base.getRaw(key)
}

func (o *overlay) set(key string, v *string) { o.kv[key] = v }

func (o *overlay) writeTo(batch *leveldb.Batch) {
	keys := make([]string, 0, len(o.kv))
	for k := range o.kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := o.kv[k]; v == nil {
			batch.Delete([]byte(k))
		} else {
			batch.Put([]byte(k), []byte(*v))
		}
	}
}
