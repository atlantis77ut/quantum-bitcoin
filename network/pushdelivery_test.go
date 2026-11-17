package network

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// deliveryReceiver bir bağlantıyı kabul eder, gelen her şeyi okur, hold bekler ve kapatır
// (handleConnection gibi: çözer, işler, kapatır). hold < 0 ise hiç kapatmaz.
func deliveryReceiver(t *testing.T, hold time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		io.Copy(io.Discard, c)
		if hold < 0 {
			t.Cleanup(func() { c.Close() })
			return
		}
		time.Sleep(hold)
		c.Close()
	}()
	return ln.Addr().String()
}

// Blok itmesi alıcı kapatana kadar sürer (yuva teslimde boşalır); işlem itmesi beklemez.
func TestBlockPushWaitsForDelivery(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	const hold = 400 * time.Millisecond

	addr := deliveryReceiver(t, hold)
	start := time.Now()
	if err := ps.sendTo(addr, []byte(`{"type":"block"}`+"\n"), true); err != nil {
		t.Fatalf("blok itmesi: %v", err)
	}
	if d := time.Since(start); d < hold {
		t.Fatalf("blok itmesi %v'de bitti, alıcı %v sonra kapattı", d, hold)
	}

	addr = deliveryReceiver(t, -1) // hiç kapatmaz: işlem itmesi yine hemen biter
	start = time.Now()
	if err := ps.sendTo(addr, []byte(`{"type":"txs"}`+"\n"), false); err != nil {
		t.Fatalf("işlem itmesi: %v", err)
	}
	if d := time.Since(start); d >= hold {
		t.Fatalf("işlem itmesi teslimi bekledi: %v", d)
	}
}

// Okuyup hiç kapatmayan alıcıda blok itmesi sendMax'te errNotDelivered ile biter; erken
// sayılmaz (yeniden denenmez).
func TestBlockPushNotDelivered(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.pushLim.sendMax = 300 * time.Millisecond
	addr := deliveryReceiver(t, -1)
	err := ps.sendTo(addr, []byte(`{"type":"block"}`+"\n"), true)
	if !errors.Is(err, errNotDelivered) || errors.Is(err, errPushEarly) {
		t.Fatalf("hata %v, beklenen errNotDelivered (erken değil)", err)
	}
}

// Kuyruğu okunamayan (probe yok) teslim bekleyen tutana stall kuralı uygulanmaz, hız sınırı
// uygulanır; yazan tutana ikisi de.
func TestDrainingHolderTakeover(t *testing.T) {
	l := defaultPushLimits()
	l.stall = 50 * time.Millisecond
	l.sendChunk = time.Second
	s := newPushSlots(2)
	s.acquire("a", &l)
	s.acquire("b", &l)
	s.drain("a", nil)
	now := time.Now().Add(200 * time.Millisecond) // stall geçti, hız sınırı (1 sn) geçmedi
	s.mu.Lock()
	stuck, why := s.stuckLocked(now, &l)
	s.mu.Unlock()
	if stuck != "b" || why != takeStall {
		t.Fatalf("stall sonrası devralınabilir %q, beklenen yazan tutan b", stuck)
	}
	s.release("b")
	s.mu.Lock()
	stuck, _ = s.stuckLocked(now, &l)
	s.mu.Unlock()
	if stuck != "" {
		t.Fatalf("teslim bekleyen tutan stall ile devralındı: %q", stuck)
	}
	s.mu.Lock()
	stuck, why = s.stuckLocked(now.Add(2*time.Second), &l)
	s.mu.Unlock()
	if stuck != "a" || why != takePace {
		t.Fatalf("hız sınırından sonra devralınabilir %q, beklenen a", stuck)
	}
}

// İkinci blok itmesinin yuvası, birincinin alıcısı kapatana kadar boşalmaz (tek yuvayla).
func TestPushSlotHeldUntilDelivery(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.pushSlots = newPushSlots(1)
	const hold = 400 * time.Millisecond
	addr := deliveryReceiver(t, hold)
	first := make(chan error, 1)
	start := time.Now()
	go func() { first <- ps.pushOnce(addr, []byte(`{"type":"block"}`+"\n"), pushID{}, true) }()
	for held := false; !held; { // ilk itme yuvayı alana kadar bekle (yük altında sıra kaymasın)
		if time.Since(start) > 5*time.Second {
			t.Fatal("ilk itme yuvayı almadı")
		}
		ps.pushSlots.mu.Lock()
		_, held = ps.pushSlots.holders[addr]
		ps.pushSlots.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	ps.pushSlots.acquire("ikinci", &ps.pushLim)
	got := time.Since(start)
	ps.pushSlots.release("ikinci")
	if err := <-first; err != nil {
		t.Fatalf("ilk itme: %v", err)
	}
	if got < hold {
		t.Fatalf("ikinci yuva %v'de alındı, ilk alıcı %v sonra kapattı", got, hold)
	}
}

// Kuyruğu okunan teslim bekleyen tutan: kuyruk en az bir parça küçüldükçe ya da boşalınca
// tutulur, stall boyunca böyle küçülmezse (damla damla küçülme ve sıfırda kalma dahil)
// devralınabilir; okunamayan kuyrukta eski kural kalır.
func TestDrainingHolderProbe(t *testing.T) {
	l := defaultPushLimits()
	l.stall = time.Second
	l.sendChunk = time.Hour // hız sınırı devre dışı
	stuckAt := func(s *pushSlots, at time.Time) string {
		s.mu.Lock()
		defer s.mu.Unlock()
		peer, why := s.stuckLocked(at, &l)
		if peer != "" && why != takeDrain {
			t.Errorf("teslim bekleyen %s devralma nedeni %d, beklenen takeDrain", peer, why)
		}
		return peer
	}

	q := 4 * writeChunk
	s := newPushSlots(1)
	s.acquire("a", &l)
	s.drain("a", func() (int, bool) { return q, true })
	start := time.Now()
	if got := stuckAt(s, start.Add(500*time.Millisecond)); got != "" {
		t.Fatalf("stall dolmadan devralınabilir: %q", got)
	}
	q = 3 * writeChunk
	if got := stuckAt(s, start.Add(900*time.Millisecond)); got != "" {
		t.Fatalf("bir parça küçülen kuyrukta devralınabilir: %q", got)
	}
	q -= 1000 // damla damla okuma ilerleme sayılmaz
	if got := stuckAt(s, start.Add(1500*time.Millisecond)); got != "" {
		t.Fatalf("son küçülmeden stall geçmeden devralınabilir: %q", got)
	}
	if got := stuckAt(s, start.Add(2*time.Second)); got != "a" {
		t.Fatalf("damla damla küçülen kuyrukta devralınabilir %q, beklenen a", got)
	}

	tail := 1000
	drained := newPushSlots(1)
	drained.acquire("d", &l)
	drained.drain("d", func() (int, bool) { return tail, true })
	start = time.Now()
	tail = 0 // parçadan küçük kuyruk sonunun boşalması ilerlemedir
	if got := stuckAt(drained, start.Add(900*time.Millisecond)); got != "" {
		t.Fatalf("boşalan kuyrukta devralınabilir: %q", got)
	}
	if got := stuckAt(drained, start.Add(1500*time.Millisecond)); got != "" {
		t.Fatalf("boşaldıktan sonra stall geçmeden devralınabilir: %q", got)
	}
	if got := stuckAt(drained, start.Add(2*time.Second)); got != "d" {
		t.Fatalf("boşalıp kapatılmayan bağlantı: devralınabilir %q, beklenen d", got)
	}

	zero := newPushSlots(1)
	zero.acquire("z", &l)
	zero.drain("z", func() (int, bool) { return 0, true })
	start = time.Now()
	if got := stuckAt(zero, start.Add(500*time.Millisecond)); got != "" {
		t.Fatalf("boş kuyrukta stall dolmadan devralınabilir: %q", got)
	}
	if got := stuckAt(zero, start.Add(1500*time.Millisecond)); got != "z" {
		t.Fatalf("boş kuyrukta kapatmayan alıcı: devralınabilir %q, beklenen z", got)
	}

	blind := newPushSlots(1)
	blind.acquire("b", &l)
	blind.drain("b", func() (int, bool) { return 0, false })
	if got := stuckAt(blind, time.Now().Add(time.Minute)); got != "" {
		t.Fatalf("okunamayan kuyrukta stall uygulandı: %q", got)
	}
}

// acquire devralmayı nedenine göre sayar: yazarken stall ve hız sınırı (teslim nedeni Linux'ta
// outq_linux_test.go'da).
func TestPushSlotsCountTakeovers(t *testing.T) {
	l := defaultPushLimits()
	l.stall = 50 * time.Millisecond
	l.sendChunk = time.Hour
	s := newPushSlots(1)
	s.acquire("a", &l)
	s.acquire("b", &l) // a hiç yazmadı: stall
	if got := s.takeovers(); got != (PushTakeovers{Stall: 1}) {
		t.Fatalf("stall sonrası devralmalar %+v", got)
	}

	l.stall = time.Hour
	l.sendChunk = 50 * time.Millisecond
	p := newPushSlots(1)
	p.acquire("a", &l)
	p.acquire("b", &l) // a hız sınırının gerisinde
	if got := p.takeovers(); got != (PushTakeovers{Pace: 1}) {
		t.Fatalf("hız sınırı sonrası devralmalar %+v", got)
	}
}
