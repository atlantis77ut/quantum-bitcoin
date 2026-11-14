package network

import (
	"bytes"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Hat bayt baytı sıraya koyar: boş hatta çıkış n/hız sonra, dolu hatta öncekinin ardından.
func TestLinkLimiterReserve(t *testing.T) {
	l := newLinkLimiter(8) // 1 MB/sn
	now := time.Now()
	if got := l.reserve(500_000, now).Sub(now); got != 500*time.Millisecond {
		t.Fatalf("ilk parça %v", got)
	}
	if got := l.reserve(250_000, now).Sub(now); got != 750*time.Millisecond {
		t.Fatalf("kuyruktaki parça %v", got)
	}
	later := now.Add(2 * time.Second)
	if got := l.reserve(125_000, later).Sub(later); got != 125*time.Millisecond {
		t.Fatalf("boşalmış hat %v", got)
	}
}

// Proxy veriyi bozmadan, hat hızından hızlı olmadan ve gecikmeyle iletir; yanıt da geri gelir.
func TestThrottleProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		data, _ := io.ReadAll(c)
		got <- data
		c.Write([]byte("ok"))
	}()
	const mbit, latency = 8.0, 50 * time.Millisecond // 1 MB/sn
	p, err := newThrottleProxy(ln.Addr().String(), newLinkLimiter(mbit), nil, latency)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("qbtc"), 64<<10) // 256 KiB → ~0,26 sn
	start := time.Now()
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	c.(*net.TCPConn).CloseWrite()
	reply, err := io.ReadAll(c)
	elapsed := time.Since(start)
	if err != nil || string(reply) != "ok" {
		t.Fatalf("yanıt %q, %v", reply, err)
	}
	if data := <-got; !bytes.Equal(data, payload) {
		t.Fatalf("iletilen %d bayt, gönderilen %d", len(data), len(payload))
	}
	if want := 250*time.Millisecond + 2*latency; elapsed < want {
		t.Fatalf("iletim %v sürdü, hat en az %v ister", elapsed, want)
	}
}

// Yüksek hız·gecikmede gecikme hattı hızı düşürmez: 4 MiB, 100 Mbit ve 200 ms'de ~0,54 sn
// sürer; sabit 16 parçalık kuyrukla (16 parça / 200 ms ≈ 1,3 MB/sn) ~3,2 sn sürerdi.
func TestThrottleProxyHighBDP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		n, _ := io.Copy(io.Discard, c)
		got <- int(n)
	}()
	const mbit, latency = 100.0, 200 * time.Millisecond
	p, err := newThrottleProxy(ln.Addr().String(), newLinkLimiter(mbit), nil, latency)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := make([]byte, 4<<20)
	start := time.Now()
	if _, err := c.Write(payload); err != nil {
		t.Fatal(err)
	}
	c.(*net.TCPConn).CloseWrite()
	if n := <-got; n != len(payload) {
		t.Fatalf("iletilen %d bayt", n)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("iletim %v sürdü; kuyruk hız·gecikmeyi taşımıyor", elapsed)
	}
}

func TestPropConfigValidate(t *testing.T) {
	ok := PropConfig{Txs: 10, Hops: 2, Topology: TopologyLine, Mbit: 10, Latency: time.Millisecond,
		Timeout: time.Second, Dir: "x"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*PropConfig){
		func(c *PropConfig) { c.Txs = 1001 },
		func(c *PropConfig) { c.Hops = 0 },
		func(c *PropConfig) { c.Topology = "ring" },
		func(c *PropConfig) { c.Topology, c.Hops = TopologyStarLeaf, 1 },
		func(c *PropConfig) { c.Mbit = 0 },
		func(c *PropConfig) { c.Mbit = maxPropMbit + 1 },
		func(c *PropConfig) { c.Latency = maxPropLatency + 1 },
		func(c *PropConfig) { c.Timeout = 0 },
		func(c *PropConfig) { c.Dir = "" },
	}
	for i, mut := range bad {
		c := ok
		mut(&c)
		if err := c.Validate(); !errors.Is(err, errPropConfig) {
			t.Fatalf("bozuk ayar %d kabul edildi: %v", i, err)
		}
	}
}

// Küçük bir blokla iki katman uçtan uca: blok ölçümü taze zincirde kabul eder; blok her
// topolojide kazıcı dışındaki bütün düğümlere yalnızca itmeyle ulaşır.
func TestPropagationBench(t *testing.T) {
	dir := t.TempDir()
	f, err := NewPropFixture(dir, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Block.Transactions) != 6 || f.WireBytes < 5*5000 {
		t.Fatalf("blok %d işlem, %d bayt", len(f.Block.Transactions), f.WireBytes)
	}
	bt, err := f.BenchBlock(filepath.Join(dir, "block"))
	if err != nil || bt.Accept <= 0 {
		t.Fatalf("blok ölçümü %+v, %v", bt, err)
	}
	for i, topo := range []string{TopologyLine, TopologyStarLeaf, TopologyStarHub} {
		c := PropConfig{Txs: 5, Hops: 2, Topology: topo, Mbit: 100, Latency: 2 * time.Millisecond,
			Timeout: 30 * time.Second, Dir: filepath.Join(dir, "run", string(rune('a'+i)))}
		run, err := f.BenchPropagation(c)
		if err != nil {
			t.Fatalf("%s: %v", topo, err)
		}
		if len(run.Arrival) != 2 {
			t.Fatalf("%s: %d düğüm ölçüldü", topo, len(run.Arrival))
		}
		for k, d := range run.Arrival {
			if d < 0 {
				t.Fatalf("%s: blok %d. düğüme ulaşmadı", topo, k+1)
			}
		}
		if topo == TopologyLine && run.Arrival[1] < run.Arrival[0] {
			t.Fatalf("hatta ikinci düğüm birinciden önce: %v", run.Arrival)
		}
	}
}
