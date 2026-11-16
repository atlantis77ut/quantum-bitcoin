package network

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// startTestnetNode başka bir ağda (testnet) dinleyen bir düğüm kurar.
func startTestnetNode(t *testing.T) *node {
	t.Helper()
	bc, err := core.NewBlockchain(t.TempDir(), core.TestnetParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return &node{bc: bc, ps: ps, addr: ln.Addr().String()}
}

// Her giden mesaj ağ kimliğimizi taşır; propbench'in tel boyutu da onu sayar.
func TestStampCarriesNetwork(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	_, genesis := bc.Network()
	if got := ps.stamp(message{Type: msgGetBlock}).Network; got != genesis {
		t.Fatalf("Network %q, beklenen %q", got, genesis)
	}
	if protocolVersion < 4 {
		t.Fatalf("sürüm %d, ağ kimliği sürüm 4 ister", protocolVersion)
	}
}

// Yalnızca başka ağdan gelen tek yönlü itmeler bırakılır; istekler ve kimliksiz mesajlar geçer.
func TestDropOtherNetworkOnlyPushes(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	_, genesis := bc.Network()
	other := strings.Repeat("f", 64)
	cases := []struct {
		typ, network string
		drop         bool
	}{
		{msgBlock, other, true},
		{msgTxs, other, true},
		{msgBlock, "", false},
		{msgTxs, genesis, false},
		{msgGetBlock, other, false},
		{msgGetHeaders, other, false},
		{msgGetAddr, other, false},
		{msgWaitTip, other, false},
	}
	for _, c := range cases {
		if got := ps.dropOtherNetwork(message{Type: c.typ, Network: c.network}, "127.0.0.1:1"); got != c.drop {
			t.Fatalf("%s/%q: bırakıldı %v, beklenen %v", c.typ, c.network, got, c.drop)
		}
	}
}

// Başka ağ kimliğiyle itilen geçerli blok işlenmez ve gönderen geri çevrilmez; aynı blok
// kimliksiz (eski düğüm) itilince kabul edilir.
func TestOtherNetworkPushDropped(t *testing.T) {
	n := startNode(t, time.Hour)
	src := newChain(t)
	mineN(t, src, 1)
	b, err := src.BlockByHash(src.TipHash())
	if err != nil {
		t.Fatal(err)
	}
	push := func(network string) {
		t.Helper()
		c, err := net.Dial("tcp", n.addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := json.NewEncoder(c).Encode(message{Type: msgBlock, Block: b, Network: network,
			Version: protocolVersion}); err != nil {
			t.Fatal(err)
		}
	}
	start := n.bc.TipHash()
	push(strings.Repeat("f", 64))
	time.Sleep(300 * time.Millisecond)
	if got := n.bc.TipHash(); got != start {
		t.Fatalf("başka ağdan itilen blok işlendi: uç %s", got)
	}
	if n.ps.prot.isDiscouraged(peerIDFor("127.0.0.1")) {
		t.Fatal("başka ağdaki eş geri çevrildi")
	}
	push("")
	waitTip(t, n.bc, b.Hash)
}

// Başka ağdaki eşe istek errWrongNetwork döner, eş ulaşılmış sayılmaz; yapılandırılmış eş için
// uyarı seyrektir. Karşı taraf isteği düşürmez, kendi kimliğiyle yanıtlar.
func TestRequestOtherNetwork(t *testing.T) {
	other := startTestnetNode(t)
	n := startNode(t, time.Hour, other.addr)
	if _, err := n.ps.PeerHasBlock(other.addr, strings.Repeat("0", 64)); !errors.Is(err, errWrongNetwork) {
		t.Fatalf("hata %v, beklenen errWrongNetwork", err)
	}
	n.ps.reachedMu.Lock()
	_, reached := n.ps.reached[other.addr]
	n.ps.reachedMu.Unlock()
	if reached {
		t.Fatal("başka ağdaki eş ulaşılmış sayıldı")
	}
	n.ps.netWarnMu.Lock()
	first, warned := n.ps.netWarn[other.addr]
	n.ps.netWarnMu.Unlock()
	if !warned {
		t.Fatal("yapılandırılmış eş için uyarı yazılmadı")
	}
	n.ps.warnOtherNetwork(other.addr, "x", first.Add(time.Minute))
	n.ps.netWarnMu.Lock()
	again := n.ps.netWarn[other.addr]
	n.ps.netWarnMu.Unlock()
	if !again.Equal(first) {
		t.Fatal("uyarı bir saat dolmadan yinelendi")
	}
	n.ps.warnOtherNetwork(other.addr, "x", first.Add(wrongNetWarnEvery))
	n.ps.netWarnMu.Lock()
	later := n.ps.netWarn[other.addr]
	n.ps.netWarnMu.Unlock()
	if !later.After(first) {
		t.Fatal("uyarı bir saat sonra yinelenmedi")
	}
}

// Başka ağdaki eşin getaddr yanıtı kaynağı 24 sa ertelemeye ve adres yanıtlarından çıkarmaya
// devam eder (request erken döndüğünde de noteNetwork çağrılır).
func TestAskAddrsOtherNetworkNoted(t *testing.T) {
	other := startTestnetNode(t)
	n := startNode(t, time.Hour, other.addr)
	if _, err := n.ps.askAddrs(other.addr, time.Now()); !errors.Is(err, errWrongNetwork) {
		t.Fatalf("hata %v, beklenen errWrongNetwork", err)
	}
	n.ps.reachedMu.Lock()
	defer n.ps.reachedMu.Unlock()
	if _, ok := n.ps.wrongNet[other.addr]; !ok {
		t.Fatal("başka ağdaki kaynak işaretlenmedi")
	}
}
