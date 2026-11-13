package network

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *peerSender) idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.running
}

// Kuyruklar sınırlıdır: blok kuyruğunda en eski, işlem kuyruğunda gelen düşer; bloklar önce
// çıkar; boşalan kuyruk çalışanı durdurur ve sonraki push yenisini başlatır.
func TestPeerSenderQueue(t *testing.T) {
	s := &peerSender{}
	t0 := time.Unix(1_900_000_000, 0)
	if !s.push([]byte("t0"), false, t0) {
		t.Fatal("ilk push çalışan başlatmadı")
	}
	for i := 1; i < peerTxQueue+3; i++ {
		if s.push([]byte{'t', byte('0' + i)}, false, t0) {
			t.Fatal("çalışan varken ikinci çalışan başlatıldı")
		}
	}
	if len(s.txs) != peerTxQueue || s.dropped != 3 || string(s.txs[peerTxQueue-1]) != string([]byte{'t', byte('0' + peerTxQueue - 1)}) {
		t.Fatalf("işlem kuyruğu %d, atılan %d", len(s.txs), s.dropped)
	}
	for i := 0; i < peerBlockQueue+2; i++ {
		s.push([]byte{'b', byte('0' + i)}, true, t0)
	}
	if len(s.blocks) != peerBlockQueue || string(s.blocks[0]) != "b2" {
		t.Fatalf("blok kuyruğu %q", s.blocks)
	}
	var got []string
	for {
		d, ok := s.next()
		if !ok {
			break
		}
		got = append(got, string(d))
	}
	if len(got) != peerBlockQueue+peerTxQueue || got[0] != "b2" || got[peerBlockQueue] != "t0" {
		t.Fatalf("sıra %v", got)
	}
	if !s.idle() || !s.push([]byte("x"), false, t0) {
		t.Fatal("boşalan kuyruktan sonra çalışan yeniden başlatılmadı")
	}
}

// Devre kesici: art arda peerBreakerFails hatada açılır ve bekleyen işlem gruplarını atar; açıkken
// işlem grupları alınmaz, bloklar alınır ve başarılı blok devreyi kapatır; süre dolunca işlem
// grubu yoklar, başarısız yoklama süreyi ikiye katlar (tavanla); başarı sayacı sıfırlar.
func TestPeerSenderBreaker(t *testing.T) {
	s := &peerSender{}
	t0 := time.Unix(1_900_000_000, 0)
	fail := errors.New("ulaşılamadı")
	trip := func(now time.Time) time.Duration {
		t.Helper()
		var opened time.Duration
		for i := 0; i < peerBreakerFails; i++ {
			s.push([]byte("t"), false, now)
			s.push([]byte("t"), false, now)
			if _, ok := s.next(); !ok {
				t.Fatal("kuyruk boş")
			}
			if d, _ := s.done(fail, now); d > 0 {
				opened = d
				if i != peerBreakerFails-1 {
					t.Fatalf("%d. hatada açıldı", i+1)
				}
			}
		}
		return opened
	}

	s.done(fail, t0)
	s.done(fail, t0)
	if _, rec := s.done(nil, t0); rec {
		t.Fatal("açılmamış devre kapandı sayıldı")
	}
	if d := trip(t0); d != peerBreakerMin {
		t.Fatalf("açılma süresi %v", d)
	}
	if len(s.txs) != 0 {
		t.Fatal("açılınca bekleyen işlem grupları atılmadı")
	}
	t1 := t0.Add(time.Second)
	dropped := s.dropped
	s.push([]byte("t"), false, t1)
	if len(s.txs) != 0 || s.dropped != dropped+1 {
		t.Fatal("devre açıkken işlem grubu kuyruğa alındı")
	}
	s.push([]byte("b"), true, t1)
	if d, ok := s.next(); !ok || string(d) != "b" {
		t.Fatal("devre açıkken blok kuyruğa alınmadı")
	}
	if _, rec := s.done(nil, t1); !rec {
		t.Fatal("başarılı blok devreyi kapatmadı")
	}
	s.push([]byte("t"), false, t1)
	if len(s.txs) != 1 {
		t.Fatal("kapanan devrede işlem grubu alınmadı")
	}
	s.next()

	if d := trip(t1); d != peerBreakerMin {
		t.Fatalf("başarıdan sonra süre sıfırlanmadı: %v", d)
	}
	t2 := t1.Add(peerBreakerMin)
	s.push([]byte("t"), false, t2) // süre doldu: yoklama
	if _, ok := s.next(); !ok {
		t.Fatal("süre dolunca işlem grubu alınmadı")
	}
	if d, _ := s.done(fail, t2); d != 2*peerBreakerMin {
		t.Fatalf("başarısız yoklama süresi %v", d)
	}
	var d time.Duration
	for i := 0; i < 10; i++ {
		d, _ = s.done(fail, t2)
	}
	if d != peerBreakerMax {
		t.Fatalf("tavan %v", d)
	}
}

// Takılan eş diğer eşe yayını geciktirmez ve broadcast gönderimi beklemez.
func TestPeerSendIsolation(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	slow, fast := "127.0.0.1:1", "127.0.0.2:1"
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	got := make(chan string, 4)
	ps.send = func(peer string, data []byte) error {
		if peer == slow {
			<-release
			return nil
		}
		var m message
		json.Unmarshal(data, &m)
		got <- m.Type
		return nil
	}
	ps.AddPeer(slow)
	ps.AddPeer(fast)
	ps.broadcast(message{Type: msgTxs})
	ps.broadcast(message{Type: msgBlock})
	seen := map[string]bool{}
	for len(seen) < 2 { // sıra çalışanın ne zaman başladığına bağlı (bloklar önce)
		select {
		case typ := <-got:
			seen[typ] = true
		case <-time.After(5 * time.Second):
			t.Fatal("takılan eş hızlı eşi geciktirdi")
		}
	}
	if !seen[msgTxs] || !seen[msgBlock] {
		t.Fatalf("hızlı eşe giden %v", seen)
	}
}

// Bir eşe aynı anda en çok bir mesaj gider (karşı düğümün tek çözümleme yuvası) ve kuyrukta
// bekleyen blok bekleyen işlem gruplarının önüne geçer.
func TestPeerSendOneInFlightBlocksFirst(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	peer := "127.0.0.1:1"
	var inflight, maxInflight atomic.Int32
	var mu sync.Mutex
	var order []string
	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	ps.send = func(_ string, data []byte) error {
		n := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		once.Do(func() {
			close(entered)
			<-gate
		})
		var m message
		json.Unmarshal(data, &m)
		mu.Lock()
		order = append(order, m.Type+m.Hash)
		mu.Unlock()
		time.Sleep(time.Millisecond)
		inflight.Add(-1)
		return nil
	}
	ps.AddPeer(peer)
	ps.broadcast(message{Type: msgTxs, Hash: "1"})
	<-entered // ilk grup gönderimde
	ps.broadcast(message{Type: msgTxs, Hash: "2"})
	ps.broadcast(message{Type: msgTxs, Hash: "3"})
	ps.broadcast(message{Type: msgBlock, Hash: "b"})
	close(gate)
	s := ps.senderFor(peer)
	waitFor(t, "kuyruk boşalmadı", s.idle)
	mu.Lock()
	first := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"txs1", "blockb", "txs2", "txs3"}
	if len(first) != len(want) {
		t.Fatalf("sıra %v, beklenen %v", first, want)
	}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("sıra %v, beklenen %v", first, want)
		}
	}

	var wg sync.WaitGroup // eşzamanlı yayınlar (blok ve işlem döngüleri ayrı goroutine'lerdir)
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				typ := msgTxs
				if (g+i)%3 == 0 {
					typ = msgBlock
				}
				ps.broadcast(message{Type: typ})
			}
		}(g)
	}
	wg.Wait()
	waitFor(t, "kuyruk boşalmadı", s.idle)
	if maxInflight.Load() > 1 {
		t.Fatalf("eşe aynı anda %d mesaj gitti", maxInflight.Load())
	}
}

// Ulaşılamayan eşte devre açılır; açıkken işlem grubu gönderilmez, blok denenir ve başarılı
// blok devreyi kapatır.
func TestPeerSendBreakerProbe(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	peer := "127.0.0.1:1"
	var failing atomic.Bool
	failing.Store(true)
	var calls atomic.Int32
	ps.send = func(string, []byte) error {
		calls.Add(1)
		if failing.Load() {
			return errors.New("ulaşılamadı")
		}
		return nil
	}
	ps.AddPeer(peer)
	s := ps.senderFor(peer)
	for i := 0; i < peerBreakerFails; i++ {
		ps.broadcast(message{Type: msgTxs})
	}
	waitFor(t, "devre açılmadı", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running && !s.openUntil.IsZero()
	})
	if n := calls.Load(); n != peerBreakerFails {
		t.Fatalf("%d gönderim, beklenen %d", n, peerBreakerFails)
	}
	ps.broadcast(message{Type: msgTxs})
	if !s.idle() {
		t.Fatal("devre açıkken işlem grubu için çalışan başlatıldı")
	}
	failing.Store(false)
	ps.broadcast(message{Type: msgBlock})
	waitFor(t, "blok yoklaması devreyi kapatmadı", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running && s.fails == 0 && s.openUntil.IsZero()
	})
	if n := calls.Load(); n != peerBreakerFails+1 {
		t.Fatalf("%d gönderim, beklenen %d", n, peerBreakerFails+1)
	}
}
