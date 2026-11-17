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
// alıcıda tek bir 10 sn süre verilirdi: o zamanki 7,2 MiB'lık dolu blok eş başına ~6 Mbit/sn'den yavaş
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
// tamponlarıdır; geniş alan bağlantısında bunlar ~1 MB'ı geçmez. Eşik (2 MiB) dolu bloktan
// (~1,8 MB, MaxTxPerBlock 250) büyüktür: kopan her itme erken sayılır ve bir kez daha denenir. Ayrım sezgiseldir: tamponu büyük bağlantıda (ör. aynı makinede, okumayan
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
// Teslim (kullanıcı kararı 2026-11-16): yuva yazma bitince değil, alıcı bloğu okuyup işleyip
// bağlantıyı kapatınca boşalır. Yazma yalnızca çekirdeğin gönderme tamponuna kopyalamaktır;
// tampon büyükse (döngü arayüzünde ~2,6 MB, şişkin yukarı hat) blok hattan geçmeden yazma biter
// ve bütün kopyalar birlikte gider. Gönderen yazdıktan sonra yazma yönünü kapatır (CloseWrite)
// ve alıcının kapatmasını sendMax içinde bekler; alıcı (handleConnection) mesajı çözüp işledikten
// sonra kapatır. Protokol değişmez, eski düğümlerde de çalışır. Bekleme sırasında parça
// ilerlemesi olmadığından tutan "teslim bekliyor" işaretlenir; hız sınırı (sendChunk + yazılan /
// recvMinRate) sürer. Teslim ilerlemesi (feat/push-stall-slot, 2026-11-17; 2026-11-16 kararının
// inceltilmesi): Linux'ta gönderme kuyruğunda alıcının onaylamadığı bayt (unackedBytes,
// outq_linux.go) izlenir; kuyruk stall boyunca en az writeChunk küçülmez ve boşalmazsa tutan
// takılmış sayılır (yazma aşamasında stall başına bir parça gibi; damla damla okuyan eş yuvayı
// tutamaz). Sıfıra inmiş kuyruk da küçülmez: bütün bayt alıcının çekirdeğine ulaşmış, yukarı
// bant boşalmıştır, alıcı stall içinde kapatmazsa (okumuyor ya da işliyor) bekleyenlerden biri
// yuvayı devralır. Eşik alıcının kendi alma tamponuna bağlı değildir (büyük SO_RCVBUF bütün
// bloğu okumadan onaylatabilir). Yuva yine teslimde boşalır, takılan itme sürer; yanlış
// sınıflamanın bedeli bir eşzamanlı itme fazlasıdır, her takılan eş eşzamanlılığı yine en çok
// bir artırır. Linux dışında ve TCP olmayan bağlantıda kuyruk okunmaz: hiç okumayan eş yuvayı
// stall (2 sn) yerine hız sınırına kadar (dolu blokta ~28 sn) tutabilir (eski davranış). Tam
// yazmadan sonra alıcının kapatması sıfırlama (RST) olarak da gelebilir (okunmamış son bayt);
// bu teslim sayılır. Aynı RST, okumadan kapatan alıcıdan (ör. kotası dolu) da gelebilir; ikisi
// ayırt edilemez, yuva o durumda erken boşalır (eski davranış). İşlem grubu itmeleri teslim
// beklemez.
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
	// taken devralmaların nedene göre sayısıdır (yalnızca ölçüm için okunur, propbench.go).
	taken PushTakeovers
}

// PushTakeovers itme yuvası devralmalarının nedene göre sayısıdır (yayılım ölçümü raporu).
type PushTakeovers struct {
	Stall int `json:"stall"` // yazarken stall boyunca parça yazılamadı
	Drain int `json:"drain"` // teslim beklerken kuyruk stall boyunca ilerlemedi (Linux)
	Pace  int `json:"pace"`  // hız sınırının gerisinde kaldı
}

// Add iki sayımı toplar.
func (t PushTakeovers) Add(o PushTakeovers) PushTakeovers {
	return PushTakeovers{Stall: t.Stall + o.Stall, Drain: t.Drain + o.Drain, Pace: t.Pace + o.Pace}
}

// Devralma nedenleri (stuckLocked).
const (
	takeNone = iota
	takeStall
	takeDrain
	takePace
)

type pushHolder struct {
	start    time.Time
	last     time.Time // son yazılan parça
	n        int64     // yazılan bayt
	draining bool      // yazma bitti, alıcının kapatması bekleniyor (stall yalnızca probe ile)
	// probe teslim bekleyen tutanın onaylanmamış bayt sayısını okur (unackedBytes); nil ise teslim
	// ilerlemesi izlenmez. queued ilerleme sayılan son değerdir, last o andır.
	probe  func() (int, bool)
	queued int
}

func newPushSlots(limit int) *pushSlots {
	return &pushSlots{limit: limit, holders: map[string]*pushHolder{}}
}

// stuckLocked yuvası devralınabilecek bir tutanı ve nedenini döndürür; yoksa boş. Teslim
// bekleyen tutanın kuyruğu (probe) burada yuvalar doluyken, s.mu altında okunur (ioctl,
// bloklamaz); drain de başlangıç değeri için bir kez okur.
func (s *pushSlots) stuckLocked(now time.Time, l *pushLimits) (string, int) {
	for peer, h := range s.holders {
		pace := h.start.Add(l.sendChunk + time.Duration(h.n*int64(time.Second)/l.recvMinRate))
		if now.After(pace) {
			return peer, takePace
		}
		if h.draining {
			if h.probe == nil {
				continue
			}
			q, ok := h.probe()
			if !ok {
				continue // bağlantı kapanıyor: yuva birazdan bırakılır
			}
			if q < h.queued && h.queued-q >= min(writeChunk, h.queued) { // en az bir parça ya da boşaldı
				h.queued, h.last = q, now
				continue
			}
		}
		if now.Sub(h.last) >= l.stall {
			if h.draining {
				return peer, takeDrain
			}
			return peer, takeStall
		}
	}
	return "", takeNone
}

// acquire eş için yuva alır: boş yuva yoksa tutanlardan biri takılana (stuckLocked) ya da yuva
// boşalana kadar bekler. Bekleme sınırlıdır: tutan en çok dialTimeout + sendMax sonra bırakır.
func (s *pushSlots) acquire(peer string, l *pushLimits) {
	for {
		s.mu.Lock()
		now := time.Now()
		if len(s.holders) >= s.limit {
			if stuck, why := s.stuckLocked(now, l); stuck != "" {
				delete(s.holders, stuck)
				switch why {
				case takeStall:
					s.taken.Stall++
				case takeDrain:
					s.taken.Drain++
				case takePace:
					s.taken.Pace++
				}
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

// drain eşin yazmasının bittiğini, teslimin beklendiğini işler; probe nil değilse ve okunabiliyorsa
// teslim ilerlemesi onunla izlenir (stuckLocked). Eş yuva tutmuyorsa bir şey yapmaz.
func (s *pushSlots) drain(peer string, probe func() (int, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.holders[peer]
	if !ok {
		return
	}
	h.draining = true
	if probe == nil {
		return
	}
	if q, ok := probe(); ok {
		h.probe, h.queued, h.last = probe, q, time.Now()
	}
}

// takeovers şimdiye kadarki devralma sayılarıdır.
func (s *pushSlots) takeovers() PushTakeovers {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taken
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
func (ps *P2PServer) pushOnce(peer string, data []byte, id pushID, block bool) error {
	if block {
		// Uyandırma kapısı (wakegate.go) bekleyen itmeyi iptal etmiş olabilir: yuva beklenmez;
		// yuva beklenirken iptal edildiyse yazılmaz. begin, iptal ile kilit altında yarışır.
		if !ps.wake.alive(peer, id) {
			return errPushCancelled
		}
		ps.pushSlots.acquire(peer, &ps.pushLim)
		defer ps.pushSlots.release(peer)
		if !ps.wake.begin(peer, id, time.Now()) {
			return errPushCancelled
		}
	}
	return ps.send(peer, data, block)
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
