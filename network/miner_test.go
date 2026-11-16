package network

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

func minerAddr(t *testing.T) string {
	t.Helper()
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	return kp.Address
}

// runMiner madenciyi test boyunca çalıştırır; dönen işlev onu erken durdurur ve döngünün
// bitmesini bekler (birden çok çağrılabilir).
func runMiner(t *testing.T, m *Miner) func() {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { m.Run(stop); close(done) }()
	var once sync.Once
	halt := func() { once.Do(func() { close(stop); <-done }) }
	t.Cleanup(halt)
	return halt
}

// peerHasBlockEventually PeerHasBlock'u süre dolana kadar dener ve son sonucu döner. Testteki
// bütün düğümler 127.0.0.1'dedir, yani eş için tek adres grubudur: madencinin itme ve uç
// sorgusu bağlantıları grup başına bağlantı kotasını (connsPerGroup) doldurduğunda eş yeni
// bağlantıyı kabul anında kapatır ve istek EOF alır. Bu tasarım gereğidir (quota.go); tek
// adresli test düzeneğinin yapıntısıdır.
func peerHasBlockEventually(ps *P2PServer, peer, hash string, want bool) (bool, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := ps.PeerHasBlock(peer, hash)
		if (err == nil && ok == want) || time.Now().After(deadline) {
			return ok, err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// quotaState eşin 127.0.0.1 grubu için kota ve ceza durumunu döner; başarısızlıkta bağlantının
// hangi yoldan kapandığını ayırmak için.
func quotaState(ps *P2PServer) string {
	id := peerIDFor("127.0.0.1")
	ps.connQuota.mu.Lock()
	n := ps.connQuota.counts[id.group]
	ps.connQuota.mu.Unlock()
	return fmt.Sprintf("grup bağlantısı %d/%d, geri çevrilmiş %v", n, connsPerGroup, ps.prot.isDiscouraged(id))
}

// Madenci kazar, bloklarını yayar; eş blokları alır ve PeerHasBlock onları bulur.
func TestMinerMinesAndPeerAccepts(t *testing.T) {
	peer := startNode(t, time.Hour)
	miner := startNode(t, time.Hour, peer.addr)
	m, err := NewMiner(miner.ps, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	m.checkDelay = 50 * time.Millisecond
	stopMiner := runMiner(t, m)

	// Regtest madencisi çok hızlı kazar ve yayın kuyruğu blok düşürebilir; eşin aldığı
	// ilk blok (genesis'in çocuğu) her zaman bağlanır.
	genesis := peer.bc.TipHash()
	deadline := time.Now().Add(20 * time.Second)
	for peer.bc.TipHash() == genesis {
		if time.Now().After(deadline) {
			t.Fatalf("eş kazılan bloğu almadı (kazılan %d)", m.mined.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := peer.bc.TipHash()
	if _, err := miner.bc.BlockByHash(got); err != nil {
		t.Fatalf("eşin ucu madencinin bloğu değil: %v", err)
	}
	// Sorgulardan önce madenci durur; süren itmeler bitince grubun bağlantı kotası boşalır.
	stopMiner()
	if ok, err := peerHasBlockEventually(miner.ps, peer.addr, got, true); err != nil || !ok {
		t.Fatalf("PeerHasBlock eşteki bloğu bulamadı: %v %v (%s)", ok, err, quotaState(peer.ps))
	}
	if ok, err := peerHasBlockEventually(miner.ps, peer.addr, strings.Repeat("0", 64), false); err != nil || ok {
		t.Fatalf("olmayan blok bulundu: %v %v (%s)", ok, err, quotaState(peer.ps))
	}
}

// Eş kazılan blokları reddederse (burada farklı ağ/genesis) madenci uyarılır.
func TestMinerWarnsWhenPeersReject(t *testing.T) {
	other, err := core.NewBlockchain(t.TempDir(), core.TestnetParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	ops := NewP2PServer("127.0.0.1:0", other)
	ops.syncEvery = time.Hour
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ops.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	miner := startNode(t, time.Hour, ln.Addr().String())
	m, err := NewMiner(miner.ps, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	m.checkDelay = 50 * time.Millisecond
	runMiner(t, m)

	deadline := time.Now().Add(20 * time.Second)
	for m.unaccepted.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("reddeden eşe rağmen uyarı yok (kazılan %d)", m.mined.Load())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := NewMiner(miner.ps, "geçersiz"); err == nil {
		t.Fatal("geçersiz kazım adresi kabul edildi")
	}
}
