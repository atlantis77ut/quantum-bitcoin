//go:build linux

package network

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Vekil göndericiden hattın önünde en çok küçük bir pay okur: göndericinin çekirdeğinde
// onaylanmış bayt (yazılan - onaylanmamış), hattan çıkabilecek bayttan alma tamponu, okuma payı,
// gecikme hattı ve bir yazma parçası kadar fazladır. Eski vekil (önden 16 parçalık kuyruk ve
// ayarlanmamış alma tamponu) bunun birkaç katını onaylatıyordu.
func TestThrottleProxyPacedRead(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		io.Copy(io.Discard, c)
	}()
	const mbit, latency = 8.0, 20 * time.Millisecond // 1 MB/sn
	lim := newLinkLimiter(mbit)
	p, err := newThrottleProxy(ln.Addr().String(), lim, nil, latency)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	start := time.Now()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tc := c.(*net.TCPConn)

	const piece = 16 << 10
	var written atomic.Int64
	go func() {
		buf := make([]byte, piece)
		for written.Load() < 4<<20 {
			n, err := c.Write(buf)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	bound := int64(2*proxyRcvBuf) + int64(lim.readAhead().Seconds()*lim.bps) +
		int64(lim.bps*latency.Seconds()) + piece
	var maxAhead, acked int64
	for time.Since(start) < 1500*time.Millisecond {
		time.Sleep(50 * time.Millisecond)
		w := written.Load()
		q, ok := unackedBytes(tc)
		if !ok {
			t.Fatal("kuyruk okunamadı")
		}
		acked = w - int64(q)
		line := int64(time.Since(start).Seconds() * lim.bps) // hattan çıkabilecek en çok bayt
		maxAhead = max(maxAhead, acked-line)
	}
	if maxAhead > bound {
		t.Fatalf("vekil hattın %d bayt önünden onaylattı, sınır %d", maxAhead, bound)
	}
	if acked < int64(lim.bps/2) {
		t.Fatalf("1,5 sn'de yalnızca %d bayt geçti; vekil hattı boşta bırakıyor", acked)
	}
	t.Logf("hattın önünde en çok %d bayt (sınır %d), %d bayt geçti", maxAhead, bound, acked)
}
