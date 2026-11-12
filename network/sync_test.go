package network

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// node bir zinciri dinleyen P2P sunucusudur.
type node struct {
	bc   *core.Blockchain
	ps   *P2PServer
	addr string
}

func startNode(t *testing.T, every time.Duration, peers ...string) *node {
	t.Helper()
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = every
	for _, p := range peers {
		ps.AddPeer(p)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return &node{bc: bc, ps: ps, addr: ln.Addr().String()}
}

func mineN(t *testing.T, bc *core.Blockchain, n int) {
	t.Helper()
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := bc.AddBlock(nil, kp.Address); err != nil {
			t.Fatal(err)
		}
	}
}

func waitTip(t *testing.T, bc *core.Blockchain, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bc.TipHash() == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("uç %s, beklenen %s", bc.TipHash(), want)
}

func waitRounds(t *testing.T, ps *P2PServer, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for ps.syncRounds.Load() < n {
		if time.Now().After(deadline) {
			t.Fatal("senkronizasyon turu tamamlanmadı")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Geride kalan düğüm açılışta eşinden zinciri başlık öncelikli olarak çeker.
func TestP2PSyncCatchesUp(t *testing.T) {
	src := startNode(t, time.Hour)
	mineN(t, src.bc, 12)
	dst := startNode(t, time.Hour, src.addr)
	waitTip(t, dst.bc, src.bc.TipHash())
	if n, _ := dst.bc.Confirmations(src.bc.TipHash()); n != 1 {
		t.Fatalf("onay %d", n)
	}
}

// Ebeveyni bilinmeyen blok senkronizasyonu tetikler (periyodik tur beklenmez).
func TestP2POrphanTriggersSync(t *testing.T) {
	src := startNode(t, time.Hour)
	dst := startNode(t, time.Hour, src.addr)
	waitRounds(t, dst.ps, 1) // açılış turu bitti; sonraki tur yalnızca tetikle gelir
	mineN(t, src.bc, 4)
	tip, err := src.bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	announcer := NewP2PServer("127.0.0.1:0", src.bc)
	announcer.AddPeer(dst.addr)
	announcer.BroadcastBlock(tip) // yalnızca uç: dst için yetim
	waitTip(t, dst.bc, tip.Hash)
}

// A → B → C hattında yeni uç blok yayınla en uca ulaşır.
func TestP2PRelayAcrossNodes(t *testing.T) {
	c := startNode(t, time.Hour)
	b := startNode(t, time.Hour, c.addr)
	waitRounds(t, b.ps, 1)
	waitRounds(t, c.ps, 1)

	src := newChain(t)
	mineN(t, src, 1)
	blk, err := src.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	a := NewP2PServer("127.0.0.1:0", src)
	a.AddPeer(b.addr)
	a.BroadcastBlock(blk)
	waitTip(t, b.bc, blk.Hash)
	waitTip(t, c.bc, blk.Hash)
}

// Biçimi bozuk ya da blok olmayan anahtarı gösteren özetler sunulmaz.
func TestP2PRejectsBadRequests(t *testing.T) {
	n := startNode(t, time.Hour)
	genesis, err := n.bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"lh", "schema", "bal_x", "", genesis.Hash + "0"} {
		if resp, err := n.ps.request(n.addr, message{Type: msgGetBlock, Hash: h}, maxBlockMessageBytes); err == nil && resp.Block != nil {
			t.Fatalf("%q için blok döndü", h)
		}
	}
	resp, err := n.ps.request(n.addr, message{Type: msgGetBlock, Hash: genesis.Hash}, maxBlockMessageBytes)
	if err != nil || resp.Block == nil || resp.Block.Hash != genesis.Hash {
		t.Fatalf("bilinen blok sunulmadı: %v", err)
	}
	long := make([]string, core.MaxLocatorHashes+1)
	for i := range long {
		long[i] = genesis.Hash
	}
	if _, err := n.ps.request(n.addr, message{Type: msgGetHeaders, Locator: long}, maxHeadersMessageBytes); err == nil {
		t.Fatal("çok uzun konum belirleyiciye yanıt verildi")
	}
	if _, err := n.ps.request(n.addr, message{Type: msgGetHeaders, Locator: []string{"lh"}}, maxHeadersMessageBytes); err == nil {
		t.Fatal("geçersiz özetli konum belirleyiciye yanıt verildi")
	}
}

func TestConfirmationsEndpoint(t *testing.T) {
	bc := newChain(t)
	mineN(t, bc, 3)
	h := NewAPIServer(":0", bc).Handler()
	tip := bc.TipHash()
	for url, want := range map[string]int{
		"/confirmations?hash=" + tip: http.StatusOK,
		"/confirmations?hash=lh":     http.StatusBadRequest,
		"/confirmations":             http.StatusBadRequest,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != want {
			t.Fatalf("%s = %d, beklenen %d", url, rec.Code, want)
		}
		if want != http.StatusOK {
			continue
		}
		var body struct {
			Confirmations int64 `json:"confirmations"`
			Alarm         bool  `json:"deep_reorg_alarm"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Confirmations != 1 || body.Alarm {
			t.Fatalf("yanıt %+v, hata %v", body, err)
		}
	}
}

// Başlık aşaması bir hatayla kesilse de (burada eşin son başlığı bozuk) o ana kadar
// doğrulanmış başlıklar indirilir: ilerleme kaybolmaz, hata yine bildirilir.
func TestP2PSyncKeepsValidHeaderPrefix(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 6)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
					hs := src.HeadersAfter(req.Locator, core.MaxHeadersPerMsg)
					if len(hs) > 0 {
						hs[len(hs)-1].Nonce++ // özetle uyuşmayan son başlık
					}
					json.NewEncoder(conn).Encode(message{Type: msgHeaders, Headers: hs})
				case msgGetBlock:
					b, _ := src.BlockByHash(req.Hash)
					json.NewEncoder(conn).Encode(message{Type: msgBlock, Block: b})
				}
			}
			conn.Close()
		}
	}()
	good := src.HeadersAfter([]string{}, core.MaxHeadersPerMsg)

	dst := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", dst)
	if err := ps.syncFrom(ln.Addr().String(), make(chan struct{})); !errors.Is(err, core.ErrBadHash) {
		t.Fatalf("hata %v, beklenen ErrBadHash", err)
	}
	if want := good[len(good)-2].Hash; dst.TipHash() != want {
		t.Fatalf("geçerli önek indirilmedi: uç %s, beklenen %s", dst.TipHash(), want)
	}
}
