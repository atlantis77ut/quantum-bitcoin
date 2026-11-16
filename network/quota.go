package network

import (
	"net"
	"sync"
	"time"
)

// Eş başına kotalar (CLAUDE.md madde 5). Gelen bağlantılar kimlik doğrulamasızdır; bir eş
// adres grubuyla tanınır: IPv4'te /24, IPv6'da /48. Tek bir kiralık sunucu ya da tek bir /48
// tahsisi ortak yuvaların (bağlantı, çözümleme, sunum) yalnızca küçük bir kısmını tutabilir.
// Grup başına tek çözümleme ve tek sunum yuvası: çözümleme yuvalarının korunmayan payını (5)
// tutmak için 5 ayrı grup gerekir; yuvanın ilk connReadTimeout'u bedavadır (yeniden bağlanarak
// sürdürülebilir), sonrası yuva başına ~1 Mbit/sn sürekli gönderim ister ve en çok
// transferTimeout sürer (push.go; dürüst yavaş itme de yuvayı bu kadar tutar). 16 sunum
// yuvasını (asgari hızda okuyarak 60 sn) ve 64 bağlantı yuvasını tutmak için 16 ayrı grup
// gerekir. Kalan risk (tahliye yok): bu kadar /24 öneki ya da tek bir IPv6 /44 (16 adet /48)
// tahsisi olan saldırgan gelen eşlere senkronizasyon sunumunu ve blok duyurularını
// aksatabilir; yapılandırılmış eşlere giden senkronizasyon etkilenmez.
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
// onChunk (nil olabilir) her yazılan parçadan sonra parçanın boyuyla çağrılır.
type deadlineWriter struct {
	conn    net.Conn
	chunk   int
	per     time.Duration
	end     time.Time
	onChunk func(n int)
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
		if w.onChunk != nil {
			w.onChunk(m)
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
// önbelleğini aşardı). Tor/ters vekil arkasındaki eşler tek adres sayılır, kovayı ve cezayı
// paylaşır.
//
// Hız kovası iç anahtarındır, dış grubun kovası txGroupFactor katı bir tavandır (kullanıcı
// kararı 2026-11-16, m = 4): aynı /24'teki komşu kendi kovasını tüketip dürüst eşin işlem
// yayınını kesemez; grubun toplamı tavanla sınırlıdır, yeni iç anahtar bedava yeni bütçe getirmez
// (tavanın ötesinde). Tavanı doldurmak gruptan txGroupFactor adres ister; o zaman dürüst komşu
// yine yavaşlar. İşlem başına kova reddi cezasızdır: grup bırakılır, havuz eşitlemesine kalır.
// Gönderen tarafı (relaybudget.go) iç anahtarın hızının altında (7/8) gönderir.
const (
	txRatePerSec  = 64 // dürüst ağ en çok ~4 işlem/sn onaylar (250 işlem / 60 sn)
	txRateBurst   = 256
	txGroupFactor = 4 // dış grup tavanı: txGroupFactor × iç anahtar hızı ve ani payı
	badTxPenalty  = 10 * time.Minute
	maxTxGateKeys = 10_000
)

// txGate iç anahtar başına işlem belirteç kovası, dış grup başına tavan kovası ve iç anahtar
// başına ceza süresidir; grupta pencere içinde innerEscalateK farklı iç anahtar cezalanırsa ceza
// bütün gruba yükselir (innerkey.go).
type txGate struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*txBucket // dış grup → tavan kovası
	inner   map[string]*txBucket // iç anahtar → kova
	bans    map[string]time.Time // iç anahtar ya da yükseltilmiş grup → ceza sonu
	esc     *escalator
}

type txBucket struct {
	tokens float64
	last   time.Time
}

func newTxGate() *txGate {
	return &txGate{now: time.Now, buckets: map[string]*txBucket{}, inner: map[string]*txBucket{},
		bans: map[string]time.Time{}, esc: newEscalator()}
}

// allow eşin n işlemini kabul etmeye izin verir; iç anahtarı ya da grubu ceza süresindeyse ya
// da iç anahtarın ya da grubun kovası yetmiyorsa reddedilir (ikisinden de harcanmaz).
func (g *txGate) allow(id peerID, n int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	if g.bannedLocked(id.inner, now) || g.bannedLocked(id.group, now) {
		return false
	}
	const groupRate, groupBurst = txGroupFactor * txRatePerSec, txGroupFactor * txRateBurst
	gb := entryLocked(g.buckets, id.group, now, groupRate, groupBurst)
	ib := entryLocked(g.inner, id.inner, now, txRatePerSec, txRateBurst)
	gb.refill(now, groupRate, groupBurst)
	ib.refill(now, txRatePerSec, txRateBurst)
	if gb.tokens < float64(n) || ib.tokens < float64(n) {
		return false
	}
	gb.tokens -= float64(n)
	ib.tokens -= float64(n)
	return true
}

func (b *txBucket) refill(now time.Time, rate, burst float64) {
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
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

// entryLocked tablodaki kovayı döndürür, yoksa dolu açar. Tablo doluysa önce dolu kovalar, yine
// doluysa rastgele bir kayıt silinir; tablo maxTxGateKeys'i aşmaz. Silinen kova dolu yeniden
// açılır: iç anahtar tablosunu (ör. bir /48'in /64'leriyle) dolduran saldırgan kovaları
// sıfırlayabilir, ama grubun tavan kovası (ayrı tablo, grup başına bir kayıt) onu sınırlar.
func entryLocked(m map[string]*txBucket, key string, now time.Time, rate, burst float64) *txBucket {
	if b, ok := m[key]; ok {
		return b
	}
	if len(m) >= maxTxGateKeys {
		for k, b := range m {
			if b.tokens+now.Sub(b.last).Seconds()*rate >= burst {
				delete(m, k)
			}
		}
		for k := range m {
			if len(m) < maxTxGateKeys {
				break
			}
			delete(m, k)
		}
	}
	b := &txBucket{tokens: burst, last: now}
	m[key] = b
	return b
}
