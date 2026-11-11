package network

import (
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// headerServer src zincirini sunan ve getheaders isteklerini sayan basit bir eştir.
func headerServer(t *testing.T, src *core.Blockchain) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req message
			if json.NewDecoder(conn).Decode(&req) == nil {
				switch req.Type {
				case msgGetHeaders:
					n.Add(1)
					json.NewEncoder(conn).Encode(message{Type: msgHeaders, Headers: src.HeadersAfter(req.Locator, core.MaxHeadersPerMsg)})
				case msgGetBlock:
					b, _ := src.BlockByHash(req.Hash)
					json.NewEncoder(conn).Encode(message{Type: msgBlock, Block: b})
				}
			}
			conn.Close()
		}
	}()
	return ln.Addr().String(), &n
}

// Az işli, pencereden uzun bir zincir sunan eşle başarısız ön-senkronizasyon erteleme
// başlatır; ertelemede tur tek başlık isteğiyle biter ve ön-senkronizasyon başlamaz; süre
// dolunca yeniden denenir ve erteleme ikiye katlanır, en çok presyncBackoffMax olur.
func TestPresyncBackoff(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 20)
	addr, gets := headerServer(t, src)

	dst := newChain(t)
	mineN(t, dst, 25)
	ps := NewP2PServer("127.0.0.1:0", dst)
	ps.syncWindow = 8
	stop := make(chan struct{})
	defer close(stop)

	if err := ps.syncFrom(addr, stop); !errors.Is(err, errPresyncEnd) {
		t.Fatalf("ilk tur: %v, beklenen errPresyncEnd", err)
	}
	pen := ps.presyncBackoff[addr]
	if ps.presyncs[addr] != nil || pen == nil || pen.delay != presyncBackoffMin || !ps.presyncBackedOff(addr) {
		t.Fatalf("erteleme %+v", pen)
	}

	gets.Store(0)
	if err := ps.syncFrom(addr, stop); !errors.Is(err, errPresyncBackoff) {
		t.Fatalf("ertelemede tur: %v, beklenen errPresyncBackoff", err)
	}
	if n := gets.Load(); n != 1 || ps.presyncs[addr] != nil {
		t.Fatalf("ertelemede %d başlık isteği, ön-senkronizasyon %v", n, ps.presyncs[addr] != nil)
	}

	pen.until = time.Now().Add(-time.Second)
	if err := ps.syncFrom(addr, stop); !errors.Is(err, errPresyncEnd) {
		t.Fatalf("süre dolunca: %v, beklenen errPresyncEnd", err)
	}
	if d := ps.presyncBackoff[addr].delay; d != 2*presyncBackoffMin {
		t.Fatalf("ikinci erteleme %v", d)
	}
	ps.presyncBackoff[addr] = &presyncPenalty{until: time.Now().Add(-time.Second), delay: presyncBackoffMax}
	if err := ps.syncFrom(addr, stop); !errors.Is(err, errPresyncEnd) {
		t.Fatal(err)
	}
	if d := ps.presyncBackoff[addr].delay; d != presyncBackoffMax {
		t.Fatalf("erteleme tavanı aşıldı: %v", d)
	}
	if dst.TipHash() == src.TipHash() {
		t.Fatal("az işli zincire geçildi")
	}
}

// Pencereyi doldurmayan ama uzun, ucun işini geçmeyen yanıt ön-senkronizasyona girmeden
// ertelemeye alınır; ertelemede yeniden ceza katlanmaz.
func TestLowWorkHeadersBackoff(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 20)
	addr, _ := headerServer(t, src)

	dst := newChain(t)
	mineN(t, dst, 25)
	ps := NewP2PServer("127.0.0.1:0", dst)
	ps.syncWindow = 30 // 20 başlık pencereyi doldurmaz ama sınırı (15) aşar
	stop := make(chan struct{})
	defer close(stop)
	for i := 0; i < 2; i++ {
		if err := ps.syncFrom(addr, stop); !errors.Is(err, errLowWorkHeaders) {
			t.Fatalf("tur %d: %v, beklenen errLowWorkHeaders", i, err)
		}
		if pen := ps.presyncBackoff[addr]; pen == nil || pen.delay != presyncBackoffMin || ps.presyncs[addr] != nil {
			t.Fatalf("tur %d: erteleme %+v", i, pen)
		}
	}
}

// Ertelemedeki eşten ucumuzu uzatan bloklar olağan senkronizasyonla gelir.
func TestPresyncBackoffKeepsExtension(t *testing.T) {
	dst := newChain(t)
	mineN(t, dst, 5)
	src := newChain(t)
	hs, _, _ := dst.HeadersRange(1, 5)
	for _, h := range hs {
		b, err := dst.BlockByHash(h.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if err := src.AcceptBlock(b); err != nil {
			t.Fatal(err)
		}
	}
	mineN(t, src, 3)
	addr, _ := headerServer(t, src)

	ps := NewP2PServer("127.0.0.1:0", dst)
	ps.presyncBackoff[addr] = &presyncPenalty{until: time.Now().Add(time.Hour), delay: time.Hour}
	if err := ps.syncFrom(addr, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	if dst.TipHash() != src.TipHash() {
		t.Fatal("ertelemedeki eşten uç uzatılmadı")
	}
	if ps.presyncBackoff[addr] == nil {
		t.Fatal("ucu uzatmak ertelemeyi sıfırladı")
	}
}

// Başarılı ön-senkronizasyon (derin dal benimsendi) ertelemeyi sıfırlar.
func TestPresyncSuccessClearsBackoff(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 30)
	addr, _ := headerServer(t, src)

	dst := newChain(t)
	mineN(t, dst, 25)
	ps := NewP2PServer("127.0.0.1:0", dst)
	ps.syncWindow = 8
	ps.presyncBackoff[addr] = &presyncPenalty{until: time.Now().Add(-time.Second), delay: time.Hour}
	stop := make(chan struct{})
	defer close(stop)
	for i := 0; dst.TipHash() != src.TipHash(); i++ {
		if i == 20 {
			t.Fatalf("derin dal benimsenmedi: uç %s", dst.TipHash())
		}
		ps.syncFrom(addr, stop) // ön-senkronizasyon son başlıkta biter; kalan iş sonraki turda
	}
	if ps.presyncBackoff[addr] != nil {
		t.Fatalf("başarılı ön-senkronizasyon ertelemeyi sıfırlamadı: %+v", ps.presyncBackoff[addr])
	}
}
