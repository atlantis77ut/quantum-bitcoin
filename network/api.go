package network

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// HTTP sunucu sınırları.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	maxHeaderBytes    = 8 << 10

	// İstemci IP'si başına oran sınırı: saniyede rateLimitPerSec istek, en fazla rateLimitBurst ani.
	rateLimitPerSec = 10
	rateLimitBurst  = 20

	// maxTxBodyBytes POST /tx gövde sınırıdır: ML-DSA-65 işlemi JSON'da ~7,5 KiB.
	maxTxBodyBytes = 16 << 10

	// /headers sayfa boyutu: varsayılan ve en çok (500 başlık ~165 KB; oran sınırı istek
	// sayar, bayt saymaz, yanıt boyutunu bu sınır belirler).
	defaultHeadersCount = 100
	maxHeadersCount     = 500

	// /block işlem sayfası: varsayılan ve en çok (ML-DSA-65 işlemi JSON'da ~7,5 KiB;
	// 200 işlem ~1,5 MiB).
	defaultBlockTxLimit = 50
	maxBlockTxLimit     = 200
)

type APIServer struct {
	Blockchain *core.Blockchain
	ListenAddr string
	TLSCert    string // Boş değilse HTTPS ile dinler
	TLSKey     string
	Mempool    *core.Mempool // nil ise işlem kabulü kapalıdır (503)

	// OnTxAccepted havuza yeni kabul edilen işlem için çağrılır (eşlere yayın); nil olabilir.
	OnTxAccepted func(core.Transaction)

	server  *http.Server
	limiter *ipRateLimiter
	log     *slog.Logger
}

func NewAPIServer(listenAddr string, bc *core.Blockchain) *APIServer {
	api := &APIServer{
		Blockchain: bc,
		ListenAddr: listenAddr,
		limiter:    newIPRateLimiter(rateLimitPerSec, rateLimitBurst),
		log:        slog.Default().With("component", "api"),
	}
	api.server = &http.Server{
		Addr:              listenAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	return api
}

// Handler kendi ServeMux'ı ile rotaları döndürür (http.DefaultServeMux kullanılmaz).
func (api *APIServer) Handler() http.Handler {
	mux := http.NewServeMux()
	// Bakiye sorgulama (borsalar kullanıcı bakiyelerini bununla doğrular)
	// Kullanım: GET /balance?address=QBTC...
	mux.HandleFunc("/balance", getOnly(api.handleGetBalance))
	// Son blok, işlemleriyle birlikte (en fazla ~7 MiB; uyumluluk için korunur). Zinciri izlemek
	// için /header ve /headers kullanılmalıdır.
	// Kullanım: GET /latestblock
	mux.HandleFunc("/latestblock", getOnly(api.handleGetLatestBlock))
	// Ana zincirdeki bir bloğun başlığı; hash verilmezse uç. Ana zincirde olmayan özet 404.
	// Kullanım: GET /header[?hash=<64 hex blok özeti>]
	mux.HandleFunc("/header", getOnly(api.handleHeader))
	// Ana zincir başlıkları, yükseklik sırasıyla sayfalı.
	// Kullanım: GET /headers?from=<yükseklik>[&count=<1..500, varsayılan 100>]
	mux.HandleFunc("/headers", getOnly(api.handleHeaders))
	// Ana zincirdeki bir bloğun başlığı ve işlemleri, sayfalı; txid verilirse yalnızca o işlem.
	// Kullanım: GET /block?hash=<64 hex> | ?height=<yükseklik>
	//           [&offset=<0..>][&limit=<1..200, varsayılan 50>] | [&txid=<64 hex>]
	mux.HandleFunc("/block", getOnly(api.handleBlock))
	// Onay sayısı (borsalar yatırımları bununla onaylar). deep_reorg_alarm true ise onaylara
	// güvenilmemeli ve operatöre danışılmalıdır.
	// Kullanım: GET /confirmations?hash=<64 hex blok özeti>
	mux.HandleFunc("/confirmations", getOnly(api.handleConfirmations))
	// İmzalı işlem gönderimi (mempool politikası: core/mempool.go).
	// Kullanım: POST /tx, gövde core.Transaction JSON'u; yanıt {"txid": ...}
	mux.HandleFunc("/tx", postOnly(api.handleSubmitTx))
	// Cüzdanın işlemi imzalamak için gereken değerler: sıradaki nonce, en düşük ücret ve
	// son kullanma yüksekliği aralığı (varsayılan 1 gün).
	// Kullanım: GET /txpolicy?address=QBTC...
	mux.HandleFunc("/txpolicy", getOnly(api.handleTxPolicy))
	return api.withRateLimit(mux)
}

// getOnly yalnızca GET/HEAD isteklerine izin verir. "GET /yol" kalıpları yerine elle
// kontrol: davranış go.mod'daki go sürümüne / GODEBUG httpmuxgo121 ayarına bağlı kalmaz.
func getOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "yalnızca GET"})
			return
		}
		h(w, r)
	}
}

// postOnly yalnızca POST isteklerine izin verir.
func postOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "yalnızca POST"})
			return
		}
		h(w, r)
	}
}

// Start API sunucusunu başlatır; Shutdown çağrılana kadar bloklar.
func (api *APIServer) Start() error {
	api.log.Info("HTTP sunucusu başlatılıyor", "addr", api.ListenAddr, "tls", api.TLSCert != "")
	var err error
	if api.TLSCert != "" {
		err = api.server.ListenAndServeTLS(api.TLSCert, api.TLSKey)
	} else {
		err = api.server.ListenAndServe()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (api *APIServer) Shutdown(ctx context.Context) error {
	return api.server.Shutdown(ctx)
}

func (api *APIServer) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	address := r.URL.Query().Get("address")
	if !crypto.IsValidAddress(address) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz veya eksik address parametresi"})
		return
	}

	balance, err := api.Blockchain.GetBalance(address)
	if err != nil {
		api.log.Error("bakiye okunamadı", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iç hata"})
		return
	}
	// balance taban birim cinsindendir (1 QBTC = 10^decimals); balance_qbtc gösterim içindir.
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"address":      address,
		"balance":      balance,
		"balance_qbtc": core.FormatAmount(balance),
		"decimals":     core.AmountDecimals,
	})
}

func (api *APIServer) handleGetLatestBlock(w http.ResponseWriter, r *http.Request) {
	latestBlock, err := api.Blockchain.GetLatestBlock()
	if err != nil {
		api.log.Error("son blok okunamadı", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iç hata"})
		return
	}
	writeJSON(w, http.StatusOK, latestBlock)
}

// handleHeader ana zincirdeki bir başlığı (hash yoksa ucu) döndürür.
func (api *APIServer) handleHeader(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if hash == "" {
		writeJSON(w, http.StatusOK, api.Blockchain.TipHeader())
		return
	}
	if !core.IsHashHex(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz hash parametresi"})
		return
	}
	h, ok := api.Blockchain.HeaderByHash(hash)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "blok ana zincirde değil"})
		return
	}
	writeJSON(w, http.StatusOK, h)
}

// handleHeaders from yüksekliğinden başlayan ana zincir başlıklarını döndürür. Sayfa ve uç
// aynı anlık görüntüden gelir; sayfalar arasında reorg olabilir: istemci her sayfanın ilk
// başlığının prev_hash'ini önceki sayfanın son hash'iyle karşılaştırmalı, tutmazsa geri
// sarıp yeniden istemelidir. from uçtan büyükse liste boştur.
func (api *APIServer) handleHeaders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := strconv.ParseInt(q.Get("from"), 10, 64)
	if err != nil || from < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz veya eksik from parametresi"})
		return
	}
	count := defaultHeadersCount
	if s := q.Get("count"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxHeadersCount {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "count 1 ile 500 arasında olmalı"})
			return
		}
		count = n
	}
	hdrs, tipHeight, tipHash := api.Blockchain.HeadersRange(from, count)
	if hdrs == nil {
		hdrs = []core.BlockHeader{} // JSON'da null değil []
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tip_height": tipHeight,
		"tip_hash":   tipHash,
		"headers":    hdrs,
	})
}

// blockTx bir bloktaki işlemin kimliği ve blok içi sırasıyla birlikte gösterimidir.
type blockTx struct {
	TxID  string `json:"txid"`
	Index int    `json:"index"`
	core.Transaction
}

// handleBlock ana zincirdeki bir bloğun başlığını ve işlemlerinin bir sayfasını döndürür.
// Blok hash ya da height ile (ikisi birden değil) seçilir. confirmations blokla aynı anlık
// görüntüdendir; sayfalar arasında reorg olabilir: istemci her sayfada header.hash'in
// değişmediğini denetlemeli (height ile sorguda özellikle) ve yatırım onayı için
// /confirmations kullanmalıdır. offset işlem sayısından büyükse liste boştur. txid verilirse
// yalnızca o işlem döner (blokta yoksa 404). Blok her istekte diskten okunur.
func (api *APIServer) handleBlock(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	hash, heightStr := q.Get("hash"), q.Get("height")
	if (hash == "") == (heightStr == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash ya da height parametrelerinden tam biri verilmeli"})
		return
	}
	offset, limit := 0, defaultBlockTxLimit
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz offset parametresi"})
			return
		}
		offset = n
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxBlockTxLimit {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit 1 ile 200 arasında olmalı"})
			return
		}
		limit = n
	}
	txid := q.Get("txid")
	if txid != "" && !core.IsHashHex(txid) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz txid parametresi"})
		return
	}
	if txid != "" && (q.Get("offset") != "" || q.Get("limit") != "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "txid, offset/limit ile birlikte verilemez"})
		return
	}

	var b *core.Block
	var confirmations int64
	var err error
	if hash != "" {
		if !core.IsHashHex(hash) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz hash parametresi"})
			return
		}
		b, confirmations, err = api.Blockchain.MainBlockByHash(hash)
	} else {
		height, perr := strconv.ParseInt(heightStr, 10, 64)
		if perr != nil || height < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz height parametresi"})
			return
		}
		b, confirmations, err = api.Blockchain.MainBlockByHeight(height)
	}
	if errors.Is(err, core.ErrUnknownBlock) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "blok ana zincirde değil"})
		return
	}
	if err != nil {
		api.log.Error("blok okunamadı", "hash", hash, "height", heightStr, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iç hata"})
		return
	}

	txs := []blockTx{} // JSON'da null değil []
	if txid != "" {
		for i := range b.Transactions {
			if id := b.Transactions[i].ID(); id == txid {
				txs = append(txs, blockTx{TxID: id, Index: i, Transaction: b.Transactions[i]})
				break
			}
		}
		if len(txs) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "işlem bu blokta değil"})
			return
		}
		offset = txs[0].Index
	} else {
		for i := offset; i < len(b.Transactions) && len(txs) < limit; i++ {
			txs = append(txs, blockTx{TxID: b.Transactions[i].ID(), Index: i, Transaction: b.Transactions[i]})
		}
	}
	header := core.BlockHeader{Version: b.Version, Index: b.Index, Timestamp: b.Timestamp, PrevHash: b.PrevHash,
		MerkleRoot: b.MerkleRoot, Bits: b.Bits, Nonce: b.Nonce, Hash: b.Hash}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"header":        header,
		"confirmations": confirmations,
		"tx_count":      len(b.Transactions),
		"offset":        offset,
		"transactions":  txs,
	})
}

// handleConfirmations blok sayısının yanında iş ve zamana dayalı onay bilgisini döndürür
// (bkz. core/confirm.go): entegrasyonlar confirmations, elapsed_seconds ve work eşiklerinin
// üçünü birlikte kullanmalı; deep_reorg_alarm ya da below_min_chain_work true ise hiçbir
// onaya güvenilmez.
func (api *APIServer) handleConfirmations(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("hash")
	if !core.IsHashHex(hash) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz veya eksik hash parametresi"})
		return
	}
	c, err := api.Blockchain.ConfirmationOf(hash)
	if err != nil {
		api.log.Error("onay bilgisi okunamadı", "hash", hash, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iç hata"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hash":                 hash,
		"confirmations":        c.Blocks,
		"elapsed_seconds":      c.ElapsedSeconds,
		"work":                 c.Work.String(), // ondalık; JSON sayısı 2^53'ü aşabilir
		"work_in_start_blocks": api.Blockchain.WorkInBlocks(c.Work),
		"deep_reorg_alarm":     c.DeepReorg,
		"below_min_chain_work": c.BelowMinWork,
	})
}

// handleSubmitTx imzalı bir işlemi havuza alır. Düğüm senkronize değilken (en az zincir
// işinin altında) durum eski olduğundan işlem kabul edilmez.
func (api *APIServer) handleSubmitTx(w http.ResponseWriter, r *http.Request) {
	if api.Mempool == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "işlem kabulü kapalı"})
		return
	}
	if api.Blockchain.BelowMinChainWork() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "düğüm senkronize değil"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var tx core.Transaction
	if err := dec.Decode(&tx); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz işlem gövdesi"})
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "gövdede tek işlem olmalı"})
		return
	}
	id, err := api.Mempool.Add(tx)
	if err != nil {
		status := txRejectStatus(err)
		if status == http.StatusInternalServerError {
			api.log.Error("işlem havuza alınamadı", "err", err)
			writeJSON(w, status, map[string]string{"error": "iç hata"})
			return
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if api.OnTxAccepted != nil {
		api.OnTxAccepted(tx)
	}
	writeJSON(w, http.StatusOK, map[string]string{"txid": id})
}

// txRejectStatus havuz retlerini HTTP durumuna eşler: geçersiz işlem 400, sıra çakışması ya da
// yineleme 409, dolu havuz 503; bilinmeyen hata (ör. okuma hatası) 500 olur ve ayrıntısı
// istemciye gönderilmez.
func txRejectStatus(err error) int {
	for _, e := range []error{core.ErrTxKnown, core.ErrBadNonce, core.ErrNonceGap, core.ErrReplaceFee, core.ErrReplaceLimit, core.ErrSenderLimit} {
		if errors.Is(err, e) {
			return http.StatusConflict
		}
	}
	for _, e := range []error{core.ErrUnexpectedCoinbase, core.ErrNonPositiveAmount, core.ErrNegativeFee,
		core.ErrBadExpiry, core.ErrBadReceiver, core.ErrBadSender, core.ErrSelfTransfer, core.ErrBadSignature,
		core.ErrFeeTooLow, core.ErrExpiryTooSoon, core.ErrExpiryTooFar, core.ErrTxExpired,
		core.ErrInsufficientBal, core.ErrOverflow} {
		if errors.Is(err, e) {
			return http.StatusBadRequest
		}
	}
	if errors.Is(err, core.ErrMempoolFull) {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// handleTxPolicy bir adresin sıradaki nonce'unu (havuzdaki bekleyenler dahil) ve işlem
// politikasını döndürür.
func (api *APIServer) handleTxPolicy(w http.ResponseWriter, r *http.Request) {
	if api.Mempool == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "işlem kabulü kapalı"})
		return
	}
	address := r.URL.Query().Get("address")
	if !crypto.IsValidAddress(address) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "geçersiz veya eksik address parametresi"})
		return
	}
	p, err := api.Mempool.Policy(address)
	if err != nil {
		api.log.Error("işlem politikası okunamadı", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "iç hata"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"address":               address,
		"tip_height":            p.TipHeight,
		"next_nonce":            p.NextNonce,
		"min_fee":               p.MinFee,
		"min_expiry_height":     p.MinExpiryHeight,
		"default_expiry_height": p.DefaultExpiryHeight,
		"max_expiry_height":     p.MaxExpiryHeight,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // İstemci bağlantıyı kopardıysa yapılacak bir şey yok
}

func (api *APIServer) withRateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !api.limiter.allowHost(ip) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "çok fazla istek"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientKey IPv4'te tam adresi, IPv6'da /64 önekini döndürür; bir saldırgan kendi /64
// bloğundaki milyarlarca adresle sınırları aşamasın.
func clientKey(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// prefix48 IPv6 adresinin /48 önekidir (bir son kullanıcı ya da kurumun tipik tahsisi);
// IPv4 ve çözülemeyen adreslerde boştur.
func prefix48(host string) string {
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return ""
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

const (
	// maxBuckets oran sınırlayıcının tutacağı en fazla istemci kaydıdır (bellek tavanı).
	maxBuckets = 100_000

	// maxKeysPerPrefix bir IPv6 /48 önekinin alabileceği en fazla ayrı /64 kaydıdır. Fazlası
	// önekin ortak kovasını paylaşır: tek bir /48 (65.536 adet /64) tabloyu dolduramaz.
	maxKeysPerPrefix = 16

	// overflowFactor tablo doluyken yeni istemcilerin paylaştığı taşma kovasının, tek bir
	// istemcinin hızına oranıdır.
	overflowFactor = 10
)

// ipRateLimiter IP başına basit bir token bucket'tır (harici bağımlılık yok). Tablo
// dolduğunda yeni istemciler reddedilmez, ortak bir taşma kovasını paylaşır: tabloyu
// dolduran saldırgan mevcut istemcileri etkilemez, yeni istemcileri yalnızca yavaşlatır.
type ipRateLimiter struct {
	mu        sync.Mutex
	rate      float64
	burst     float64
	maxKeys   int
	perPrefix int
	buckets   map[string]*bucket
	prefixes  map[string]int // /48 öneki başına ayrı /64 kaydı sayısı
	overflow  bucket
	now       func() time.Time
	lastGC    time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
	prefix string // kaydı sayan /48 öneki (yoksa boş)
}

func newIPRateLimiter(perSec, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		rate:      float64(perSec),
		burst:     float64(burst),
		maxKeys:   maxBuckets,
		perPrefix: maxKeysPerPrefix,
		buckets:   map[string]*bucket{},
		prefixes:  map[string]int{},
		now:       time.Now,
	}
}

// allowHost istemci adresine göre bir isteğe izin verir.
func (l *ipRateLimiter) allowHost(host string) bool {
	return l.allow(clientKey(host), prefix48(host))
}

func (l *ipRateLimiter) allow(key, prefix string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Bellek şişmesini önlemek için boşta kalan kovaları ara ara temizle.
	if now.Sub(l.lastGC) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 5*time.Minute {
				l.remove(k, b)
			}
		}
		l.lastGC = now
	}

	b, ok := l.buckets[key]
	if !ok && prefix != "" && l.prefixes[prefix] >= l.perPrefix {
		key = prefix // önekin kota dışı /64'leri önekin ortak kovasını paylaşır
		b, ok = l.buckets[key]
		prefix = ""
	}
	if !ok {
		if len(l.buckets) >= l.maxKeys {
			return l.take(&l.overflow, now, overflowFactor)
		}
		b = &bucket{tokens: l.burst, last: now, prefix: prefix}
		l.buckets[key] = b
		if prefix != "" {
			l.prefixes[prefix]++
		}
	}
	return l.take(b, now, 1)
}

func (l *ipRateLimiter) remove(key string, b *bucket) {
	delete(l.buckets, key)
	if b.prefix == "" {
		return
	}
	if l.prefixes[b.prefix] <= 1 {
		delete(l.prefixes, b.prefix)
	} else {
		l.prefixes[b.prefix]--
	}
}

// take kovayı factor katı hızla doldurup bir jeton harcar.
func (l *ipRateLimiter) take(b *bucket, now time.Time, factor float64) bool {
	burst := l.burst * factor
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens += now.Sub(b.last).Seconds() * l.rate * factor
	}
	if b.tokens > burst {
		b.tokens = burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
