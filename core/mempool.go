package core

import (
	"bytes"
	"container/heap"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"quantum-bitcoin/crypto"
)

// Mempool onaylanmamış işlemleri tutar ve madenciye blok şablonu için sunar. Uzlaşma kuralı
// değil, düğüm politikasıdır (kullanıcı kararı 2026-11-09); kabul edilen her işlem yine de
// uzlaşma fonksiyonlarıyla (checkFormat, CheckExpiry, stateView.apply, imza) denetlenir.
//
// Kurallar:
//   - Gönderen başına bitişik nonce zinciri: havuzdaki ilk işlemin nonce'u onaylı nonce + 1'dir,
//     boşluk kabul edilmez. Onaylanmamış gelen para harcanamaz: her gönderenin zinciri yalnızca
//     onaylı bakiyesine (ve bu blokta olgunlaşan coinbase'e) göre doğrulanır. Böylece göndericiler
//     birbirine bağlı değildir; tahliye, süre ve reorg işlemleri gönderen başına yürür.
//   - Kabulde süresinin dolmasına en az MempoolExpiryMargin blok kalmalı ve son kullanma
//     yüksekliği en çok Params.MaxTxLifetime ileride olabilir; havuzda kalmak için yalnızca
//     uç+1'de geçerli olmak yeter. Süresi dolan işlem gönderenin sonraki işlemleriyle birlikte düşer.
//   - Aynı nonce'lu işlem ücret en az %MempoolReplaceBumpPercent ve en az MempoolMinFee arttıysa
//     yerini alır; gönderenin sonraki işlemleri yeniden denetlenir, artık geçmeyenler düşer.
//     Düşenler zaten yayılmış ve doğrulanmış olduğundan yeni ücret onların ücretlerini de
//     karşılamalıdır (BIP125 kural 3 benzeri). Bir nonce yuvası havuzdayken en çok
//     MempoolMaxReplacements kez değiştirilebilir (sayaç yuva havuzdan çıkınca sıfırlanır).
//   - Havuz dolunca en düşük ücretli gönderen kuyruğu (zincirin son işlemi) tahliye edilir;
//     zincirin ortasından işlem çıkarılmaz. İşlemler ML-DSA yüzünden neredeyse eş boyutludur,
//     bu yüzden sıralama mutlak ücretledir.
//   - Uç değiştiğinde (her erişimde denetlenir) havuz yeni uca göre yeniden kurulur: reorg'da
//     ana zincirden düşen blokların işlemleri blok sırasıyla geri alınır, yeni dalda olanlar
//     nonce'u onaylandığı için kendiliğinden düşer.
//
// Kilit sırası: Mempool.saveMu → Mempool.mu → Blockchain.mu. Blockchain hiçbir zaman mempool'u çağırmaz;
// mempool zincir kilidini tutarken zincirin kilit alan dışa açık metotlarını çağırmaz.

// Mempool politikası (kullanıcı kararı 2026-11-09).
const (
	// MempoolExpiryMargin kabulde süresinin dolmasına kalan en az blok sayısıdır. Yalnızca
	// kabulde denetlenir: havuzda kalmak için uç+1'de geçerli olmak yeter, E'ye yakın dahil
	// edilen işlemin reorg payı daha azdır. Alarm derinliğinden bağımsız cüzdan politikasıdır
	// (kullanıcı kararı 2026-11-11).
	MempoolExpiryMargin int64 = 100
	MempoolMaxTxs             = 5000
	MempoolMaxBytes           = 32 << 20
	MempoolMinFee       int64 = 1000 // taban birim (0,00001 QBTC)
	MempoolMaxPerSender       = 25
	// MempoolReplaceBumpPercent aynı nonce'lu işlemin yerini almak için gereken ücret artışıdır.
	MempoolReplaceBumpPercent = 10
	// MempoolMaxReplacements bir nonce yuvasının havuzdayken en çok kaç kez değiştirilebileceğidir
	// (kullanıcı kararı 2026-11-12).
	MempoolMaxReplacements = 10

	maxTxLifetimeSeconds     = 7 * 24 * 60 * 60 // 1 hafta
	defaultTxLifetimeSeconds = 24 * 60 * 60     // 1 gün

	// txFixedOverhead işlem boyutu tahminindeki sabit alanlardır (tutar, ücret, nonce, ...).
	txFixedOverhead = 64
)

var (
	ErrTxKnown       = errors.New("işlem zaten havuzda")
	ErrFeeTooLow     = errors.New("işlem ücreti havuz politikasının altında")
	ErrExpiryTooSoon = errors.New("işlemin süresinin dolmasına çok az blok kaldı")
	ErrExpiryTooFar  = errors.New("işlemin son kullanma yüksekliği çok ileride")
	ErrNonceGap      = errors.New("işlem sırasında boşluk: önceki nonce havuzda değil")
	ErrSenderLimit   = errors.New("gönderenin havuzda çok fazla bekleyen işlemi var")
	ErrReplaceFee    = errors.New("aynı nonce'lu işlemin yerini almak için ücret artışı yetersiz")
	ErrReplaceLimit  = errors.New("bu nonce yuvası havuzda en çok sayıda değiştirildi")
	ErrMempoolFull   = errors.New("havuz dolu ve işlemin ücreti tahliyeye yetmiyor")

	// ErrNotOrphan CheckOrphan'ın, işlemin artık boşlukta olmadığını (Add yeniden denenmeli) söylemesidir.
	ErrNotOrphan = errors.New("işlem artık yetim değil")
)

// MaxTxLifetime son kullanma yüksekliğinin uca göre en çok kaç blok ileride olabileceğidir
// (mempool politikası, 1 hafta).
func (p *Params) MaxTxLifetime() int64 { return maxTxLifetimeSeconds / p.TargetSpacing }

// DefaultTxLifetime cüzdanların varsayılan işlem süresidir (1 gün): MempoolExpiryMargin'den
// çok uzundur, takılan işlem en geç bir günde düşer ve yeniden imzalanabilir.
func (p *Params) DefaultTxLifetime() int64 { return defaultTxLifetimeSeconds / p.TargetSpacing }

type poolTx struct {
	tx   Transaction
	id   [32]byte
	size int
	repl int // bu nonce yuvasının havuzdayken kaç kez değiştirildiği
}

// txSize işlemin havuzdaki bellek payının tahminidir.
func txSize(tx *Transaction) int {
	return len(tx.Sender) + len(tx.Receiver) + len(tx.PublicKey) + len(tx.Signature) + txFixedOverhead
}

// Mempool bkz. dosya belgesi.
type Mempool struct {
	bc *Blockchain

	// Sınırlar: MempoolMaxTxs ve MempoolMaxBytes (testler küçültür).
	maxTxs   int
	maxBytes int

	mu      sync.Mutex
	senders map[string][]*poolTx // gönderen → onaylı nonce + 1'den başlayan bitişik zincir
	ids     map[[32]byte]*poolTx
	count   int
	bytes   int
	tip     string // havuzun en son uzlaştırıldığı uç

	// gen her içerik değişiminde artar; savedGen son kalıcı kaydın gen'idir (mempoolstore.go).
	gen      uint64
	savedGen uint64
	saveMu   sync.Mutex // Save çağrılarını sıralar: eski anlık görüntü yenisinin üzerine yazılmaz
}

// NewMempool bc'nin ucuna göre boş bir havuz kurar.
func NewMempool(bc *Blockchain) *Mempool {
	return &Mempool{bc: bc, maxTxs: MempoolMaxTxs, maxBytes: MempoolMaxBytes,
		senders: map[string][]*poolTx{}, ids: map[[32]byte]*poolTx{}}
}

// Len havuzdaki işlem sayısıdır (uç değiştiyse uzlaştırmadan sonra).
func (m *Mempool) Len() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bc.mu.RLock()
	defer m.bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return 0, err
	}
	return m.count, nil
}

// TxPolicy cüzdanın bir sonraki işlemi imzalamak için gereken değerlerdir.
type TxPolicy struct {
	TipHeight           int64  // ucun yüksekliği
	NextNonce           uint64 // onaylı nonce + havuzdaki bekleyen işlem sayısı + 1
	MinFee              int64
	MinExpiryHeight     int64 // kabulde en erken son kullanma yüksekliği
	DefaultExpiryHeight int64 // uç + DefaultTxLifetime
	MaxExpiryHeight     int64 // uç + MaxTxLifetime
}

// Policy address için işlem politikasını ve sıradaki nonce'u döndürür.
func (m *Mempool) Policy(address string) (TxPolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bc := m.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return TxPolicy{}, err
	}
	n, err := newStateView(bc).nonce(address)
	if err != nil {
		return TxPolicy{}, err
	}
	h := bc.tip.Height
	return TxPolicy{
		TipHeight:           h,
		NextNonce:           n + uint64(len(m.senders[address])) + 1,
		MinFee:              MempoolMinFee,
		MinExpiryHeight:     h + MempoolExpiryMargin,
		DefaultExpiryHeight: h + bc.params.DefaultTxLifetime(),
		MaxExpiryHeight:     h + bc.params.MaxTxLifetime(),
	}, nil
}

// Add işlemi havuz politikası ve ucun durumuna göre denetleyip havuza alır; işlem kimliğini
// döndürür. Denetimler ucuzdan pahalıya sıralıdır, ML-DSA imzası en son doğrulanır.
func (m *Mempool) Add(tx Transaction) (string, error) {
	return m.add(tx, false)
}

// CheckOrphan Add'in ErrNonceGap ile geri çevirdiği bir işlemin, öncülleri gelince kabul
// edilebilecek gibi görünüp görünmediğini söyler (eş işlemlerinin yetim tamponu için; düğüm
// politikası). Add boşluğu imzadan önce bulduğundan işlem henüz doğrulanmamıştır: önce ucuz
// süzgeçler (biçim, ücret, son kullanma ufku, boşluk hâlâ var ve gönderen başına sınırın içinde,
// uç+1'de kullanılabilir bakiye tutar + ücreti karşılıyor), en son ML-DSA imzası denetlenir.
// nil tamponlanabilir demektir; imza ve biçim hataları Add'deki hatalarla aynıdır; ErrNotOrphan
// öncüllerin bu arada geldiğini söyler; diğerleri Add'in politika ve durum hatalarıdır. Bakiye ve boşluk anlık bir
// görüntüdür; kabul her zaman Add'dedir.
func (m *Mempool) CheckOrphan(tx Transaction) error {
	if err := tx.checkFormat(); err != nil {
		return err
	}
	if len(tx.Signature) != crypto.SignatureSize {
		return ErrBadSignature
	}
	if tx.Fee < MempoolMinFee {
		return fmt.Errorf("%w: en az %d taban birim", ErrFeeTooLow, MempoolMinFee)
	}
	params, err := m.checkOrphanState(&tx)
	if err != nil {
		return err
	}
	return tx.verifySignature(params) // kilit dışında: pahalı
}

func (m *Mempool) checkOrphanState(tx *Transaction) (*Params, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bc := m.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return nil, err
	}
	if _, ok := m.ids[tx.IDBytes()]; ok {
		return nil, ErrTxKnown
	}
	remaining := tx.ExpiryHeight - bc.tip.Height
	if remaining < MempoolExpiryMargin {
		return nil, fmt.Errorf("%w: %d blok kaldı, en az %d gerekli", ErrExpiryTooSoon, remaining, MempoolExpiryMargin)
	}
	if max := bc.params.MaxTxLifetime(); remaining > max {
		return nil, fmt.Errorf("%w: uçtan %d blok ileride, en çok %d", ErrExpiryTooFar, remaining, max)
	}
	matured, err := m.maturedLocked()
	if err != nil {
		return nil, err
	}
	confirmed, err := matured.nonce(tx.Sender)
	if err != nil {
		return nil, err
	}
	if tx.Nonce <= confirmed {
		return nil, fmt.Errorf("%w: nonce %d zaten onaylandı", ErrBadNonce, tx.Nonce)
	}
	next := confirmed + 1 + uint64(len(m.senders[tx.Sender]))
	if tx.Nonce <= next {
		return nil, ErrNotOrphan // öncüller bu arada geldi: Add yeniden denenmeli
	}
	if tx.Nonce-confirmed > MempoolMaxPerSender {
		return nil, fmt.Errorf("%w: nonce %d gönderen sınırının ötesinde", ErrNonceGap, tx.Nonce)
	}
	bal, err := matured.balance(tx.Sender)
	if err != nil {
		return nil, err
	}
	if bal < tx.Fee || bal-tx.Fee < tx.Amount {
		return nil, fmt.Errorf("%w: bakiye %d", ErrInsufficientBal, bal)
	}
	return bc.params, nil
}

// add Add'in gövdesidir. restoring true ise (kalıcı kayıttan yükleme) yalnızca
// MempoolExpiryMargin denetimi atlanır: marj kabulde uygulanır, havuzda kalmak için uç+1'de
// geçerli olmak yeter (dosya belgesi). Diğer bütün denetimler ve imza doğrulaması aynen yapılır.
func (m *Mempool) add(tx Transaction, restoring bool) (string, error) {
	if err := tx.checkFormat(); err != nil {
		return "", err
	}
	if len(tx.Signature) != crypto.SignatureSize {
		return "", ErrBadSignature
	}
	if tx.Fee < MempoolMinFee {
		return "", fmt.Errorf("%w: en az %d taban birim", ErrFeeTooLow, MempoolMinFee)
	}
	p := &poolTx{tx: tx, id: tx.IDBytes(), size: txSize(&tx)}

	m.mu.Lock()
	defer m.mu.Unlock()
	bc := m.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return "", err
	}
	if _, ok := m.ids[p.id]; ok {
		return "", ErrTxKnown
	}

	remaining := tx.ExpiryHeight - bc.tip.Height // dahil edilebileceği blok sayısı
	if !restoring && remaining < MempoolExpiryMargin {
		return "", fmt.Errorf("%w: %d blok kaldı, en az %d gerekli", ErrExpiryTooSoon, remaining, MempoolExpiryMargin)
	}
	if max := bc.params.MaxTxLifetime(); remaining > max {
		return "", fmt.Errorf("%w: uçtan %d blok ileride, en çok %d", ErrExpiryTooFar, remaining, max)
	}

	confirmed, err := newStateView(bc).nonce(tx.Sender)
	if err != nil {
		return "", err
	}
	if tx.Nonce <= confirmed {
		return "", fmt.Errorf("%w: nonce %d zaten onaylandı", ErrBadNonce, tx.Nonce)
	}
	chain := m.senders[tx.Sender]
	idx := tx.Nonce - confirmed - 1
	if idx > uint64(len(chain)) {
		return "", fmt.Errorf("%w: beklenen en çok %d, gelen %d", ErrNonceGap, confirmed+1+uint64(len(chain)), tx.Nonce)
	}
	replacing := idx < uint64(len(chain))
	if !replacing && len(chain) >= MempoolMaxPerSender {
		return "", ErrSenderLimit
	}
	if replacing {
		old := chain[idx]
		if old.repl >= MempoolMaxReplacements {
			return "", fmt.Errorf("%w: %d", ErrReplaceLimit, MempoolMaxReplacements)
		}
		if need, ok := replaceFee(old.tx.Fee); !ok || tx.Fee < need {
			return "", fmt.Errorf("%w: eski ücret %d", ErrReplaceFee, old.tx.Fee)
		}
		p.repl = old.repl + 1
	}

	candidate := append(append([]*poolTx{}, chain[:idx]...), p)
	if replacing {
		candidate = append(candidate, chain[idx+1:]...)
	}
	matured, err := m.maturedLocked()
	if err != nil {
		return "", err
	}
	kept, stopErr, err := m.validChainLocked(tx.Sender, candidate, matured)
	if err != nil {
		return "", err
	}
	if uint64(len(kept)) <= idx {
		if stopErr == nil {
			stopErr = ErrBadNonce
		}
		return "", stopErr
	}
	if replacing {
		// Düşen sonraki işlemler (candidate[len(kept):], hepsi chain[idx+1:]'den) ağda bedelsiz
		// yayılmış olmasın: yeni ücret onların ücretlerini de karşılamalı.
		need, ok := replaceFee(chain[idx].tx.Fee)
		for _, q := range candidate[len(kept):] {
			if !ok || need > math.MaxInt64-q.tx.Fee {
				ok = false
				break
			}
			need += q.tx.Fee
		}
		if !ok || tx.Fee < need {
			return "", fmt.Errorf("%w: eski ücret %d ve düşecek %d işlemin ücretleri, gereken %d",
				ErrReplaceFee, chain[idx].tx.Fee, len(candidate)-len(kept), need)
		}
	}

	addBytes := 0
	for _, q := range kept {
		addBytes += q.size
	}
	for _, q := range chain {
		addBytes -= q.size
	}
	cuts, ok := m.evictionPlanLocked(m.count+len(kept)-len(chain), m.bytes+addBytes, tx.Sender, tx.Fee)
	if !ok {
		return "", ErrMempoolFull
	}

	if err := tx.verifySignature(bc.params); err != nil {
		return "", err
	}

	for s, n := range cuts {
		m.truncateLocked(s, n)
	}
	m.setChainLocked(tx.Sender, kept)
	return hex.EncodeToString(p.id[:]), nil
}

// replaceFee eski ücretin yerine geçmek için gereken en düşük ücrettir: en az %10 ve en az
// MempoolMinFee artış. Taşarsa yer değiştirme mümkün değildir.
func replaceFee(old int64) (int64, bool) {
	bump := old / 100 * MempoolReplaceBumpPercent
	if r := old % 100 * MempoolReplaceBumpPercent; r > 0 {
		bump += (r + 99) / 100 // yukarı yuvarla
	}
	if bump < MempoolMinFee {
		bump = MempoolMinFee
	}
	if old > math.MaxInt64-bump {
		return 0, false
	}
	return old + bump, true
}

// Select şablon için en çok max işlemi döndürür: gönderen başına nonce sırasıyla, zincir
// başları arasında ücreti yüksek olan önce. NewTemplate işlemleri verilen sırayla işlediğinden
// başka bir sıra aynı gönderenin sonraki işlemlerini düşürürdü.
func (m *Mempool) Select(max int) ([]Transaction, error) {
	ps, err := m.ordered(max)
	if err != nil {
		return nil, err
	}
	out := make([]Transaction, len(ps))
	for i, p := range ps {
		out[i] = p.tx
	}
	return out, nil
}

// Inventory havuzdaki en çok max işlemin kimliğini Select sırasıyla döndürür: bu sırayla
// getiren eş her göndericinin nonce'larını boşluksuz alır (eşlerle havuz eşitlemesi).
func (m *Mempool) Inventory(max int) ([]string, error) {
	ps, err := m.ordered(max)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = hex.EncodeToString(p.id[:])
	}
	return out, nil
}

// Missing ids içinden havuzda olmayanları sırayı koruyarak döndürür; geçersiz ve yinelenen
// kimlikler atlanır.
func (m *Mempool) Missing(ids []string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	seen := map[[32]byte]bool{}
	for _, s := range ids {
		id, ok := decodeHash(s)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		if _, in := m.ids[id]; !in {
			out = append(out, s)
		}
	}
	return out
}

// Get havuzdaki işlemlerden kimliği ids içinde olanları ids sırasıyla döndürür (bulunmayan ve
// geçersiz kimlikler atlanır). Uç değişimine göre uzlaştırma yapılmaz: dönen işlemler alıcı
// tarafından yeniden doğrulanır.
func (m *Mempool) Get(ids []string) []Transaction {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Transaction
	for _, s := range ids {
		id, ok := decodeHash(s)
		if !ok {
			continue
		}
		if p, in := m.ids[id]; in {
			out = append(out, p.tx)
		}
	}
	return out
}

// ordered havuzu uca göre uzlaştırıp en çok max işlemi Select sırasıyla döndürür.
func (m *Mempool) ordered(max int) ([]*poolTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bc.mu.RLock()
	defer m.bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return nil, err
	}
	return m.orderedLocked(max), nil
}

// orderedLocked uzlaştırılmış havuzdan en çok max işlemi Select sırasıyla döndürür; m.mu ve
// bc.mu (okuma) tutulmalıdır.
func (m *Mempool) orderedLocked(max int) []*poolTx {
	h := &headHeap{}
	for _, chain := range m.senders {
		*h = append(*h, head{chain: chain})
	}
	heap.Init(h)
	var out []*poolTx
	for h.Len() > 0 && len(out) < max {
		top := &(*h)[0]
		out = append(out, top.chain[top.pos])
		top.pos++
		if top.pos == len(top.chain) {
			heap.Pop(h)
		} else {
			heap.Fix(h, 0)
		}
	}
	return out
}

type head struct {
	chain []*poolTx
	pos   int
}

type headHeap []head

func (h headHeap) Len() int { return len(h) }
func (h headHeap) Less(i, j int) bool {
	a, b := h[i].chain[h[i].pos], h[j].chain[h[j].pos]
	if a.tx.Fee != b.tx.Fee {
		return a.tx.Fee > b.tx.Fee
	}
	return bytes.Compare(a.id[:], b.id[:]) < 0
}
func (h headHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *headHeap) Push(x interface{}) { *h = append(*h, x.(head)) }
func (h *headHeap) Pop() interface{} {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// --- Aşağıdaki fonksiyonlar m.mu ve bc.mu (en az okuma) tutulurken çağrılır. ---

// syncLocked uç değiştiyse havuzu yeni uca göre yeniden kurar.
func (m *Mempool) syncLocked() error {
	bc := m.bc
	if bc.tip.Hash == m.tip {
		return nil
	}
	cands := m.disconnectedLocked()
	for _, chain := range m.senders {
		cands = append(cands, chain...)
	}
	matured, err := m.maturedLocked()
	if err != nil {
		return err
	}
	bySender := map[string][]*poolTx{}
	seen := map[[32]byte]bool{}
	for _, p := range cands {
		if seen[p.id] {
			continue
		}
		seen[p.id] = true
		bySender[p.tx.Sender] = append(bySender[p.tx.Sender], p)
	}
	// Okuma hatasında havuz eski haline döner (m.tip değişmez, sonraki erişim yeniden dener).
	oldSenders, oldIDs, oldCount, oldBytes := m.senders, m.ids, m.count, m.bytes
	m.senders, m.ids, m.count, m.bytes = map[string][]*poolTx{}, map[[32]byte]*poolTx{}, 0, 0
	for sender, list := range bySender {
		sort.SliceStable(list, func(i, j int) bool { return list[i].tx.Nonce < list[j].tx.Nonce })
		kept, _, err := m.validChainLocked(sender, list, matured)
		if err != nil {
			m.senders, m.ids, m.count, m.bytes = oldSenders, oldIDs, oldCount, oldBytes
			return err
		}
		m.setChainLocked(sender, kept)
	}
	cuts, _ := m.evictionPlanLocked(m.count, m.bytes, "", math.MaxInt64)
	for s, n := range cuts {
		m.truncateLocked(s, n)
	}
	m.tip = bc.tip.Hash
	return nil
}

// disconnectedLocked havuzun son uzlaştığı uçtan ana zincire kadar olan (artık ana zincirde
// olmayan) blokların işlemlerini blok sırasıyla döndürür. Reorg derinliği sınırsız olduğundan
// en çok MaxSideForkDepth blok ve havuz sınırı kadar işlem okunur (eski nonce'lar önce: sonrakiler
// onlarsız geçersizdir). Gövdesi okunamayan blok atlanır.
func (m *Mempool) disconnectedLocked() []*poolTx {
	bc := m.bc
	var hashes []string
	for e, ok := bc.index[m.tip]; ok && !bc.isMain(e); e, ok = bc.index[e.Prev] {
		hashes = append(hashes, e.Hash)
	}
	var out []*poolTx
	for i, n := len(hashes)-1, 0; i >= 0 && n < MaxSideForkDepth && len(out) < m.maxTxs; i, n = i-1, n+1 {
		b, err := bc.getBlock(hashes[i])
		if err != nil {
			bc.log.Warn("reorg: düşen bloğun işlemleri havuza alınamadı", "hash", hashes[i], "err", err)
			continue
		}
		for _, tx := range b.Transactions {
			if !tx.IsCoinbase {
				out = append(out, &poolTx{tx: tx, id: tx.IDBytes(), size: txSize(&tx)})
			}
		}
	}
	return out
}

// maturedLocked uç+1 bağlanırken olgunlaşan coinbase'i içeren bir görünümdür (doğrulayıcıyla
// aynı); yalnızca o alacak okunur.
func (m *Mempool) maturedLocked() (*stateView, error) {
	v := newStateView(m.bc)
	if err := m.bc.creditMaturedLocked(v, m.bc.tip.Height+1, m.bc.tip.Hash); err != nil {
		return nil, err
	}
	return v, nil
}

// validChainLocked list'teki (nonce sıralı) işlemlerden onaylı nonce + 1'den başlayan bitişik,
// uç+1'de geçerli en uzun öneki döndürür. Durma nedeni (ör. yetersiz bakiye) ikinci değerdir;
// üçüncü değer yalnızca okuma hatasıdır. Onaylı ya da yinelenen nonce'lar atlanır.
func (m *Mempool) validChainLocked(sender string, list []*poolTx, matured *stateView) ([]*poolTx, error, error) {
	height := m.bc.tip.Height + 1
	v := newStateView(m.bc)
	if b, ok := matured.balances[sender]; ok {
		v.balances[sender] = b
	}
	n, err := v.nonce(sender)
	if err != nil {
		return nil, nil, err
	}
	next := n + 1
	var out []*poolTx
	for _, p := range list {
		if p.tx.Nonce < next {
			continue
		}
		if p.tx.Nonce > next {
			return out, ErrNonceGap, nil
		}
		if len(out) == MempoolMaxPerSender {
			return out, ErrSenderLimit, nil
		}
		if err := p.tx.CheckExpiry(height); err != nil {
			return out, err, nil
		}
		if err := v.apply(&p.tx); err != nil {
			if isPermanentStatefulErr(err) {
				return out, err, nil
			}
			return nil, nil, err
		}
		out = append(out, p)
		next++
	}
	return out, nil, nil
}

// evictionPlanLocked havuz count işlem ve size bayta çıkacaksa sınırlara inmek için gönderen
// başına kalacak zincir uzunluğunu döndürür: her adımda en düşük ücretli kuyruk (exclude
// dışındaki göndericilerden) çıkar. Kuyruğun ücreti fee'den düşük değilse plan başarısızdır.
// Kuyruklar bir yığında tutulur: k tahliye O(gönderen + k log gönderen) sürer (reorg sonrası
// yeniden kurmada havuz sınırın iki katına çıkabilir).
func (m *Mempool) evictionPlanLocked(count, size int, exclude string, fee int64) (map[string]int, bool) {
	cuts := map[string]int{}
	if count <= m.maxTxs && size <= m.maxBytes {
		return cuts, true
	}
	h := &tailHeap{}
	for s, chain := range m.senders {
		if s != exclude && len(chain) > 0 {
			*h = append(*h, tail{sender: s, chain: chain, n: len(chain)})
		}
	}
	heap.Init(h)
	for count > m.maxTxs || size > m.maxBytes {
		if h.Len() == 0 {
			return nil, false
		}
		top := &(*h)[0]
		vt := top.chain[top.n-1]
		if vt.tx.Fee >= fee {
			return nil, false
		}
		top.n--
		cuts[top.sender] = top.n
		count--
		size -= vt.size
		if top.n == 0 {
			heap.Pop(h)
		} else {
			heap.Fix(h, 0)
		}
	}
	return cuts, true
}

// tail bir göndericinin tahliye planındaki kalan zincir uzunluğudur.
type tail struct {
	sender string
	chain  []*poolTx
	n      int
}

// tailHeap en düşük ücretli kuyruğu (eşitlikte büyük kimlik) tepede tutar.
type tailHeap []tail

func (h tailHeap) Len() int { return len(h) }
func (h tailHeap) Less(i, j int) bool {
	a, b := h[i].chain[h[i].n-1], h[j].chain[h[j].n-1]
	if a.tx.Fee != b.tx.Fee {
		return a.tx.Fee < b.tx.Fee
	}
	return bytes.Compare(a.id[:], b.id[:]) > 0
}
func (h tailHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *tailHeap) Push(x interface{}) { *h = append(*h, x.(tail)) }
func (h *tailHeap) Pop() interface{} {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// truncateLocked gönderenin zincirini ilk n işleme kısaltır.
func (m *Mempool) truncateLocked(sender string, n int) {
	m.gen++
	chain := m.senders[sender]
	for _, p := range chain[n:] {
		delete(m.ids, p.id)
		m.count--
		m.bytes -= p.size
	}
	if n == 0 {
		delete(m.senders, sender)
		return
	}
	m.senders[sender] = chain[:n:n]
}

// setChainLocked gönderenin zincirini kept ile değiştirir.
func (m *Mempool) setChainLocked(sender string, kept []*poolTx) {
	m.truncateLocked(sender, 0) // gen'i artırır
	if len(kept) == 0 {
		return
	}
	for _, p := range kept {
		m.ids[p.id] = p
		m.count++
		m.bytes += p.size
	}
	m.senders[sender] = kept
}
