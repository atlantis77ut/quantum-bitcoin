package network

import (
	"errors"
	"net"
	"sync"
	"time"

	"quantum-bitcoin/core"
)

// İtme taşıması (docs/ES-KESFI.md §4.5; kullanıcı kararı 2026-11-14; düğüm politikası, uzlaşma
// kuralı değil). Tek mesajlı bağlantıda itilen mesaja (blok, işlem grubu) eskiden gönderende ve
// alıcıda tek bir 10 sn süre verilirdi: 7,2 MiB'lık dolu blok eş başına ~6 Mbit/sn'den yavaş
// bağlantıdan hiç gidemez, başarısız blok itmesi de yeniden denenmezdi.
//
// Alıcı: okuma süresi her okumadan önce tazelenir ve üç sınırın en erkenidir: şimdi + recvGrace
// (canlılık), başlangıç + recvGrace + okunan bayt / recvMinRate (asgari ortalama hız; ilk
// recvGrace eskisi gibi bedavadır) ve başlangıç + recvMax. Başlangıç çözümleme yuvası
// alındıktan sonradır. Varsayılanlarla dolu blok ~1 Mbit/sn bağlantıdan gelir. Hıza ya da tavana
// takılan bağlantı bozuk mesaj sayılmaz, eş geri çevrilmez. Çözümleme yuvası grup başına 1
// kalır; yuvayı recvGrace'ten uzun tutmak yuva başına recvMinRate'lik sürekli gönderim ister
// (quota.go).
//
// Gönderen: her parçaya sendChunk süre verilir; bu, alıcının yuva için bekleyebileceği en uzun
// süreden (quotaWait + connReadTimeout) uzundur, yoksa alıcı yuva beklerken tampon dolar ve
// gönderim düşerdi. Toplam sendMax alıcının yuva beklemesini ve okuma tavanını kapsar.
//
// Yeniden deneme: blok itmesi erken başarısız olursa (bağlantı kurulamadı ya da en çok
// earlyBytes yazılmışken koptu) retryDelay sonra bir kez yeniden denenir; eşin son gönderimi
// başarılıysa ve kuyrukta daha yeni blok yoksa (beklemeden önce ve sonra bakılır). Alıcı,
// yuvası doluyken bağlantıyı okumadan kapatınca yazılan yalnızca iki uçtaki çekirdek
// tamponlarıdır; geniş alan bağlantısında bunlar ~1 MB'ı geçmez, eşik (2 MiB) dolu bloğun üçte
// birinin altındadır. Ayrım sezgiseldir: tamponu büyük bağlantıda (ör. aynı makinede, okumayan
// alıcıda bile ~2,5 MB) dolu alıcıya giden itme geç sayılabilir, asgari hızın çok altındaki
// bağlantıda kesilen itme erken sayılıp bir kez daha denenebilir. İki uçtaki tamponlara sığan
// itme (lansmandaki küçük bloklar) gönderende hiç başarısız görünmez: alıcı okumadan kapatırsa
// blok o eşe uç beklemesi ya da senkronizasyonla gider. Geç kopan itme denenmez: bağlantı o
// kopya için yavaştır, yeniden denemek diğer kopyaların bandını yer.
//
// Kademeli itme: düğüm aynı anda en çok blockPushSlots blok gövdesi iter; yukarı bant bütün
// kopyalara bölünmez, ilk alıcılar bloğu erken alıp kendileri yaymaya başlar (en çok kendi
// bloğumuzda önemlidir, ondan başka kaynak yoktur). Yuva bekleyen gönderim, yuvayı tutanlar
// ilerledikçe bekler. Tutan, stall boyunca hiç parça yazamazsa ya da dürüst bir alıcının
// isteyeceği hızın gerisinde kalırsa (sendChunk + yazılan / recvMinRate) bekleyenlerden biri
// yuvasını devralır; takılan itme sürer ama sayılmaz. Böylece her takılan eş eşzamanlılığı en
// çok bir artırır. Varsayılanlarla iki kopya ~2 Mbit/sn yukarı bantla alıcıların asgari hızını
// karşılar.
//
// Kaynak: senkronizasyonla ya da uç beklemesiyle bir eşten indirilen blok o eşe geri itilmez.
// Gelen bağlantıyla itilen blok kaynağına geri itilebilir (yankı): gelen bağlantının kaynak
// portu geçicidir ve aynı IP'de birden çok düğüm (aynı makine, NAT) olabilir; IP ile eşleştirmek
// bloğu almamış bir eşi atlayabilirdi. Yankı alıcıda ErrKnownBlock ile biter.
const (
	blockPushSlots = 2
	pushSlotPoll   = 50 * time.Millisecond
	pushEarlyBytes = 2 << 20
)

// errPushEarly itmenin erken (yeniden denemeye değer biçimde) başarısız olduğunu söyler.
var errPushEarly = errors.New("itme erken başarısız")

// pushLimits itme taşımasının süreleridir; Serve'den önce ayarlanır (testler kısaltır), sonra
// yalnızca okunur.
type pushLimits struct {
	recvGrace   time.Duration
	recvMinRate int64 // bayt/sn
	recvMax     time.Duration
	sendChunk   time.Duration
	sendMax     time.Duration
	retryDelay  time.Duration
	stall       time.Duration
	earlyBytes  int
}

func defaultPushLimits() pushLimits {
	return pushLimits{
		recvGrace:   connReadTimeout,
		recvMinRate: 128 << 10, // ~1 Mbit/sn
		recvMax:     transferTimeout,
		sendChunk:   quotaWait + connReadTimeout + chunkWriteTimeout,
		sendMax:     quotaWait + connReadTimeout + transferTimeout + 3*time.Second,
		retryDelay:  2 * time.Second,
		stall:       2 * time.Second,
		earlyBytes:  pushEarlyBytes,
	}
}

// readDeadline start'ta başlayan okumada n bayt okunduktan sonra now anındaki okuma süresidir.
func (l *pushLimits) readDeadline(start, now time.Time, n int64) time.Time {
	d := now.Add(l.recvGrace)
	if r := start.Add(l.recvGrace + time.Duration(n*int64(time.Second)/l.recvMinRate)); r.Before(d) {
		d = r
	}
	if c := start.Add(l.recvMax); c.Before(d) {
		d = c
	}
	return d
}

// rateReader her okumadan önce okuma süresini readDeadline ile tazeler.
type rateReader struct {
	conn  net.Conn
	lim   *pushLimits
	start time.Time
	n     int64
}

func (r *rateReader) Read(p []byte) (int, error) {
	if err := r.conn.SetReadDeadline(r.lim.readDeadline(r.start, time.Now(), r.n)); err != nil {
		return 0, err
	}
	m, err := r.conn.Read(p)
	r.n += int64(m)
	return m, err
}

// pushSlots blok itme yuvalarıdır. Bir eşe aynı anda en çok bir mesaj gittiğinden eş adresi
// anahtar olarak yeter.
type pushSlots struct {
	mu      sync.Mutex
	limit   int
	holders map[string]*pushHolder
}

type pushHolder struct {
	start time.Time
	last  time.Time // son yazılan parça
	n     int64     // yazılan bayt
}

func newPushSlots(limit int) *pushSlots {
	return &pushSlots{limit: limit, holders: map[string]*pushHolder{}}
}

// stuckLocked yuvası devralınabilecek bir tutanı döndürür; yoksa boş.
func (s *pushSlots) stuckLocked(now time.Time, l *pushLimits) string {
	for peer, h := range s.holders {
		pace := h.start.Add(l.sendChunk + time.Duration(h.n*int64(time.Second)/l.recvMinRate))
		if now.Sub(h.last) >= l.stall || now.After(pace) {
			return peer
		}
	}
	return ""
}

// acquire eş için yuva alır: boş yuva yoksa tutanlardan biri takılana (stuckLocked) ya da yuva
// boşalana kadar bekler. Bekleme sınırlıdır: tutan en çok dialTimeout + sendMax sonra bırakır.
func (s *pushSlots) acquire(peer string, l *pushLimits) {
	for {
		s.mu.Lock()
		now := time.Now()
		if len(s.holders) >= s.limit {
			if stuck := s.stuckLocked(now, l); stuck != "" {
				delete(s.holders, stuck)
			}
		}
		if len(s.holders) < s.limit {
			s.holders[peer] = &pushHolder{start: now, last: now}
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		time.Sleep(pushSlotPoll)
	}
}

// progress yuvayı tutan eşin n baytlık parça yazdığını işler; eş yuva tutmuyorsa (yuvası
// devralındıysa) bir şey yapmaz.
func (s *pushSlots) progress(peer string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.holders[peer]; ok {
		h.last = time.Now()
		h.n += int64(n)
	}
}

func (s *pushSlots) release(peer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.holders, peer)
}

// relayItem yayın kuyruğundaki bloktur; from bloğun indirildiği eştir (boşsa bütün eşlere).
type relayItem struct {
	b    *core.Block
	from string
}

// pushOnce mesajı eşe bir kez gönderir; blok gövdesi blok itme yuvasıyla gider.
func (ps *P2PServer) pushOnce(peer string, data []byte, block bool) error {
	if block {
		ps.pushSlots.acquire(peer, &ps.pushLim)
		defer ps.pushSlots.release(peer)
	}
	return ps.send(peer, data)
}

// retryWorth erken başarısız blok itmesinin yeniden denenip denenmeyeceğine karar verir;
// denenecekse retryDelay bekler. Kuyrukta daha yeni blok varsa (beklemeden önce ya da sonra)
// denemez, sıradaki blok gider.
func (ps *P2PServer) retryWorth(s *peerSender) bool {
	if s.hasBlock() {
		return false
	}
	time.Sleep(ps.pushLim.retryDelay)
	return !s.hasBlock()
}
