package network

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

func newChain(t *testing.T) *core.Blockchain {
	t.Helper()
	bc, err := core.NewBlockchain(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	return bc
}

func TestBalanceEndpoint(t *testing.T) {
	bc := newChain(t)
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, kp.Address); err != nil {
		t.Fatal(err)
	}
	h := NewAPIServer(":0", bc).Handler()

	cases := []struct {
		method, url string
		want        int
	}{
		{"GET", "/balance?address=" + kp.Address, http.StatusOK},
		{"GET", "/balance", http.StatusBadRequest},
		{"GET", "/balance?address=bal_x", http.StatusBadRequest},
		{"POST", "/balance?address=" + kp.Address, http.StatusMethodNotAllowed},
		{"GET", "/latestblock", http.StatusOK},
		{"GET", "/nope", http.StatusNotFound},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.url, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, beklenen %d", c.method, c.url, rec.Code, c.want)
		}
	}

	req := httptest.NewRequest("GET", "/balance?address="+kp.Address, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Balance     int64
		BalanceQBTC string `json:"balance_qbtc"`
		Decimals    int
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Balance != core.BlockReward {
		t.Fatalf("bakiye %d, hata %v", body.Balance, err)
	}
	if body.BalanceQBTC != "50.00000000" || body.Decimals != 8 {
		t.Fatalf("biçimli bakiye %q, basamak %d", body.BalanceQBTC, body.Decimals)
	}
}

func TestRateLimit(t *testing.T) {
	bc := newChain(t)
	h := NewAPIServer(":0", bc).Handler()
	limited := false
	for i := 0; i < rateLimitBurst+5; i++ {
		req := httptest.NewRequest("GET", "/latestblock", nil)
		req.RemoteAddr = "192.0.2.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Fatal("oran sınırı devreye girmedi")
	}
	// Farklı IP etkilenmemeli.
	req := httptest.NewRequest("GET", "/latestblock", nil)
	req.RemoteAddr = "192.0.2.2:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("başka IP engellendi: %d", rec.Code)
	}
}

func TestServerTimeoutsConfigured(t *testing.T) {
	s := NewAPIServer(":0", newChain(t)).server
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatal("HTTP zaman aşımları ayarlı değil")
	}
	if s.Handler == nil {
		t.Fatal("DefaultServeMux kullanılıyor")
	}
}

// Açık 2 (ağ yolu): eşten gelen sahte coinbase'li blok zincire girmemeli.
func TestP2PRejectsForgedCoinbase(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	defer ln.Close()

	mallory, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	prev, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	forged := &core.Block{
		Index:     prev.Index + 1,
		Timestamp: prev.Timestamp + 1,
		PrevHash:  prev.Hash,
		Transactions: []core.Transaction{{
			Sender: core.CoinbaseSender, Receiver: mallory.Address, Amount: 21_000_000, IsCoinbase: true,
		}},
	}
	forged.MineBlock(1)

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(forged); err != nil {
		t.Fatal(err)
	}
	conn.Close()

	// Bir de bozuk/garip veri gönder: düğüm çökmemeli.
	if c2, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		c2.Write([]byte(strings.Repeat("{", 1000)))
		c2.Close()
	}

	time.Sleep(300 * time.Millisecond)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	if tip.Index != prev.Index {
		t.Fatal("sahte blok zincire eklendi")
	}
	if b, _ := bc.GetBalance(mallory.Address); b != 0 {
		t.Fatalf("saldırgan bakiyesi %d", b)
	}
}

func TestP2PAcceptsValidBlock(t *testing.T) {
	dst := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", dst)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	defer ln.Close()

	miner, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	prev, _ := dst.GetLatestBlock()
	b := &core.Block{
		Index: prev.Index + 1, Timestamp: prev.Timestamp + 1, PrevHash: prev.Hash,
		Transactions: []core.Transaction{{Sender: core.CoinbaseSender, Receiver: miner.Address, Amount: core.BlockReward, IsCoinbase: true}},
	}
	b.MineBlock(1)

	ps.AddPeer(ln.Addr().String())
	ps.BroadcastBlock(b)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tip, _ := dst.GetLatestBlock(); tip.Hash == b.Hash {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("geçerli blok eşten kabul edilmedi")
}

func TestClientKeyIPv6Prefix(t *testing.T) {
	a := clientKey("2001:db8:1:2:aaaa::1")
	b := clientKey("2001:db8:1:2:bbbb::9")
	c := clientKey("2001:db8:1:3::1")
	if a != b || a == c {
		t.Fatalf("IPv6 /64 anahtarı yanlış: %s %s %s", a, b, c)
	}
	if clientKey("192.0.2.7") != "192.0.2.7" {
		t.Fatal("IPv4 anahtarı değişti")
	}
}

func TestP2PPerIPConnLimit(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	for i := 0; i < maxConnsPerIP; i++ {
		if !ps.acquireIP("198.51.100.1") {
			t.Fatalf("%d. bağlantı reddedildi", i+1)
		}
	}
	if ps.acquireIP("198.51.100.1") {
		t.Fatal("IP başına sınır aşıldı")
	}
	if !ps.acquireIP("198.51.100.2") {
		t.Fatal("başka IP etkilendi")
	}
	ps.releaseIP("198.51.100.1")
	if !ps.acquireIP("198.51.100.1") {
		t.Fatal("serbest bırakılan yuva geri alınamadı")
	}
}
