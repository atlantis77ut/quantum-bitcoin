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

// Teslim bekleyen tutana stall kuralı uygulanmaz, hız sınırı uygulanır; yazan tutana ikisi de.
func TestDrainingHolderTakeover(t *testing.T) {
	l := defaultPushLimits()
	l.stall = 50 * time.Millisecond
	l.sendChunk = time.Second
	s := newPushSlots(2)
	s.acquire("a", &l)
	s.acquire("b", &l)
	s.drain("a")
	now := time.Now().Add(200 * time.Millisecond) // stall geçti, hız sınırı (1 sn) geçmedi
	s.mu.Lock()
	stuck := s.stuckLocked(now, &l)
	s.mu.Unlock()
	if stuck != "b" {
		t.Fatalf("stall sonrası devralınabilir %q, beklenen yazan tutan b", stuck)
	}
	s.release("b")
	s.mu.Lock()
	stuck = s.stuckLocked(now, &l)
	s.mu.Unlock()
	if stuck != "" {
		t.Fatalf("teslim bekleyen tutan stall ile devralındı: %q", stuck)
	}
	s.mu.Lock()
	stuck = s.stuckLocked(now.Add(2*time.Second), &l)
	s.mu.Unlock()
	if stuck != "a" {
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
	time.Sleep(50 * time.Millisecond)
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
