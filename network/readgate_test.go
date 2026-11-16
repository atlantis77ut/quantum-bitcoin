package network

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quantum-bitcoin/crypto"
)

func gateGet(h http.Handler, url, remote string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", url, nil)
	req.RemoteAddr = remote
	h.ServeHTTP(rec, req)
	return rec
}

// Genel sınır doluyken önbellekteki blok ve uç okunur, önbellekte olmayan eski blok 503 alır;
// yer açılınca aynı okuma geçer.
func TestOldBlockReadsGlobalCap(t *testing.T) {
	bc := newChain(t)
	mineN(t, bc, 4)
	api := NewAPIServer(":0", bc)
	h := api.Handler()
	const ip = "192.0.2.10:1000"

	if rec := gateGet(h, "/block?height=1", ip); rec.Code != http.StatusOK {
		t.Fatalf("ilk okuma %d", rec.Code)
	}
	for i := 0; i < blockMissSlots; i++ {
		api.missSlots <- struct{}{}
	}
	if rec := gateGet(h, "/block?height=1", ip); rec.Code != http.StatusOK {
		t.Fatalf("önbellekteki blok %d", rec.Code)
	}
	if rec := gateGet(h, "/latestblock", ip); rec.Code != http.StatusOK {
		t.Fatalf("uç genel sınırdan muaf olmalı: %d", rec.Code)
	}
	if n := len(api.tipSlots); n != 0 {
		t.Fatalf("uç yuvası geri verilmedi: %d", n)
	}
	rec := gateGet(h, "/block?height=2", ip)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("dolu sınırda eski blok %d", rec.Code)
	}
	<-api.missSlots
	if rec := gateGet(h, "/block?height=2", "192.0.2.11:1000"); rec.Code != http.StatusOK {
		t.Fatalf("yer açılınca %d", rec.Code)
	}
	if n := len(api.missSlots); n != blockMissSlots-1 {
		t.Fatalf("okuma yuvasını geri vermedi: %d dolu", n)
	}
}

// Farklı eski bloklarla dolaşan istemci kaçırma bedelini öder: jetonu bitince kaçırması 429
// alır, önbellekten gelen okuması ve başka istemciler sürer.
func TestOldBlockReadsPerClientCost(t *testing.T) {
	bc := newChain(t)
	mineN(t, bc, 6)
	api := NewAPIServer(":0", bc)
	now := time.Unix(1_700_000_000, 0)
	api.limiter.now = func() time.Time { return now }
	h := api.Handler()
	const ip = "192.0.2.20:1000"

	// Kova rateLimitBurst jetonla başlar; kaçırma 1 + blockMissCost harcar.
	allowed := rateLimitBurst / (1 + blockMissCost)
	for height := 1; height <= allowed; height++ {
		if rec := gateGet(h, fmt.Sprintf("/block?height=%d", height), ip); rec.Code != http.StatusOK {
			t.Fatalf("%d. kaçırma %d", height, rec.Code)
		}
	}
	rec := gateGet(h, fmt.Sprintf("/block?height=%d", allowed+1), ip)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("bedel bitince kaçırma %d", rec.Code)
	}
	if rec := gateGet(h, "/block?height=1", ip); rec.Code != http.StatusOK {
		t.Fatalf("önbellekten okuma %d", rec.Code)
	}
	if rec := gateGet(h, fmt.Sprintf("/block?height=%d", allowed+1), "192.0.2.21:1000"); rec.Code != http.StatusOK {
		t.Fatalf("başka istemci %d", rec.Code)
	}
	if n := len(api.missSlots); n != 0 {
		t.Fatalf("reddedilen kaçırmalar yuva tuttu: %d", n)
	}
}

// Uç kaçırması kendi tek yuvasını kullanır: yuva doluyken önbellekte olmayan uç 503 alır, genel
// yuvalar boş olsa da.
func TestTipReadSlot(t *testing.T) {
	bc := newChain(t)
	mineN(t, bc, 2)
	api := NewAPIServer(":0", bc)
	h := api.Handler()
	api.tipSlots <- struct{}{}
	if rec := gateGet(h, "/latestblock", "192.0.2.40:1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uç yuvası doluyken %d", rec.Code)
	}
	if n := len(api.missSlots); n != 0 {
		t.Fatalf("uç okuması genel yuva tuttu: %d", n)
	}
	<-api.tipSlots
	if rec := gateGet(h, "/latestblock", "192.0.2.40:1"); rec.Code != http.StatusOK {
		t.Fatalf("uç yuvası boşken %d", rec.Code)
	}
}

// /transaction ve /merkleproof aynı kapıdan geçer: önbellekte olmayan bloktaki işlem genel sınır
// doluyken 503 alır.
func TestOldBlockReadsTxEndpoints(t *testing.T) {
	bc := newChain(t)
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	old, err := bc.AddBlock(nil, kp.Address) // blok kabulü önbelleğe koymaz
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 2)
	api := NewAPIServer(":0", bc)
	h := api.Handler()
	for i := 0; i < blockMissSlots; i++ {
		api.missSlots <- struct{}{}
	}
	id := old.Transactions[0].ID()
	for _, url := range []string{"/transaction?id=" + id, "/merkleproof?id=" + id} {
		if rec := gateGet(h, url, "192.0.2.30:1"); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s dolu sınırda %d", url, rec.Code)
		}
	}
	<-api.missSlots
	for _, url := range []string{"/transaction?id=" + id, "/merkleproof?id=" + id} {
		if rec := gateGet(h, url, "192.0.2.30:1"); rec.Code != http.StatusOK {
			t.Fatalf("%s yer açılınca %d", url, rec.Code)
		}
	}
}
