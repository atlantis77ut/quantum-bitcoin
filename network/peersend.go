package network

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Eş başına gönderim kuyruğu ve devre kesici (düğüm politikası, uzlaşma kuralı değil).
//
// Yayınlar (blok, işlem grubu) her yapılandırılmış eşin kendi kuyruğuna girer ve eşin kendi
// çalışanıyla gönderilir; broadcast gönderimi beklemez. Böylece kara deliğe düşmüş ya da yavaş
// okuyan bir eş yalnızca kendi kuyruğunu geciktirir, diğer eşlere yayını ve yayın döngülerini
// değil (eskiden global bir kilit altında bütün eşler beklenirdi). Eskiden o kilidin sağladığı
// iki şey eş başına korunur: karşı düğüm bağlantılarımızı tek adres grubundan görür ve grubun tek
// çözümleme yuvası vardır, bu yüzden bir eşe aynı anda en çok bir mesaj gider; bloklar bekleyen
// işlem gruplarının önüne geçer.
//
// Kuyruklar sınırlıdır ve kodlanmış mesajı eşler arasında paylaşır. Blok kuyruğu dolunca en eski
// blok düşer (en yeni uç önemlidir; eksik atalar senkronizasyonla gelir). İşlem kuyruğu dolunca
// gelen grup düşer (sıra korunur); düşen işlemler havuz eşitlemesine kalır. İşlem kuyruğu
// karşı düğümün ani payına göre seçilir: eş bir süre takılıp kuyruk birden boşalırken gönderen
// kovası da dolmuş olabilir (takılma kovayı bekletmez), bu yüzden karşı düğüm bizden en çok
// kuyruk + uçuştaki grup + kovanın ani payı = 112 + 16 + 128 = 256 işlemlik bir öbek görür,
// ani payını (txRateBurst) aşmaz.
//
// Devre kesici: art arda peerBreakerFails başarısız gönderimde devre açılır, bekleyen işlem
// grupları atılır ve devre açıkken yeni işlem grupları kuyruğa alınmaz (boşa bağlantı denemesi
// olmasın). Bloklar her zaman denenir ve yoklama işi görür: blok gidince devre kapanır; başarısız
// olursa süre ikiye katlanarak (peerBreakerMin'den peerBreakerMax'a) yeniden açılır. Süre dolunca
// işlem grupları yeniden alınır; ilki başarısız olursa devre yeniden açılır ve bekleyenler
// atılır. Herhangi bir başarılı gönderim sayacı sıfırlar. Kesici
// yalnızca yayınları kapsar; senkronizasyon ve havuz eşitlemesi istekleri kendi süre bütçeleriyle
// çalışır.

const (
	peerBlockQueue   = 4
	peerTxQueue      = (txRateBurst-relayBurst)/maxTxsPerMsg - 1 // 7 grup (bkz. dosya belgesi)
	peerBreakerFails = 3
	peerBreakerMin   = 30 * time.Second
	peerBreakerMax   = 10 * time.Minute
)

// peerSender bir eşin gönderim kuyruğu ve devre kesici durumudur. running, kuyruk boşken
// çalışanın durduğunu söyler; kuyruk denetimiyle aynı kilit altında değişir (aynı eşe ikinci
// çalışan başlamaz).
type peerSender struct {
	mu        sync.Mutex
	blocks    [][]byte
	ids       []pushID // blocks ile aynı sırada kopya kimlikleri (uyandırma kapısı)
	txs       [][]byte
	running   bool
	fails     int
	backoff   time.Duration
	openUntil time.Time
	dropped   int // atılan işlem grubu sayısı
}

func popFront(q *[][]byte) []byte {
	d := (*q)[0]
	copy(*q, (*q)[1:])
	(*q)[len(*q)-1] = nil
	*q = (*q)[:len(*q)-1]
	return d
}

// push mesajı kuyruğa koyar; yeni bir çalışan başlatılması gerekiyorsa true döner.
func (s *peerSender) push(data []byte, block bool, now time.Time) bool {
	if block {
		start, _ := s.pushBlock(data, pushID{}, now)
		return start
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.Before(s.openUntil) || len(s.txs) == peerTxQueue {
		s.dropped++
		return false
	}
	s.txs = append(s.txs, data)
	return s.startLocked()
}

// pushBlock id kopyasını kuyruğa koyar; kuyruk doluysa en eskiyi atar ve kimliğini dropped'ta
// döndürür.
func (s *peerSender) pushBlock(data []byte, id pushID, now time.Time) (start bool, dropped pushID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.blocks) == peerBlockQueue {
		popFront(&s.blocks)
		dropped = popFrontID(&s.ids)
	}
	s.blocks = append(s.blocks, data)
	s.ids = append(s.ids, id)
	return s.startLocked(), dropped
}

// startLocked çalışan yoksa onu başlatılmış işaretler ve true döner.
func (s *peerSender) startLocked() bool {
	if s.running {
		return false
	}
	s.running = true
	return true
}

func popFrontID(q *[]pushID) pushID {
	h := (*q)[0]
	*q = append((*q)[:0], (*q)[1:]...)
	return h
}

// next sıradaki mesajı verir (önce bloklar); kuyruk boşsa çalışanı durdurur.
func (s *peerSender) next() ([]byte, bool) {
	data, _, _, ok := s.nextItem()
	return data, ok
}

// nextItem next gibidir; mesajın blok olup olmadığını ve blok kopyasının kimliğini de söyler.
func (s *peerSender) nextItem() (data []byte, id pushID, block, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case len(s.blocks) > 0:
		return popFront(&s.blocks), popFrontID(&s.ids), true, true
	case len(s.txs) > 0:
		return popFront(&s.txs), pushID{}, false, true
	}
	s.running = false
	return nil, pushID{}, false, false
}

// discard kuyrukları boşaltır (eş etkin kümeden çıktı, backup.go); çalışanı yoksa true. Çalışan
// sürmekte olan gönderimi bitirince boş kuyrukta durur.
func (s *peerSender) discard() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.blocks)
	s.blocks = s.blocks[:0]
	s.ids = s.ids[:0]
	clear(s.txs)
	s.txs = s.txs[:0]
	return !s.running
}

// hasBlock kuyrukta bekleyen blok olup olmadığını söyler.
func (s *peerSender) hasBlock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blocks) > 0
}

// healthy eşe son gönderimin başarılı olduğunu (art arda hata olmadığını) söyler.
func (s *peerSender) healthy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fails == 0
}

// done gönderim sonucunu devre kesiciye işler. Devre bu sonuçla açıldıysa açık kalma süresini,
// açıkken başarılı gönderimle kapandıysa recovered'ı döndürür.
func (s *peerSender) done(err error, now time.Time) (opened time.Duration, recovered bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		recovered = s.fails >= peerBreakerFails
		s.fails, s.backoff, s.openUntil = 0, 0, time.Time{}
		return 0, recovered
	}
	s.fails++
	if s.fails < peerBreakerFails {
		return 0, false
	}
	if s.backoff == 0 {
		s.backoff = peerBreakerMin
	} else {
		s.backoff = min(2*s.backoff, peerBreakerMax)
	}
	s.openUntil = now.Add(s.backoff)
	s.dropped += len(s.txs)
	clear(s.txs)
	s.txs = s.txs[:0]
	return s.backoff, false
}

// senderFor eşin gönderim durumunu döndürür; yoksa oluşturur.
func (ps *P2PServer) senderFor(peer string) *peerSender {
	ps.sendersMu.Lock()
	defer ps.sendersMu.Unlock()
	s := ps.senders[peer]
	if s == nil {
		s = &peerSender{}
		ps.senders[peer] = s
	}
	return s
}

// enqueueSend mesajı eşin kuyruğuna koyar ve gerekirse eşin çalışanını başlatır.
func (ps *P2PServer) enqueueSend(peer string, data []byte, block bool, now time.Time) {
	s := ps.senderFor(peer)
	if s.push(data, block, now) {
		go ps.sendLoop(peer, s)
	}
}

// enqueueBlock özeti hash olan bloğu eşin kuyruğuna koyar; itme evrelerini uyandırma kapısına
// işler (wakegate.go).
func (ps *P2PServer) enqueueBlock(peer string, data []byte, hash string, now time.Time) {
	id, ok := ps.wake.queue(peer, hash, now)
	if !ok {
		return // aynı blok eşe zaten bekliyor ya da yazılıyor
	}
	s := ps.senderFor(peer)
	start, dropped := s.pushBlock(data, id, now)
	ps.wake.finish(peer, dropped, phaseDropped, now)
	if start {
		go ps.sendLoop(peer, s)
	}
}

// sendLoop eşin kuyruğunu sırayla gönderir ve kuyruk boşalınca biter. Erken başarısız blok
// itmesi bir kez yeniden denenir (push.go).
func (ps *P2PServer) sendLoop(peer string, s *peerSender) {
	for {
		data, id, block, ok := s.nextItem()
		if !ok {
			return
		}
		err := ps.pushOnce(peer, data, id, block)
		if errors.Is(err, errPushCancelled) {
			continue // uyandırma kapısı iptal etti (wakegate.go): ne başarı ne hata
		}
		if block && errors.Is(err, errPushEarly) && s.healthy() {
			// Yeniden deneme beklenirken itme yazılmıyor: kapı onu bekliyor sayar ve iptal edebilir.
			ps.wake.requeue(peer, id, time.Now())
			if ps.retryWorth(s) {
				ps.log.Debug("blok itmesi erken başarısız, yeniden deneniyor", "peer", peer, "err", err)
				// Kapı yeniden denemeyi iptal ettiyse ilk hata sayılır.
				if err2 := ps.pushOnce(peer, data, id, block); !errors.Is(err2, errPushCancelled) {
					err = err2
				}
			}
		}
		if err == nil {
			ps.wake.finish(peer, id, phaseSent, time.Now())
		} else {
			ps.wake.finish(peer, id, phaseFailed, time.Now())
		}
		if err != nil {
			ps.log.Warn("mesaj eşe gönderilemedi", "peer", peer, "err", err)
		}
		switch opened, recovered := s.done(err, time.Now()); {
		case opened > 0:
			ps.log.Warn("eşe gönderim devresi açıldı; işlem grupları atılıyor, bloklar denenmeye devam ediyor",
				"peer", peer, "sure", opened)
		case recovered:
			ps.log.Info("eşe gönderim devresi kapandı", "peer", peer)
		}
	}
}

// sendTo tek bir mesajı yeni bir bağlantıyla eşe yazar (push.go): her parçaya sendChunk süre
// verilir, toplam en çok dialTimeout + sendMax sürer; yazılan her parça blok itme yuvasının
// ilerlemesine işlenir. Bağlantı kurulamaz ya da en çok earlyBytes yazılmışken koparsa hata
// errPushEarly'yi sarar.
func (ps *P2PServer) sendTo(peer string, data []byte, block bool) error {
	conn, err := net.DialTimeout("tcp", peer, dialTimeout)
	if err != nil {
		return fmt.Errorf("%w: %w", errPushEarly, err)
	}
	defer conn.Close()
	w := &deadlineWriter{
		conn:    conn,
		chunk:   writeChunk,
		per:     ps.pushLim.sendChunk,
		end:     time.Now().Add(ps.pushLim.sendMax),
		onChunk: func(n int) { ps.pushSlots.progress(peer, n) },
	}
	n, err := w.Write(data)
	if err != nil && n <= ps.pushLim.earlyBytes {
		return fmt.Errorf("%w: %w", errPushEarly, err)
	}
	if err != nil || !block {
		return err
	}
	return ps.awaitDelivery(peer, conn, w.end)
}

// errNotDelivered tam yazılan blok itmesinin alıcı kapatmadan süresinin dolduğunu söyler; erken
// sayılmaz (yeniden denenmez): alıcı bağlantıyı tutuyor ama okumuyor.
var errNotDelivered = errors.New("itilen blok teslim edilmedi (alıcı süre içinde kapatmadı)")

// awaitDelivery blok itmesinin yazması bittikten sonra yuvayı teslime kadar tutar (push.go
// "Teslim"): yazma yönü kapatılır, alıcının kapatması end'e kadar beklenir. Alıcı itmeye yanıt
// yazmaz; gelen her şey atılır (en çok 4 KiB, sonrası beklenmeyen veri sayılıp bırakılır).
func (ps *P2PServer) awaitDelivery(peer string, conn net.Conn, end time.Time) error {
	tc, _ := conn.(*net.TCPConn)
	var probe func() (int, bool)
	if tc != nil {
		probe = func() (int, bool) { return unackedBytes(tc) }
	}
	ps.pushSlots.drain(peer, probe)
	if tc != nil {
		if err := tc.CloseWrite(); err != nil {
			return nil // yazılan gitti; kapatılamayan yön teslimi bekletmeye değmez
		}
	}
	if err := conn.SetReadDeadline(end); err != nil {
		return nil
	}
	_, err := io.Copy(io.Discard, io.LimitReader(conn, 4<<10))
	var ne net.Error
	switch {
	case err == nil: // EOF: alıcı kapattı
		return nil
	case errors.As(err, &ne) && ne.Timeout():
		return errNotDelivered
	default:
		return nil // sıfırlama: alıcı tam yazmadan sonra kapattı (okunmamış son bayt olabilir)
	}
}
