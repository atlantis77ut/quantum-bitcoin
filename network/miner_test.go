package network

import (
	"net"
	"strings"
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

func runMiner(t *testing.T, m *Miner) {
	t.Helper()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { m.Run(stop); close(done) }()
	t.Cleanup(func() { close(stop); <-done })
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
	runMiner(t, m)

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
	if ok, err := miner.ps.PeerHasBlock(peer.addr, got); err != nil || !ok {
		t.Fatalf("PeerHasBlock eşteki bloğu bulamadı: %v %v", ok, err)
	}
	if ok, err := miner.ps.PeerHasBlock(peer.addr, strings.Repeat("0", 64)); err != nil || ok {
		t.Fatalf("olmayan blok bulundu: %v %v", ok, err)
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
