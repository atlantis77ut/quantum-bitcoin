package network

import (
	"errors"
	"fmt"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if block {
		if len(s.blocks) == peerBlockQueue {
			popFront(&s.blocks)
		}
		s.blocks = append(s.blocks, data)
	} else {
		if now.Before(s.openUntil) || len(s.txs) == peerTxQueue {
			s.dropped++
			return false
		}
		s.txs = append(s.txs, data)
	}
	if s.running {
		return false
	}
	s.running = true
	return true
}

// next sıradaki mesajı verir (önce bloklar); kuyruk boşsa çalışanı durdurur.
func (s *peerSender) next() ([]byte, bool) {
	data, _, ok := s.nextItem()
	return data, ok
}

// nextItem next gibidir; mesajın blok olup olmadığını da söyler.
func (s *peerSender) nextItem() (data []byte, block, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case len(s.blocks) > 0:
		return popFront(&s.blocks), true, true
	case len(s.txs) > 0:
		return popFront(&s.txs), false, true
	}
	s.running = false
	return nil, false, false
}

// discard kuyrukları boşaltır (eş etkin kümeden çıktı, backup.go); çalışanı yoksa true. Çalışan
// sürmekte olan gönderimi bitirince boş kuyrukta durur.
func (s *peerSender) discard() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.blocks)
	s.blocks = s.blocks[:0]
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

// sendLoop eşin kuyruğunu sırayla gönderir ve kuyruk boşalınca biter. Erken başarısız blok
// itmesi bir kez yeniden denenir (push.go).
func (ps *P2PServer) sendLoop(peer string, s *peerSender) {
	for {
		data, block, ok := s.nextItem()
		if !ok {
			return
		}
		err := ps.pushOnce(peer, data, block)
		if block && errors.Is(err, errPushEarly) && s.healthy() && ps.retryWorth(s) {
			ps.log.Debug("blok itmesi erken başarısız, yeniden deneniyor", "peer", peer, "err", err)
			err = ps.pushOnce(peer, data, block)
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
func (ps *P2PServer) sendTo(peer string, data []byte) error {
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
	return err
}
