package network

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// tipTestServer bekleyen istekleri test sonunda, zincir kapanmadan önce bırakan bir sunucudur.
func tipTestServer(t *testing.T, bc *core.Blockchain) *P2PServer {
	t.Helper()
	ps := NewP2PServer("127.0.0.1:0", bc)
	quit := make(chan struct{})
	ps.setQuit(quit)
	t.Cleanup(func() { close(quit) })
	return ps
}

// waitTipReply serveWaitTip'i bir boru üzerinde çalıştırır; yanıt gelirse kanala yazılır.
func waitTipReply(t *testing.T, ps *P2PServer, id peerID, known string) <-chan message {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	go ps.serveWaitTip(b, "r", id, known)
	out := make(chan message, 1)
	go func() {
		var m message
		if json.NewDecoder(a).Decode(&m) == nil {
			out <- m
		}
	}()
	return out
}

func recvTip(t *testing.T, ch <-chan message, within time.Duration) message {
	t.Helper()
	select {
	case m := <-ch:
		if m.Type != msgTip {
			t.Fatalf("yanıt türü %q", m.Type)
		}
		return m
	case <-time.After(within):
		t.Fatalf("%v içinde uç yanıtı gelmedi", within)
		return message{}
	}
}

func noTip(t *testing.T, ch <-chan message, d time.Duration) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("beklenmeyen uç yanıtı: %+v", m)
	case <-time.After(d):
	}
}

// Bilinen uç farklıysa hemen, aynıysa uç değişince ya da süre dolunca yanıtlanır; geçersiz özet
// yanıtlanmaz; sunucu kapanınca bekleyen yanıtsız döner ve yuvasını bırakır.
func TestServeWaitTip(t *testing.T) {
	bc := newChain(t)
	ps := tipTestServer(t, bc)
	tip := bc.TipHash()

	if m := recvTip(t, waitTipReply(t, ps, pid("a"), ""), time.Second); len(m.Headers) != 1 || m.Headers[0].Hash != tip {
		t.Fatalf("ilk istek: %+v", m.Headers)
	}

	ps.tipWait = 300 * time.Millisecond
	ch := waitTipReply(t, ps, pid("b"), tip)
	noTip(t, ch, 100*time.Millisecond)
	if m := recvTip(t, ch, 2*time.Second); len(m.Headers) != 1 || m.Headers[0].Hash != tip {
		t.Fatalf("süre dolunca: %+v", m.Headers)
	}

	ps.tipWait = time.Minute
	ch = waitTipReply(t, ps, pid("c"), tip)
	waitFor(t, "istek beklemeye girmedi", func() bool { return len(ps.waits.all) == 1 })
	mineN(t, bc, 1)
	b, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	ps.relay(b)
	// Yedek denetim (tipWaitRecheck) beklenmeden: bildirim uyandırır.
	if m := recvTip(t, ch, 500*time.Millisecond); len(m.Headers) != 1 || m.Headers[0].Hash != b.Hash {
		t.Fatalf("uç değişince: %+v", m.Headers)
	}

	noTip(t, waitTipReply(t, ps, pid("d"), "zz"), 200*time.Millisecond)

	quit := make(chan struct{})
	ps.setQuit(quit)
	waitTipReply(t, ps, pid("e"), b.Hash)
	waitFor(t, "istek beklemeye girmedi", func() bool { return len(ps.waits.all) == 1 })
	close(quit)
	waitFor(t, "kapanışta bekleme yuvası bırakılmadı", func() bool { return len(ps.waits.all) == 0 })
}

// Grup kotası, dolu açık pay ve en az zincir işinin altındaki sunucu boş yanıt ("şimdi
// bekletemiyor") verir; yapılandırılmış eş ayrılmış payla yine bekler.
func TestServeWaitTipBusy(t *testing.T) {
	if maxConcurrentWaits-reservedWaits >= maxConcurrentConns-reservedConns {
		t.Fatal("bekleme havuzunun açık payı bağlantılarınkini dolduruyor")
	}
	bc := newChain(t)
	ps := tipTestServer(t, bc)
	ps.tipWait = time.Minute
	tip := bc.TipHash()

	g1, g2 := peerIDFor("203.0.113.1"), peerIDFor("203.0.113.2")
	waitTipReply(t, ps, g1, tip)
	waitFor(t, "istek beklemeye girmedi", func() bool { return len(ps.waits.all) == 1 })
	if m := recvTip(t, waitTipReply(t, ps, g2, tip), time.Second); len(m.Headers) != 0 {
		t.Fatalf("grup kotası aşıldı: %+v", m.Headers)
	}

	for len(ps.waits.open) < cap(ps.waits.open) {
		if !ps.waits.tryAcquire(levelNone) {
			t.Fatal("açık pay dolmadan yuva alınamadı")
		}
	}
	if m := recvTip(t, waitTipReply(t, ps, pid("açık"), tip), time.Second); len(m.Headers) != 0 {
		t.Fatalf("dolu açık payda bekletildi: %+v", m.Headers)
	}
	n := len(ps.waits.all)
	cfg := peerIDFor("198.51.100.1")
	ps.prot.addConfigured(cfg.group)
	ch := waitTipReply(t, ps, cfg, tip)
	waitFor(t, "yapılandırılmış eş beklemeye girmedi", func() bool { return len(ps.waits.all) == n+1 })
	noTip(t, ch, 100*time.Millisecond)

	p := *core.RegtestParams
	p.MinChainWork = new(big.Int).Lsh(big.NewInt(1), 200)
	low, err := core.NewBlockchain(t.TempDir(), &p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { low.Close() })
	lps := tipTestServer(t, low)
	if m := recvTip(t, waitTipReply(t, lps, pid("a"), ""), time.Second); len(m.Headers) != 0 {
		t.Fatalf("en az zincir işinin altında uç sunuldu: %+v", m.Headers)
	}
}

// Kabul testi: istemci sunucuyu yapılandırmış, sunucu istemciyi tanımıyor (istemci gelen
// bağlantı alamayan düğüm gibi); senkronizasyon turu saatte bir. Sunucuda kazılan blok
// istemciye uç beklemesiyle saniyeler içinde ulaşır.
func TestTipLongPollDeliversBlock(t *testing.T) {
	src := startNode(t, time.Hour)
	dst := startNode(t, time.Hour, src.addr)
	waitRounds(t, dst.ps, 1)
	waitFor(t, "uç isteği beklemeye girmedi", func() bool { return len(src.ps.waits.all) == 1 })

	start := time.Now()
	mineN(t, src.bc, 1)
	b, err := src.bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	src.ps.relay(b) // madenci de bloğu relay ile duyurur
	waitTip(t, dst.bc, b.Hash)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("blok %v sonra ulaştı", d)
	}
	if n := dst.ps.syncRounds.Load(); n != 1 {
		t.Fatalf("blok senkronizasyonla geldi: %d tur", n)
	}
}

// Ebeveyni bilinmeyen uç başlığı (istemci birden çok blok geride) senkronizasyonu tetikler;
// tipSyncFloor içindeki ikinci tetik bastırılır. Döngüler çalışmaz: tetik syncCh'de görülür.
func TestAdoptTipTriggersSyncFloored(t *testing.T) {
	src := startNode(t, time.Hour)
	mineN(t, src.bc, 2)
	c := NewP2PServer("127.0.0.1:0", newChain(t))
	c.AddPeer(src.addr) // yalnızca yapılandırılmış eşin başlığı tetikler (backup.go)
	now := time.Now()
	if out := c.adoptTip(src.addr, src.bc.TipHeader()); out != tipNeutral || len(c.syncCh) != 1 {
		t.Fatalf("sonuç %d, tetik %d", out, len(c.syncCh))
	}
	<-c.syncCh
	c.triggerTipSync(now.Add(tipSyncFloor / 2))
	if len(c.syncCh) != 0 {
		t.Fatal("taban içinde ikinci tetik")
	}
	c.triggerTipSync(now.Add(2 * tipSyncFloor))
	if len(c.syncCh) != 1 {
		t.Fatal("taban sonrası tetik yok")
	}
}

// tipServer her bağlantıda tek mesaj okur, uç isteklerini sayar ve respond'un döndürdüğünü
// yazar (nil: yanıtsız kapatır, eski sürümlü eş gibi). Başlık isteklerine boş yanıt verir.
func tipServer(t *testing.T, respond func(req message) []byte) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var req message
			if json.NewDecoder(c).Decode(&req) == nil {
				switch req.Type {
				case msgWaitTip:
					n.Add(1)
					if r := respond(req); r != nil {
						c.Write(r)
					}
				case msgGetHeaders:
					json.NewEncoder(c).Encode(message{Type: msgHeaders})
				}
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), &n
}

// tipLine ve randHash eşin gorutininde de çağrılır (t.Fatal kullanılamaz).
func tipLine(m message) []byte {
	data, err := json.Marshal(stamped(m))
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func randHash() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Yanıt sınıfları: yanıtsız kapanış ve bozuk yanıt başarısız, boş yanıt meşgul, değişmeyen uç
// ilerleme, ana zincirimizdeki uç nötrdür.
func TestPollTipOutcomes(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	tip := bc.TipHeader()
	stop := make(chan struct{})
	for _, c := range []struct {
		name  string
		resp  []byte
		known string
		want  tipOutcome
	}{
		{"eski eş", nil, "", tipFailed},
		{"yanlış tür", tipLine(message{Type: msgHeaders}), "", tipFailed},
		{"iki başlık", tipLine(message{Type: msgTip, Headers: []core.BlockHeader{tip, tip}}), "", tipFailed},
		{"meşgul", tipLine(message{Type: msgTip}), "", tipBusy},
		{"değişmedi", tipLine(message{Type: msgTip, Headers: []core.BlockHeader{tip}}), tip.Hash, tipProgress},
		{"bilinen uç", tipLine(message{Type: msgTip, Headers: []core.BlockHeader{tip}}), "", tipNeutral},
	} {
		resp := c.resp
		addr, _ := tipServer(t, func(message) []byte { return resp })
		if _, got := ps.pollTip(addr, c.known, stop); got != c.want {
			t.Fatalf("%s: sonuç %d, beklenen %d", c.name, got, c.want)
		}
	}
}

func TestTipBackoff(t *testing.T) {
	for _, c := range []struct {
		fails int
		want  time.Duration
	}{{1, tipPollBackoffMin}, {2, 2 * tipPollBackoffMin}, {7, tipPollBackoffMax}, {100, tipPollBackoffMax}} {
		if got := tipBackoff(c.fails); got != c.want {
			t.Fatalf("%d başarısızlık: %v, beklenen %v", c.fails, got, c.want)
		}
	}
}

// Uç beklemesini bilmeyen eşe istemci ertelemeyle döner; meşgul eşe de sık istek açmaz.
func TestTipPollBacksOff(t *testing.T) {
	old, oldN := tipServer(t, func(message) []byte { return nil })
	busy, busyN := tipServer(t, func(message) []byte { return tipLine(message{Type: msgTip}) })
	startNode(t, time.Hour, old, busy)
	waitFor(t, "eski eşe uç isteği gitmedi", func() bool { return oldN.Load() >= 1 })
	waitFor(t, "meşgul eşe uç isteği gitmedi", func() bool { return busyN.Load() >= 1 })
	time.Sleep(2 * time.Second)
	if o, b := oldN.Load(), busyN.Load(); o != 1 || b != 1 {
		t.Fatalf("2 sn içinde eski eşe %d, meşgul eşe %d istek", o, b)
	}
}

// Ebeveyni rastgele uç başlıkları bedavadır: istemci her saniye en çok bir istek açar ve
// senkronizasyonu tipSyncFloor içinde en çok bir kez tetikler.
func TestTipPollRandomParentBounded(t *testing.T) {
	addr, n := tipServer(t, func(message) []byte {
		h := core.BlockHeader{Version: core.BlockVersion, Index: 5, PrevHash: randHash(), MerkleRoot: randHash(),
			Hash: randHash()}
		return tipLine(message{Type: msgTip, Headers: []core.BlockHeader{h}})
	})
	dst := startNode(t, time.Hour, addr)
	waitFor(t, "uç isteği tekrarlanmadı", func() bool { return n.Load() >= 3 })
	if r := dst.ps.syncRounds.Load(); r > 2 {
		t.Fatalf("%d senkronizasyon turu: tetik sınırlanmadı", r)
	}
	if got := n.Load(); got > 5 {
		t.Fatalf("%d uç isteği: istekler arası en az süre uygulanmadı", got)
	}
}
