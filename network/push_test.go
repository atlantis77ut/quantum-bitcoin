package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Okuma süresi üç sınırın en erkenidir: canlılık, asgari ortalama hız (ilk recvGrace bedava)
// ve tavan.
func TestReadDeadline(t *testing.T) {
	l := pushLimits{recvGrace: 10 * time.Second, recvMinRate: 1000, recvMax: 60 * time.Second}
	t0 := time.Unix(1_900_000_000, 0)
	cases := []struct {
		now  time.Duration
		n    int64
		want time.Duration
	}{
		{0, 0, 10 * time.Second},                      // ilk süre bedava
		{5 * time.Second, 0, 10 * time.Second},        // veri yok: başlangıç + grace
		{5 * time.Second, 20_000, 15 * time.Second},   // canlılık: şimdi + grace
		{20 * time.Second, 15_000, 25 * time.Second},  // hız: başlangıç + grace + n/hız
		{55 * time.Second, 100_000, 60 * time.Second}, // tavan
	}
	for _, c := range cases {
		if got := l.readDeadline(t0, t0.Add(c.now), c.n); !got.Equal(t0.Add(c.want)) {
			t.Fatalf("now=%v n=%d: %v, beklenen %v", c.now, c.n, got.Sub(t0), c.want)
		}
	}
}

// pipeRead lim ile okur; karşı taraf her adımda chunk bayt yazıp gap bekler.
func pipeRead(lim *pushLimits, chunk, steps int, gap time.Duration) (int64, time.Duration, error) {
	a, b := net.Pipe()
	defer a.Close()
	go func() {
		defer b.Close()
		buf := make([]byte, chunk)
		for i := 0; i < steps; i++ {
			if _, err := b.Write(buf); err != nil {
				return
			}
			time.Sleep(gap)
		}
	}()
	r := &rateReader{conn: a, lim: lim, start: time.Now()}
	n, err := io.Copy(io.Discard, r)
	return n, time.Since(r.start), err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Asgari hızın üstündeki gönderim recvGrace'ten uzun sürse de biter; takılan, hızın altında
// kalan ve tavanı aşan gönderim zaman aşımıyla kesilir ve bozuk mesaj sayılmaz.
func TestRateReader(t *testing.T) {
	lim := &pushLimits{recvGrace: 300 * time.Millisecond, recvMinRate: 64 << 10, recvMax: 3 * time.Second}

	n, d, err := pipeRead(lim, 16<<10, 20, 50*time.Millisecond) // ~320 KiB/sn, ~1 sn
	if err != nil || n != 20*16<<10 || d <= lim.recvGrace {
		t.Fatalf("hızlı gönderim: %d bayt, %v, %v", n, d, err)
	}

	n, d, err = pipeRead(lim, 1<<10, 2, 2*time.Second) // takılma
	if !isTimeout(err) || malformedMessage(err) || n != 1<<10 || d > 1500*time.Millisecond {
		t.Fatalf("takılan gönderim: %d bayt, %v, %v", n, d, err)
	}

	n, d, err = pipeRead(lim, 1<<10, 100, 50*time.Millisecond) // ~20 KiB/sn, 5 sn sürerdi
	if !isTimeout(err) || n >= 100<<10 || d > 2*time.Second {
		t.Fatalf("yavaş gönderim: %d bayt, %v, %v", n, d, err)
	}

	capped := &pushLimits{recvGrace: 300 * time.Millisecond, recvMinRate: 64 << 10, recvMax: 500 * time.Millisecond}
	n, d, err = pipeRead(capped, 16<<10, 40, 50*time.Millisecond) // hızlı ama 2 sn sürerdi
	if !isTimeout(err) || d < 450*time.Millisecond || d > 1500*time.Millisecond {
		t.Fatalf("tavan: %d bayt, %v, %v", n, d, err)
	}
}

// Gerçek bağlantıda: okuma süresine takılan eş geri çevrilmez; aynı eşin recvGrace'ten uzun
// süren ama asgari hızın üstündeki blok itmesi kabul edilir.
func TestSlowBlockPush(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ps.pushLim.recvGrace = 300 * time.Millisecond
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	src := newChain(t)
	mineN(t, src, 1)
	blk, err := src.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(stamped(message{Type: msgBlock, Block: blk}))
	if err != nil {
		t.Fatal(err)
	}
	// ~20 adımda 40 ms arayla: toplam ~800 ms > recvGrace; hız sınırı gönderim hızının yarısı.
	piece := max(1, len(data)/20)
	ps.pushLim.recvMinRate = int64(piece) * 25 / 2
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(data[:len(data)/2]); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !isConnReset(err) {
		t.Fatalf("takılan bağlantı kesilmedi: %v", err)
	}
	id := peerIDOf(conn.LocalAddr())
	conn.Close()
	if ps.prot.isDiscouraged(id) {
		t.Fatal("okuma süresine takılan eş geri çevrildi")
	}

	conn, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	for off := 0; off < len(data); off += piece {
		if _, err := conn.Write(data[off:min(off+piece, len(data))]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	if time.Since(start) <= ps.pushLim.recvGrace {
		t.Fatal("gönderim recvGrace'ten kısa sürdü; test anlamsız")
	}
	waitTip(t, bc, blk.Hash)
}

// tick peer yuvasının ilerlemesini stop kapanana kadar her 20 ms'de işler.
func tick(s *pushSlots, peer string, n int, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				s.progress(peer, n)
			}
		}
	}()
}

func (s *pushSlots) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.holders)
}

// Tutanlar ilerledikçe bekleyen başlamaz; biri takılınca bekleyenlerden yalnızca biri onun
// yuvasını devralır, yuva bırakılınca bir sonraki başlar.
func TestPushSlotsTakeover(t *testing.T) {
	l := defaultPushLimits()
	l.stall = 200 * time.Millisecond
	l.sendChunk = time.Hour // hız kuralı devre dışı
	s := newPushSlots(2)
	s.acquire("a", &l)
	s.acquire("b", &l)
	stop, stopB := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	tick(s, "a", 1, stop, &wg)
	tick(s, "b", 1, stopB, &wg)

	started := make(chan string, 4)
	for i := 0; i < 4; i++ {
		peer := fmt.Sprintf("w%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.acquire(peer, &l)
			started <- peer
			tick(s, peer, 1, stop, &wg)
		}()
	}
	time.Sleep(500 * time.Millisecond)
	if len(started) != 0 {
		t.Fatal("tutanlar ilerlerken bekleyen başladı")
	}
	close(stopB)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("takılan tutanın yuvası devralınmadı")
	}
	time.Sleep(500 * time.Millisecond)
	if len(started) != 0 || s.count() != 2 {
		t.Fatalf("tek takılan tutan için %d bekleyen daha başladı, %d tutan", len(started), s.count())
	}
	s.release("a")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("bırakılan yuva alınmadı")
	}
	close(stop) // kalanlar takılmayla sırayla başlar
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("bekleyenler başlamadı")
		}
	}
	wg.Wait()
}

// Dürüst alıcının isteyeceği hızın gerisinde kalan tutanın yuvası devralınır; hızın üstündeki
// tutanınki devralınmaz.
func TestPushSlotsPace(t *testing.T) {
	l := defaultPushLimits()
	l.stall = time.Hour
	l.sendChunk = 300 * time.Millisecond
	l.recvMinRate = 1 << 10
	var wg sync.WaitGroup
	stop := make(chan struct{})
	defer func() { close(stop); wg.Wait() }()

	fast := newPushSlots(1)
	fast.acquire("fast", &l)
	tick(fast, "fast", 200, stop, &wg) // ~10 KB/sn
	got := make(chan struct{})
	go func() { fast.acquire("next", &l); close(got) }()
	select {
	case <-got:
		t.Fatal("hızlı tutanın yuvası devralındı")
	case <-time.After(500 * time.Millisecond):
	}
	fast.release("fast")
	<-got

	slow := newPushSlots(1)
	slow.acquire("slow", &l)
	tick(slow, "slow", 10, stop, &wg) // ~500 B/sn
	start := time.Now()
	slow.acquire("next", &l)
	if d := time.Since(start); d < 300*time.Millisecond || d > 3*time.Second {
		t.Fatalf("yavaş tutanın yuvası %v sonra devralındı", d)
	}
}

// Blok gövdesi aynı anda en çok blockPushSlots eşe itilir; işlem grupları yuva beklemez.
func TestBlockPushSlotsLimitConcurrency(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	var inflight, maxInflight, calls atomic.Int32
	ps.send = func(peer string, data []byte) error {
		calls.Add(1)
		n := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if n <= m || maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		for i := 0; i < 10; i++ {
			ps.pushSlots.progress(peer, writeChunk)
			time.Sleep(10 * time.Millisecond)
		}
		inflight.Add(-1)
		return nil
	}
	peers := []string{"127.0.0.1:1", "127.0.0.2:1", "127.0.0.3:1", "127.0.0.4:1"}
	for _, p := range peers {
		ps.AddPeer(p)
	}
	ps.broadcast(message{Type: msgBlock})
	for _, p := range peers {
		waitFor(t, "kuyruk boşalmadı", ps.senderFor(p).idle)
	}
	if calls.Load() != 4 || maxInflight.Load() > blockPushSlots {
		t.Fatalf("%d gönderim, aynı anda en çok %d", calls.Load(), maxInflight.Load())
	}

	ps2 := NewP2PServer("127.0.0.1:0", newChain(t))
	sent := make(chan struct{}, 1)
	ps2.send = func(string, []byte) error { sent <- struct{}{}; return nil }
	ps2.pushSlots.acquire("x", &ps2.pushLim)
	ps2.pushSlots.acquire("y", &ps2.pushLim)
	ps2.AddPeer("127.0.0.5:1")
	ps2.broadcast(message{Type: msgTxs})
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("işlem grubu blok yuvası bekledi")
	}
}

// freeAddr kapatılmış bir dinleyicinin adresidir (bağlantı reddedilir).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// Gerçek soketlerde: bağlantı kurulamaması ve alıcının okumadan kapatması erken, alıcı bir
// kısmını okuduktan sonra kopma geç başarısızlıktır. Aynı makinede çekirdek tamponları okumayan
// alıcıda ~2,5 MB'ı, okuyan alıcıda çok daha fazlasını bulabildiğinden eşik ve mesaj büyütülür.
func TestSendToEarlyAndLate(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.pushLim.earlyBytes = 6 << 20
	if err := ps.sendTo(freeAddr(t), []byte("x")); !errors.Is(err, errPushEarly) {
		t.Fatalf("bağlantı hatası erken sayılmadı: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	big := make([]byte, 64<<20)
	serve := func(read int64) {
		go func() {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if read < 0 {
				io.Copy(io.Discard, c)
			} else {
				io.CopyN(io.Discard, c, read)
			}
			c.Close()
		}()
	}

	serve(0) // yuvası dolu alıcı: okumadan kapatır
	if err := ps.sendTo(addr, big); !errors.Is(err, errPushEarly) {
		t.Fatalf("okunmadan kapanan bağlantı erken sayılmadı: %v", err)
	}
	serve(8 << 20)
	if err := ps.sendTo(addr, big); err == nil || errors.Is(err, errPushEarly) {
		t.Fatalf("kısmen okunan bağlantı geç sayılmadı: %v", err)
	}
	serve(-1)
	if err := ps.sendTo(addr, big); err != nil {
		t.Fatalf("tam okunan gönderim: %v", err)
	}
}

// Okumadan kapatılan ilk itmeden sonra blok retryDelay sonra yeniden itilir ve ulaşır.
func TestBlockPushRetried(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.pushLim.retryDelay = 50 * time.Millisecond
	ps.pushLim.earlyBytes = 16 << 20 // aynı makinede çekirdek tamponları büyüktür
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int64, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		c.Close() // ilk itme: yuvası dolu alıcı
		c, err = ln.Accept()
		if err != nil {
			return
		}
		n, _ := io.Copy(io.Discard, c)
		c.Close()
		got <- n
	}()
	peer := ln.Addr().String()
	ps.AddPeer(peer)
	data := make([]byte, 8<<20) // tamponlara sığmaz: ilk itme hata alır
	ps.enqueueSend(peer, data, true, time.Now())
	select {
	case n := <-got:
		if n != int64(len(data)) {
			t.Fatalf("yeniden itmede %d bayt ulaştı", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("blok yeniden itilmedi")
	}
	s := ps.senderFor(peer)
	waitFor(t, "kuyruk boşalmadı", s.idle)
	if !s.healthy() {
		t.Fatal("başarılı yeniden itmeden sonra eş sağlıksız")
	}
}

// Erken başarısızlık bir kez yeniden denenir; geç başarısızlık, sağlıksız eş ve kuyrukta daha
// yeni blok varken yeniden denenmez.
func TestBlockPushRetryRules(t *testing.T) {
	run := func(t *testing.T, fails int, first error, newer bool) []string {
		t.Helper()
		ps := NewP2PServer("127.0.0.1:0", newChain(t))
		ps.pushLim.retryDelay = time.Second
		peer := "127.0.0.1:1"
		var mu sync.Mutex
		var calls []string
		firstCall := make(chan struct{})
		ps.send = func(_ string, data []byte) error {
			var m message
			json.Unmarshal(data, &m)
			mu.Lock()
			calls = append(calls, m.Hash)
			n := len(calls)
			mu.Unlock()
			if n == 1 {
				close(firstCall)
				return first
			}
			return nil
		}
		ps.AddPeer(peer)
		s := ps.senderFor(peer)
		s.mu.Lock()
		s.fails = fails
		s.mu.Unlock()
		ps.broadcast(message{Type: msgBlock, Hash: "1"})
		<-firstCall
		if newer {
			ps.broadcast(message{Type: msgBlock, Hash: "2"})
		}
		time.Sleep(50 * time.Millisecond) // sendLoop ilk sonucu işlesin
		waitFor(t, "kuyruk boşalmadı", s.idle)
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
	early := fmt.Errorf("%w: test", errPushEarly)
	for _, c := range []struct {
		name  string
		fails int
		first error
		newer bool
		want  string
	}{
		{"erken", 0, early, false, "[1 1]"},
		{"geç", 0, errors.New("geç"), false, "[1]"},
		{"sağlıksız", 1, early, false, "[1]"},
		{"daha yeni blok", 0, early, true, "[1 2]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := fmt.Sprint(run(t, c.fails, c.first, c.newer)); got != c.want {
				t.Fatalf("gönderimler %s, beklenen %s", got, c.want)
			}
		})
	}
}

// İndirildiği eşe blok geri itilmez; kaynak yayın kuyruğunda taşınır.
func TestRelaySkipsSource(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	got := make(chan string, 4)
	ps.send = func(peer string, _ []byte) error { got <- peer; return nil }
	a, b := "127.0.0.2:1", "127.0.0.3:1"
	ps.AddPeer(a)
	ps.AddPeer(b)
	ps.broadcastFrom(message{Type: msgBlock}, a)
	select {
	case p := <-got:
		if p != b {
			t.Fatalf("blok %s eşine gitti", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blok diğer eşe gitmedi")
	}
	waitFor(t, "kuyruk boşalmadı", ps.senderFor(b).idle)
	if len(got) != 0 {
		t.Fatal("blok kaynağına geri itildi")
	}

	blk, err := ps.Blockchain.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	ps.relayFrom(blk, a)
	if it := <-ps.relayCh; it.b != blk || it.from != a {
		t.Fatalf("yayın kuyruğunda kaynak %q", it.from)
	}
}
