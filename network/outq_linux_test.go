//go:build linux

package network

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// Okumayan alıcıda onaylanmamış kuyruk dolu kalır, alıcı okuyunca boşalır; kapanan
// bağlantıda okunmaz.
func TestUnackedBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := <-accepted
	if r == nil {
		t.Fatal("bağlantı kabul edilmedi")
	}
	defer r.Close()
	tc := c.(*net.TCPConn)

	if err := c.SetWriteDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, err := c.Write(make([]byte, 32<<20)) // iki uçtaki tamponlardan çok büyük
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("okumayan alıcıya yazma takılmadı: %d bayt, %v", n, err)
	}
	q1, ok := unackedBytes(tc)
	if !ok || q1 <= 0 {
		t.Fatalf("okumayan alıcıda kuyruk %d (%v)", q1, ok)
	}
	time.Sleep(200 * time.Millisecond)
	if q2, ok := unackedBytes(tc); !ok || q2 <= 0 {
		t.Fatalf("okumayan alıcıda kuyruk boşaldı: %d -> %d (%v)", q1, q2, ok)
	}

	go io.CopyN(io.Discard, r, int64(n))
	deadline := time.Now().Add(5 * time.Second)
	for {
		q, ok := unackedBytes(tc)
		if !ok {
			t.Fatal("kuyruk okunamadı")
		}
		if q == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alıcı okuduktan sonra kuyruk boşalmadı: %d", q)
		}
		time.Sleep(10 * time.Millisecond)
	}

	c.Close()
	if _, ok := unackedBytes(tc); ok {
		t.Fatal("kapanan bağlantının kuyruğu okundu")
	}
}

// Bloğu okuyup kapatmayan alıcının yuvası teslim süresinin (sendMax) sonunu beklemeden, kuyruk
// boşaldıktan stall sonra devralınır; itme yine süresi dolunca errNotDelivered ile biter.
func TestNonClosingReceiverSlotTakenOver(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.pushSlots = newPushSlots(1)
	ps.pushLim.stall = 200 * time.Millisecond
	ps.pushLim.sendMax = 5 * time.Second
	addr := deliveryReceiver(t, -1)
	first := make(chan error, 1)
	start := time.Now()
	go func() { first <- ps.pushOnce(addr, []byte(`{"type":"block"}`+"\n"), pushID{}, true) }()
	for draining := false; !draining; {
		if time.Since(start) > 3*time.Second {
			t.Fatal("ilk itme teslim beklemeye geçmedi")
		}
		ps.pushSlots.mu.Lock()
		h, ok := ps.pushSlots.holders[addr]
		draining = ok && h.draining
		ps.pushSlots.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	ps.pushSlots.acquire("ikinci", &ps.pushLim)
	got := time.Since(start)
	ps.pushSlots.release("ikinci")
	if got > 2500*time.Millisecond {
		t.Fatalf("kapatmayan alıcının yuvası %v sonra devralındı (sendMax %v)", got, ps.pushLim.sendMax)
	}
	if err := <-first; !errors.Is(err, errNotDelivered) {
		t.Fatalf("ilk itme: %v, beklenen errNotDelivered", err)
	}
}
