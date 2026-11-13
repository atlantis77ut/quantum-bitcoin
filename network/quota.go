package network

import (
	"net"
	"sync"
	"time"
)

// Eş başına kotalar (CLAUDE.md madde 5). Gelen bağlantılar kimlik doğrulamasızdır; bir eş
// adres grubuyla tanınır: IPv4'te /24, IPv6'da /48. Tek bir kiralık sunucu ya da tek bir /48
// tahsisi ortak yuvaların (bağlantı, çözümleme, sunum) yalnızca küçük bir kısmını tutabilir.
// Grup başına tek çözümleme ve tek sunum yuvası: 8 çözümleme yuvasını (yavaş gönderimle
// 10 sn) tutmak için 8, 16 sunum yuvasını (asgari hızda okuyarak 60 sn) ve 64 bağlantı
// yuvasını tutmak için 16 ayrı grup gerekir. Kalan risk (tahliye yok): bu kadar /24 öneki ya da
// tek bir IPv6 /44 (16 adet /48) tahsisi olan saldırgan gelen eşlere senkronizasyon sunumunu
// ve blok duyurularını aksatabilir; yapılandırılmış eşlere giden senkronizasyon etkilenmez.
const (
	connsPerGroup   = 4 // aynı anda açık gelen bağlantı
	decodesPerGroup = 1 // aynı anda çözümlenen gelen mesaj (maxConcurrentDecodes içinden)
	servesPerGroup  = 1 // aynı anda sunulan yanıt (maxConcurrentServes içinden)

	// quotaWait yuva beklemesinin üst sınırıdır: kısa süreli çakışmalar (eşzamanlı yayın ve
	// senkronizasyon) isteği düşürmesin, ama yuva bekleyen bağlantılar birikmesin.
	quotaWait = 2 * time.Second

	// Yanıtlar writeChunk'lık parçalarla yazılır ve her parçaya chunkWriteTimeout süre
	// verilir: yanıtı okumayan ya da çok yavaş okuyan eş (~260 kbit/sn altı) sunum yuvasını
	// transferTimeout boyunca değil, en çok bir parça süresi kadar tutar.
	writeChunk        = 64 << 10
	chunkWriteTimeout = 2 * time.Second
)

// groupQuota adres grubu başına eşzamanlı kullanım sayacıdır.
type groupQuota struct {
	mu     sync.Mutex
	limit  int
	counts map[string]int
}

func newGroupQuota(limit int) *groupQuota {
	return &groupQuota{limit: limit, counts: map[string]int{}}
}

// tryAcquire grubun kotası doluysa beklemeden false döner.
func (q *groupQuota) tryAcquire(group string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.counts[group] >= q.limit {
		return false
	}
	q.counts[group]++
	return true
}

// acquire kota boşalana kadar en çok wait bekler.
func (q *groupQuota) acquire(group string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for !q.tryAcquire(group) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

func (q *groupQuota) release(group string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.counts[group] <= 1 {
		delete(q.counts, group)
		return
	}
	q.counts[group]--
}

// peerGroup eşin kota grubudur: IPv4'te /24, IPv6'da /48 öneki.
func peerGroup(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// deadlineWriter her parçadan önce yazma süresini tazeler; toplam süre end ile sınırlıdır.
type deadlineWriter struct {
	conn  net.Conn
	chunk int
	per   time.Duration
	end   time.Time
}

func (w *deadlineWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		c := min(len(p), w.chunk)
		d := time.Now().Add(w.per)
		if d.After(w.end) {
			d = w.end
		}
		if err := w.conn.SetWriteDeadline(d); err != nil {
			return n, err
		}
		m, err := w.conn.Write(p[:c])
		n += m
		if err != nil {
			return n, err
		}
		p = p[c:]
	}
	return n, nil
}

// Gelen işlem mesajlarının adres grubu başına sınırları. Her geçerli biçimli işlem bir ML-DSA
// doğrulaması ister ve havuz kilidi altında doğrulanır; kotalar yalnızca eşzamanlılığı sınırlar,
// bu yüzden işlem sayısı ayrıca zamana bağlı sınırlanır. Dürüst eş yalnızca kendi kabul ettiği
// işlemleri yayar: geçersiz biçimli ya da imzalı işlem gönderen eşin (iç anahtarın) işlemleri
// bir süre bırakılır (imzayı değiştirerek her mesajda yeni kimlikli bir işlem üretmek ret
// önbelleğini aşardı). Hız kovası dış grubundur. Tor/ters vekil arkasındaki eşler tek adres
// sayılır, kovayı ve cezayı paylaşır.
const (
	txRatePerSec  = 64 // dürüst ağ en çok ~17 işlem/sn onaylar (1000 işlem / 60 sn)
	txRateBurst   = 256
	badTxPenalty  = 10 * time.Minute
	maxTxGateKeys = 10_000
)

// txGate dış grup başına işlem belirteç kovası ve iç anahtar başına ceza süresidir; grupta
// pencere içinde innerEscalateK farklı iç anahtar cezalanırsa ceza bütün gruba yükselir
// (innerkey.go).
type txGate struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*txBucket // dış grup → kova
	bans    map[string]time.Time // iç anahtar ya da yükseltilmiş grup → ceza sonu
	esc     *escalator
}

type txBucket struct {
	tokens float64
	last   time.Time
}

func newTxGate() *txGate {
	return &txGate{now: time.Now, buckets: map[string]*txBucket{}, bans: map[string]time.Time{},
		esc: newEscalator()}
}

// allow eşin n işlemini kabul etmeye izin verir; iç anahtarı ya da grubu ceza süresindeyse ya
// da grubun kovası yetmiyorsa reddedilir.
func (g *txGate) allow(id peerID, n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.bannedLocked(id.inner, now) || g.bannedLocked(id.group, now) {
		return false
	}
	b := g.entryLocked(id.group, now)
	b.tokens = min(txRateBurst, b.tokens+now.Sub(b.last).Seconds()*txRatePerSec)
	b.last = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

func (g *txGate) bannedLocked(key string, now time.Time) bool {
	until, ok := g.bans[key]
	if !ok {
		return false
	}
	if !now.Before(until) {
		delete(g.bans, key)
		return false
	}
	return true
}

// penalize eşin iç anahtarının işlemlerini badTxPenalty boyunca bırakır; grupta pencere içinde
// innerEscalateK farklı iç anahtar cezalandıysa bütün grubunkileri. Grubu zaten cezalı eş yeni
// kayıt açmaz: tek bir /48 her yeni /64 ile tabloyu dolduramaz.
func (g *txGate) penalize(id peerID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.bannedLocked(id.group, now) {
		return
	}
	g.banLocked(id.inner, now)
	if g.esc.note(id, now) {
		g.banLocked(id.group, now)
	}
}

// banLocked anahtarı badTxPenalty boyunca bırakır; süren daha uzun ceza kısalmaz. Tablo doluysa
// süresi dolanlar, yine doluysa kayıtların onda biri rastgele silinir (map sırası rastgeledir;
// tam tarama en çok her maxTxGateKeys/10 yeni kayıtta bir yapılır): tabloyu cezalı kayıtlarla
// dolduran saldırgan yeni dürüst eşleri kilitleyemez, ama başka eşlerin cezalarını da
// düşürebilir; bunun için çok sayıda grup gerekir (grup başına en çok innerEscalateK iç anahtar
// ve bir grup kaydı). Tablo maxTxGateKeys'i aşmaz.
func (g *txGate) banLocked(key string, now time.Time) {
	until := now.Add(badTxPenalty)
	if old, ok := g.bans[key]; ok {
		if old.After(until) {
			return
		}
	} else if len(g.bans) >= maxTxGateKeys {
		for k, u := range g.bans {
			if !now.Before(u) {
				delete(g.bans, k)
			}
		}
		for k := range g.bans {
			if len(g.bans) <= maxTxGateKeys-maxTxGateKeys/10 {
				break
			}
			delete(g.bans, k)
		}
	}
	g.bans[key] = until
}

// entryLocked grubun kovasını döndürür, yoksa açar. Tablo doluysa önce dolu kovalar, yine
// doluysa rastgele bir kayıt silinir; tablo maxTxGateKeys'i aşmaz.
func (g *txGate) entryLocked(group string, now time.Time) *txBucket {
	if b, ok := g.buckets[group]; ok {
		return b
	}
	if len(g.buckets) >= maxTxGateKeys {
		g.pruneLocked(now)
		for k := range g.buckets {
			if len(g.buckets) < maxTxGateKeys {
				break
			}
			delete(g.buckets, k)
		}
	}
	b := &txBucket{tokens: txRateBurst, last: now}
	g.buckets[group] = b
	return b
}

func (g *txGate) pruneLocked(now time.Time) {
	for k, b := range g.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*txRatePerSec >= txRateBurst {
			delete(g.buckets, k)
		}
	}
}
