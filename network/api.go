package network

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
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
)

type APIServer struct {
	Blockchain *core.Blockchain
	ListenAddr string
	TLSCert    string // Boş değilse HTTPS ile dinler
	TLSKey     string

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
	// Son blok (borsalar ağı senkronize etmek için)
	// Kullanım: GET /latestblock
	mux.HandleFunc("/latestblock", getOnly(api.handleGetLatestBlock))
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"address": address,
		"balance": balance,
		"unit":    "QBTC",
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
		if !api.limiter.allow(clientKey(ip)) {
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

// maxBuckets oran sınırlayıcının tutacağı en fazla istemci kaydıdır (bellek tavanı).
const maxBuckets = 100_000

// ipRateLimiter IP başına basit bir token bucket'tır (harici bağımlılık yok).
type ipRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newIPRateLimiter(perSec, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		rate:    float64(perSec),
		burst:   float64(burst),
		buckets: map[string]*bucket{},
		now:     time.Now,
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()

	// Bellek şişmesini önlemek için boşta kalan kovaları ara ara temizle.
	if now.Sub(l.lastGC) > time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.last) > 5*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}

	b, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			return false // tablo dolu: yeni istemcileri geçici olarak reddet (fail-closed)
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
