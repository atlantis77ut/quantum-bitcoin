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

func groupOf(a net.Addr) string {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return peerGroup(host)
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
