package network

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
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

// peerHasBlockEventually PeerHasBlock'u yalnızca ağ hatasında, süre dolana kadar yeniden dener;
// eşin yanıtı (var ya da yok) ilk gelişte döner, iddialar gevşemez. Bir düğümün bir eşe açtığı
// bütün bağlantılar (uç sorgusu, itme, senkronizasyon, bu sorgu) eş için tek adres grubudur;
// grup başına bağlantı kotası (connsPerGroup) doluysa eş yeni bağlantıyı kabul anında kapatır
// ve istek EOF alır (quota.go). Bu üretimde de geçerlidir; teste özgü olan regtest
// madencisinin milisaniyede bir blok itmesidir.
func peerHasBlockEventually(ps *P2PServer, peer, hash string) (bool, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, err := ps.PeerHasBlock(peer, hash)
		if err == nil || time.Now().After(deadline) {
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
	if ok, err := peerHasBlockEventually(miner.ps, peer.addr, got); err != nil || !ok {
		t.Fatalf("PeerHasBlock eşteki bloğu bulamadı: %v %v (%s)", ok, err, quotaState(peer.ps))
	}
	if ok, err := peerHasBlockEventually(miner.ps, peer.addr, strings.Repeat("0", 64)); err != nil || ok {
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
	m.retryDelay = 20 * time.Millisecond
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

// fakePeer ilk closeFirst bağlantıyı kabul anında kapatır (grup kotası dolu eşin yaptığı gibi,
// istemci EOF alır); sonrakilerde isteği okuyup has ise istenen bloğu, değilse "bende yok"
// (boş msgBlock) döner. conns kabul edilen bağlantıları sayar.
type fakePeer struct {
	addr  string
	conns atomic.Int64
}

func startFakePeer(t *testing.T, closeFirst int64, has bool) *fakePeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	fp := &fakePeer{addr: ln.Addr().String()}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Sayaç kapatmadan önce artar: istemci EOF'u ancak kapanıştan sonra görür.
			if fp.conns.Add(1) <= closeFirst {
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var req message
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					return
				}
				resp := message{Type: msgBlock}
				if has {
					resp.Block = &core.Block{Hash: req.Hash}
				}
				json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()
	return fp
}

// checkMiner dinlemeyen (arka plan senkronizasyonu ve yayını olmayan) bir düğüm üzerinde,
// verilen eşlere sorgu atan ve beklemeleri kısaltılmış bir madenci kurar.
func checkMiner(t *testing.T, peers ...string) *Miner {
	t.Helper()
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	for _, p := range peers {
		ps.AddPeer(p)
	}
	m, err := NewMiner(ps, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	m.checkDelay = 0
	m.retryDelay = 10 * time.Millisecond
	return m
}

var checkBlock = &core.Block{Index: 1, Hash: strings.Repeat("ab", 32)}

// Bütün eşler bağlantıyı kabul anında kapatırsa (grup kotası dolu) blok "hiçbir eşte yok"
// sayılmaz: her eş checkAttempts kez denenir, ardından yalnızca ulaşılamadı uyarısı çıkar.
func TestMinerCheckAcceptedUnreachableNotUnaccepted(t *testing.T) {
	a := startFakePeer(t, 1<<30, true)
	b := startFakePeer(t, 1<<30, true)
	m := checkMiner(t, a.addr, b.addr)
	m.checkAccepted(checkBlock, make(chan struct{}))
	if got := m.unaccepted.Load(); got != 0 {
		t.Fatalf("ulaşılamayan eşler reddetmiş sayıldı: unaccepted=%d", got)
	}
	if got := m.unreachable.Load(); got != 1 {
		t.Fatalf("unreachable=%d, 1 bekleniyordu", got)
	}
	for _, fp := range []*fakePeer{a, b} {
		if got := fp.conns.Load(); got != int64(m.checkAttempts) {
			t.Fatalf("eş %d kez sorgulandı, %d bekleniyordu", got, m.checkAttempts)
		}
	}
}

// İlk sorgu EOF alır, yeniden deneme bloğu bulur: hiçbir uyarı çıkmaz.
func TestMinerCheckAcceptedRetriesNetworkError(t *testing.T) {
	fp := startFakePeer(t, 1, true)
	m := checkMiner(t, fp.addr)
	m.checkAccepted(checkBlock, make(chan struct{}))
	if u, r := m.unaccepted.Load(), m.unreachable.Load(); u != 0 || r != 0 {
		t.Fatalf("yeniden denemede bulunan blok için uyarı: unaccepted=%d unreachable=%d", u, r)
	}
	if got := fp.conns.Load(); got != 2 {
		t.Fatalf("eş %d kez sorgulandı, 2 bekleniyordu", got)
	}
}

// Bir eş "bende yok" der, diğerine ulaşılamaz: yanıt veren eş bloğu tutmadığından uyarı
// unaccepted olarak sayılır; yanıt veren eş bir daha sorgulanmaz.
func TestMinerCheckAcceptedMixedCountsUnaccepted(t *testing.T) {
	no := startFakePeer(t, 0, false)
	down := startFakePeer(t, 1<<30, true)
	m := checkMiner(t, no.addr, down.addr)
	m.checkAccepted(checkBlock, make(chan struct{}))
	if u, r := m.unaccepted.Load(), m.unreachable.Load(); u != 1 || r != 0 {
		t.Fatalf("unaccepted=%d unreachable=%d, 1/0 bekleniyordu", u, r)
	}
	if got := no.conns.Load(); got != 1 {
		t.Fatalf("yanıt veren eş %d kez sorgulandı, 1 bekleniyordu", got)
	}
	if got := down.conns.Load(); got != int64(m.checkAttempts) {
		t.Fatalf("ulaşılamayan eş %d kez sorgulandı, %d bekleniyordu", got, m.checkAttempts)
	}
}

// Durdurma yeniden deneme beklemesini keser: sayaçlar değişmez.
func TestMinerCheckAcceptedStopsDuringRetry(t *testing.T) {
	fp := startFakePeer(t, 1<<30, true)
	m := checkMiner(t, fp.addr)
	m.retryDelay = time.Hour
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { m.checkAccepted(checkBlock, stop); close(done) }()
	for fp.conns.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("durdurma yeniden deneme beklemesini kesmedi")
	}
	if u, r := m.unaccepted.Load(), m.unreachable.Load(); u != 0 || r != 0 {
		t.Fatalf("durdurulan denetim sayıldı: unaccepted=%d unreachable=%d", u, r)
	}
}
