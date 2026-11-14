package network

import (
	"bufio"
	"encoding/json"
	"net"
	"slices"
	"testing"
	"time"
)

// Yedekler yapılandırılmış eşlere after boyunca ulaşılamayınca devreye girer; yeniden ulaşılınca
// exitAfter boyunca kesintisiz ulaşılırsa çıkar, araya giren kesinti histerezisi baştan başlatır.
func TestBackupMode(t *testing.T) {
	l := testBackupLimits()
	l.after, l.exitAfter, l.fresh = 10*time.Minute, 10*time.Minute, 2*time.Minute
	t0 := time.Unix(1_900_000_000, 0)
	var m backupMode
	if m.update(t0.Add(9*time.Minute), t0, &l) {
		t.Fatal("süre dolmadan yedek devreye girdi")
	}
	if !m.update(t0.Add(10*time.Minute), t0, &l) {
		t.Fatal("yedek devreye girmedi")
	}
	now := t0.Add(20 * time.Minute)
	if !m.update(now, now.Add(-time.Minute), &l) || !m.upSince.Equal(now) {
		t.Fatalf("ilk ulaşılmada yedekler hemen çıktı: %+v", m)
	}
	// Kesinti histerezisi sıfırlar.
	if !m.update(now.Add(5*time.Minute), now.Add(2*time.Minute), &l) || !m.upSince.IsZero() {
		t.Fatalf("kesinti histerezisi sıfırlamadı: %+v", m)
	}
	back := now.Add(6 * time.Minute)
	m.update(back, back, &l)
	if !m.update(back.Add(9*time.Minute), back.Add(9*time.Minute), &l) {
		t.Fatal("histerezis dolmadan yedekler çıktı")
	}
	if m.update(back.Add(10*time.Minute), back.Add(10*time.Minute), &l) {
		t.Fatal("histerezisten sonra yedekler çıkmadı")
	}
}

// Yedekler IPv4 /16 ve IPv6 /32 başına bir; regtest'te her adres ayrı gruptur.
func TestBackupGroup(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newTestnetChain(t))
	same := [][2]string{
		{"8.8.1.1:8001", "8.8.200.3:18001"},
		{"[2606:4700::1]:1", "[2606:4700:ffff::2]:2"},
	}
	for _, p := range same {
		if ps.backupGroup(p[0]) != ps.backupGroup(p[1]) {
			t.Fatalf("%s ve %s aynı grupta değil", p[0], p[1])
		}
	}
	diff := [][2]string{
		{"8.8.1.1:8001", "8.9.1.1:8001"},
		{"[2606:4700::1]:1", "[2606:4701::1]:1"},
		{"8.8.1.1:8001", "[808:101::1]:8001"},
	}
	for _, p := range diff {
		if ps.backupGroup(p[0]) == ps.backupGroup(p[1]) {
			t.Fatalf("%s ve %s aynı grupta", p[0], p[1])
		}
	}
	reg := NewP2PServer("127.0.0.1:0", newChain(t))
	if reg.backupGroup("127.0.0.1:1") == reg.backupGroup("127.0.0.1:2") {
		t.Fatal("regtest'te aynı makinedeki düğümler tek grup")
	}
}

// Adaylar: bozuk işaretli ve yakında başarısız olanlar elenir, son başarılı kullanılan önce
// gelir; yedek olarak başarıyla kullanılan kayıt kaynaklar susunca silinmez.
func TestAddrBookCandidates(t *testing.T) {
	b := MemoryAddrBook(newChain(t))
	now := time.Unix(1_900_000_000, 0)
	b.learn("S", []string{"8.1.0.1:1", "8.2.0.1:1", "8.3.0.1:1", "8.4.0.1:1", "8.5.0.1:1"}, none, now)
	b.markBad("8.1.0.1:1", now.Add(time.Hour))
	b.markFailure("8.2.0.1:1", now)
	b.markSuccess("8.3.0.1:1", now.Add(-2*time.Hour))
	b.markFailure("8.3.0.1:1", now.Add(-3*time.Hour)) // başarıdan eski: aday kalır
	b.markSuccess("8.4.0.1:1", now.Add(-time.Hour))
	got := b.candidates(now.Add(time.Minute), 30*time.Minute)
	want := []string{"8.4.0.1:1", "8.3.0.1:1", "8.5.0.1:1"}
	if !slices.Equal(got, want) {
		t.Fatalf("adaylar %v, beklenen %v", got, want)
	}
	if got := b.candidates(now.Add(31*time.Minute), 30*time.Minute); !slices.Contains(got, "8.2.0.1:1") {
		t.Fatalf("yeniden deneme süresinden sonra aday dönmedi: %v", got)
	}
	if got := b.candidates(now.Add(2*time.Hour), 30*time.Minute); !slices.Contains(got, "8.1.0.1:1") {
		t.Fatalf("bozuk işaret süresi dolunca aday dönmedi: %v", got)
	}

	// Kaynaklar 7 gün susar; yalnızca yedek olarak kullanılan kayıt kalır.
	late := now.Add(addrExpiry + time.Hour)
	b.markSuccess("8.5.0.1:1", late.Add(-time.Hour))
	b.learn("S", nil, none, late)
	if snap := b.snapshot(); len(snap) != 1 || snap["8.5.0.1:1"].Addr == "" {
		t.Fatalf("süre dolumundan sonra tablo %v", snap)
	}
}

// Yapılandırılmamış eşin (yedek ya da çıkarılmış yedek) ön-senkronizasyon ertelemesi IP'ye
// bağlıdır; tur başındaki budama etkin olmayan eşlerin durumunu siler, süren ön-senkronizasyonu
// ertelemeyle bırakır, ertelemeyi yalnızca süresi çoktan dolmuşsa siler.
func TestBackupSyncStatePruning(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.AddPeer("127.0.0.1:1")
	ps.backups = []string{"127.0.0.1:5"}
	if k := ps.presyncKey("127.0.0.1:5"); k != "127.0.0.1" {
		t.Fatalf("yedeğin erteleme anahtarı %q", k)
	}
	if k := ps.presyncKey("127.0.0.1:7"); k != "127.0.0.1" {
		t.Fatalf("çıkarılmış yedeğin erteleme anahtarı %q", k)
	}
	if k := ps.presyncKey("127.0.0.1:1"); k != "127.0.0.1:1" {
		t.Fatalf("yapılandırılmış eşin erteleme anahtarı %q", k)
	}
	now := time.Now()
	ps.presyncs["127.0.0.1:9"] = &peerPresync{}
	ps.presyncs["127.0.0.1:5"] = &peerPresync{}
	ps.lastMempoolSync["127.0.0.1:9"] = now
	old := &presyncPenalty{until: now.Add(-presyncBackoffMax - time.Minute), delay: time.Hour}
	ps.presyncBackoff["127.0.0.2"] = old
	ps.presyncBackoff["127.0.0.1:1"] = old
	ps.presyncBackoff["127.0.0.3"] = &presyncPenalty{until: now.Add(-time.Minute), delay: time.Hour}
	ps.pruneSyncState(ps.activePeers(), now)
	if ps.presyncs["127.0.0.1:9"] != nil || ps.presyncs["127.0.0.1:5"] == nil {
		t.Fatalf("ön-senkronizasyon budaması %v", ps.presyncs)
	}
	if _, ok := ps.lastMempoolSync["127.0.0.1:9"]; ok {
		t.Fatal("havuz eşitleme kaydı budanmadı")
	}
	if ps.presyncBackoff["127.0.0.2"] != nil || ps.presyncBackoff["127.0.0.1:1"] == nil ||
		ps.presyncBackoff["127.0.0.3"] == nil {
		t.Fatalf("erteleme budaması %v", ps.presyncBackoff)
	}
	if pen := ps.presyncBackoff["127.0.0.1"]; pen == nil || pen.delay != presyncBackoffMin {
		t.Fatalf("çıkan eşin süren ön-senkronizasyonu ertelenmedi: %+v", pen)
	}
}

// Etkin olmayan eşin boştaki gönderim durumu silinir; çalışanı süren durumun kuyruğu boşaltılır
// ama durum kalır (aynı eşe iki gönderim döngüsü olmasın).
func TestBackupSenderPruning(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.senderFor("idle")
	busy := ps.senderFor("busy")
	busy.push([]byte("x"), true, time.Now()) // çalışan başlatılmadı ama running işaretli
	ps.senderFor("live")
	ps.pruneSenders([]string{"live"})
	ps.sendersMu.Lock()
	_, idle := ps.senders["idle"]
	_, kept := ps.senders["busy"]
	_, live := ps.senders["live"]
	ps.sendersMu.Unlock()
	if idle || !kept || !live || busy.hasBlock() {
		t.Fatalf("budama: boştaki %v, çalışan %v (kuyruk %v), etkin %v", idle, kept, busy.hasBlock(), live)
	}
}

// Yedekten gelen, ebeveyni bilinmeyen uç başlığı senkronizasyon tetiklemez; yapılandırılmış
// eşinki tetikler.
func TestBackupTipNoTrigger(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 2)
	h, ok := src.HeaderByHash(src.TipHash())
	if !ok {
		t.Fatal("başlık yok")
	}
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.backups = []string{"127.0.0.1:5"}
	if out := ps.adoptTip("127.0.0.1:5", h); out != tipNeutral || ps.lastTipSync.Load() != 0 {
		t.Fatalf("yedeğin başlığı: %v, tetik %d", out, ps.lastTipSync.Load())
	}
	if out := ps.adoptTip("127.0.0.1:6", h); out != tipNeutral || ps.lastTipSync.Load() != 0 {
		t.Fatalf("çıkarılmış yedeğin başlığı: %v, tetik %d", out, ps.lastTipSync.Load())
	}
	ps.AddPeer("127.0.0.1:1")
	if out := ps.adoptTip("127.0.0.1:1", h); out != tipNeutral || ps.lastTipSync.Load() == 0 {
		t.Fatalf("yapılandırılmış eşin başlığı: %v, tetik %d", out, ps.lastTipSync.Load())
	}
}

func testBackupLimits() backupLimits {
	return backupLimits{
		after:     200 * time.Millisecond,
		exitAfter: 300 * time.Millisecond,
		fresh:     500 * time.Millisecond,
		stale:     time.Hour,
		retry:     time.Hour,
		badFor:    time.Hour,
		scan:      20 * time.Millisecond,
		max:       4,
		probes:    8,
	}
}

// startBackupNode yedekleri hızlı devreye giren, keşfi açık bir regtest düğümü başlatır; prep
// Serve'den önce kendi dinleme adresiyle çağrılır (tabloyu doldurmak için).
func startBackupNode(t *testing.T, peers, seeds []string, prep func(ps *P2PServer, self string)) *node {
	t.Helper()
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = 50 * time.Millisecond
	ps.backupLim = testBackupLimits()
	for _, p := range peers {
		ps.AddPeer(p)
	}
	ps.EnableDiscovery(MemoryAddrBook(bc), seeds)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if prep != nil {
		prep(ps, ln.Addr().String())
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return &node{bc: bc, ps: ps, addr: ln.Addr().String()}
}

// withTable tabloya adresleri güvenilen bir kaynaktan öğrenilmiş gibi koyar.
func withTable(addrs ...string) func(*P2PServer, string) {
	return func(ps *P2PServer, _ string) {
		ps.addrs.learn("kaynak", addrs, none, time.Now())
	}
}

// Yapılandırılmış eşe ulaşılamayınca tablodaki eş yedek olur ve zincir ondan senkronize edilir;
// yedek paylaşılmaz. Yapılandırılmış eş geri gelince yedekler histerezisten sonra bırakılır.
func TestBackupTakesOverAndReleases(t *testing.T) {
	d := startNode(t, time.Hour)
	mineN(t, d.bc, 5)
	dead := freeAddr(t)
	a := startBackupNode(t, []string{dead}, nil, withTable(d.addr))
	waitTip(t, a.bc, d.bc.TipHash())
	if !a.ps.isBackup(d.addr) || !slices.Equal(a.ps.Peers(), []string{dead}) {
		t.Fatalf("yedekler %v, yapılandırılmış %v", a.ps.backupList(), a.ps.Peers())
	}
	if got := a.ps.reachedAddrs(time.Now()); len(got) != 0 {
		t.Fatalf("yedek getaddr yanıtına girdi: %v", got)
	}
	if e := a.ps.addrs.snapshot()[d.addr]; e.LastSuccess == 0 {
		t.Fatalf("yedeğin başarısı yazılmadı: %+v", e)
	}

	// Yapılandırılmış eş aynı adreste geri gelir.
	ln, err := net.Listen("tcp", dead)
	if err != nil {
		t.Skipf("yapılandırılmış eşin portu yeniden açılamadı: %v", err)
	}
	back := NewP2PServer(dead, newChain(t))
	go back.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	waitFor(t, "yapılandırılmış eş dönünce yedekler bırakılmadı", func() bool {
		return len(a.ps.backupList()) == 0
	})
	if got := a.ps.activePeers(); !slices.Equal(got, []string{dead}) {
		t.Fatalf("etkin eşler %v", got)
	}
}

// -peer verilmişken gömülü liste de yedek havuzundadır.
func TestBackupUsesSeeds(t *testing.T) {
	d := startNode(t, time.Hour)
	a := startBackupNode(t, []string{freeAddr(t)}, []string{d.addr}, nil)
	waitFor(t, "gömülü liste yedek olmadı", func() bool { return a.ps.isBackup(d.addr) })
}

// Tablodaki kendi adresimiz sınamada anlaşılır, yedek olmaz ve tablodan çıkar.
func TestBackupProbeDetectsSelf(t *testing.T) {
	var self string
	a := startBackupNode(t, []string{freeAddr(t)}, nil, func(ps *P2PServer, addr string) {
		self = addr
		ps.addrs.learn("kaynak", []string{addr}, none, time.Now())
	})
	waitFor(t, "kendine bağlanma anlaşılmadı", func() bool {
		a.ps.probeMu.Lock()
		defer a.ps.probeMu.Unlock()
		return a.ps.selfAddrs[self]
	})
	if a.ps.isBackup(self) || a.ps.addrs.Len() != 0 {
		t.Fatalf("kendi adresimiz: yedek %v, tablo %d", a.ps.isBackup(self), a.ps.addrs.Len())
	}
}

// Başka ağdaki aday sınamada bozuk işaretlenir ve yedek olmaz.
func TestBackupWrongNetworkMarkedBad(t *testing.T) {
	other := startAddrNode(t, newTestnetChain(t), false, nil)
	a := startBackupNode(t, []string{freeAddr(t)}, nil, withTable(other.addr))
	waitFor(t, "başka ağdaki aday işaretlenmedi", func() bool {
		return a.ps.addrs.snapshot()[other.addr].BadUntil > time.Now().Unix()
	})
	if a.ps.isBackup(other.addr) {
		t.Fatal("başka ağdaki aday yedek oldu")
	}
}

// malformedPeer sınamayı geçer (doğru ağ kimliğiyle `addr` yanıtı), sonraki her isteğe JSON
// sözdizimi bozuk yanıt verir.
func malformedPeer(t *testing.T, genesis string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	addrResp, _ := json.Marshal(stamped(message{Type: msgAddr, Network: genesis}))
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var m message
			if line, err := bufio.NewReader(c).ReadBytes('\n'); err == nil && json.Unmarshal(line, &m) == nil {
				if m.Type == msgGetAddr {
					c.Write(append(addrResp, '\n'))
				} else {
					c.Write([]byte("{bad\n"))
				}
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

// Sözdizimi bozuk yanıt veren yedek bozuk işaretlenir ve hemen çıkarılır; yeniden seçilmez.
func TestBackupMalformedMarkedBad(t *testing.T) {
	_, genesis := newChain(t).Network()
	bad := malformedPeer(t, genesis)
	a := startBackupNode(t, []string{freeAddr(t)}, nil, withTable(bad))
	waitFor(t, "bozuk yanıt veren yedek işaretlenmedi", func() bool {
		return a.ps.addrs.snapshot()[bad].BadUntil > time.Now().Unix()
	})
	time.Sleep(100 * time.Millisecond)
	if a.ps.isBackup(bad) {
		t.Fatal("işaretli eş yeniden yedek oldu")
	}
}
