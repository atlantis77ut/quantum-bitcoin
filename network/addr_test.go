package network

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"

	"quantum-bitcoin/core"
)

// Yalnızca IP ve geçerli port; regtest dışında yalnızca yönlendirilebilir adresler.
func TestCanonicalAddr(t *testing.T) {
	ok := map[string]string{
		"8.8.8.8:8001":            "8.8.8.8:8001",
		"8.8.8.8:08001":           "8.8.8.8:8001",
		"[2606:4700::1111]:18001": "[2606:4700::1111]:18001",
		"[::ffff:8.8.4.4]:8001":   "8.8.4.4:8001",
		"[64:ff9b::808:808]:8001": "[64:ff9b::808:808]:8001",
		"[2002:808:808::1]:8001":  "[2002:808:808::1]:8001",
	}
	for in, want := range ok {
		if got, valid := canonicalAddr(in, false); !valid || got != want {
			t.Fatalf("%s: %q %v, beklenen %q", in, got, valid, want)
		}
	}
	for _, in := range []string{
		"node.example.com:8001", "8.8.8.8", "8.8.8.8:0", "8.8.8.8:65536", "8.8.8.8:-1", "8.8.8.8:+1",
		"10.1.2.3:8001", "172.16.0.1:8001", "192.168.1.1:8001", "127.0.0.1:8001", "100.64.0.1:8001",
		"169.254.1.1:8001", "192.0.2.1:8001", "198.51.100.1:8001", "203.0.113.5:8001",
		"198.18.0.1:8001", "0.0.0.0:8001", "224.0.0.1:8001", "255.255.255.255:8001",
		"[::]:8001", "[::1]:8001", "[fe80::1%eth0]:8001", "[fe80::1]:8001", "[fc00::1]:8001",
		"[2001:db8::1]:8001", "[ff02::1]:8001", "[::1.2.3.4]:8001", "[::ffff:0:a00:1]:8001",
		"[64:ff9b::a00:1]:8001", "[64:ff9b::7f00:1]:8001", "[64:ff9b:1::1]:8001",
		"[2002:a00:1::]:8001", "[2002:c0a8:101::1]:8001", "[2001:2::1]:8001", "[3fff::1]:8001",
	} {
		if got, valid := canonicalAddr(in, false); valid {
			t.Fatalf("%s kabul edildi: %q", in, got)
		}
	}
	for in, want := range map[string]string{"127.0.0.1:28001": "127.0.0.1:28001", "10.0.0.1:1": "10.0.0.1:1"} {
		if got, valid := canonicalAddr(in, true); !valid || got != want {
			t.Fatalf("regtest %s: %q %v", in, got, valid)
		}
	}
	for _, in := range []string{"0.0.0.0:1", "[::]:1", "224.0.0.1:1", "node:1"} {
		if _, valid := canonicalAddr(in, true); valid {
			t.Fatalf("regtest %s kabul edildi", in)
		}
	}
}

// addrsN kaynak için n farklı yönlendirilebilir adres üretir.
func addrsN(prefix, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("8.%d.%d.%d:8001", prefix, i/250, i%250+1)
	}
	return out
}

func none(string) bool { return false }

// Kaynak başına ve toplam sınır, en eski görülenin düşmesi, süre dolumu, bilinen adreslerin ve
// tekrarların elenmesi.
func TestAddrBookLearn(t *testing.T) {
	b := MemoryAddrBook(newChain(t))
	t0 := time.Unix(1_900_000_000, 0)

	in := append([]string{"8.8.8.8:8001", "host:1", "8.1.0.1:8001"}, addrsN(1, 40)...)
	known := func(a string) bool { return a == "8.8.8.8:8001" }
	if n := b.learn("A", in, known, t0); n != addrPerSource {
		t.Fatalf("A'dan %d adres alındı, beklenen %d", n, addrPerSource)
	}
	if _, ok := b.snapshot()["8.8.8.8:8001"]; ok {
		t.Fatal("bilinen adres öğrenildi")
	}

	// Aynı adres başka kaynaktan: kaynağı değişir, yeni sayılmaz.
	if n := b.learn("B", []string{"8.1.0.1:8001"}, none, t0.Add(time.Minute)); n != 0 {
		t.Fatalf("var olan adres yeni sayıldı: %d", n)
	}
	if e := b.snapshot()["8.1.0.1:8001"]; e.Source != "B" || e.LastSeen != t0.Add(time.Minute).Unix() {
		t.Fatalf("tazelenen kayıt %+v", e)
	}

	// Tablo sınırı: en eski görülenler (A'nın kayıtları) düşer.
	for s := 2; s <= 9; s++ {
		b.learn(fmt.Sprintf("S%d", s), addrsN(s, addrPerSource), none, t0.Add(time.Duration(s)*time.Hour))
	}
	snap := b.snapshot()
	if len(snap) != addrTableMax {
		t.Fatalf("tablo %d kayıt", len(snap))
	}
	for _, e := range snap {
		if e.Source == "A" {
			t.Fatal("en eski görülen kaynak düşmedi")
		}
	}

	// Süre dolumu: addrExpiry boyunca görülmeyenler silinir.
	late := t0.Add(9*time.Hour + addrExpiry + time.Second)
	b.learn("S9", addrsN(9, addrPerSource), none, late)
	if n := b.Len(); n != addrPerSource {
		t.Fatalf("süre dolumundan sonra %d kayıt", n)
	}
}

// Tablo diskte kalır; geçersiz ve süresi dolmuş kayıtlar yüklenmez ve silinir; başka ağın tablosu
// sıfırlanır.
func TestAddrBookPersist(t *testing.T) {
	bc := newChain(t)
	path := filepath.Join(t.TempDir(), "peers")
	b, err := OpenAddrBook(path, bc)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	b.learn("A", []string{"8.8.8.8:8001", "127.0.0.1:28001"}, none, now)
	b.Close()
	b.learn("A", []string{"8.8.4.4:8001"}, none, now) // kapalı tablo: kayıt atlanır, panik yok

	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(addrEntry{Addr: "9.9.9.9:8001", Source: "A", LastSeen: now.Add(-addrExpiry - time.Hour).Unix()})
	bad, _ := json.Marshal(addrEntry{Addr: "8.8.8.8:1", Source: "A", LastSeen: now.Unix()})
	for k, v := range map[string][]byte{"a/9.9.9.9:8001": old, "a/host:1": []byte("{}"), "a/8.8.8.9:1": bad} {
		if err := db.Put([]byte(k), v, nil); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	b, err = OpenAddrBook(path, bc)
	if err != nil {
		t.Fatal(err)
	}
	snap := b.snapshot()
	if len(snap) != 2 || snap["8.8.8.8:8001"].Source != "A" || snap["127.0.0.1:28001"].Addr == "" {
		t.Fatalf("yüklenen tablo %v", snap)
	}
	b.Close()
	db, err = leveldb.OpenFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a/9.9.9.9:8001", "a/host:1", "a/8.8.8.9:1"} {
		if ok, _ := db.Has([]byte(k), nil); ok {
			t.Fatalf("%s diskten silinmedi", k)
		}
	}
	db.Close()

	b, err = OpenAddrBook(path, newTestnetChain(t))
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Fatal("başka ağın tablosu sıfırlanmadı")
	}
	b.Close()
}

func newTestnetChain(t *testing.T) *core.Blockchain {
	t.Helper()
	bc, err := core.NewBlockchain(t.TempDir(), core.TestnetParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	return bc
}

// Başka ağdaki yapılandırılmış eş, isteklerine yanıt verdiği için ulaşılmış görünse de adres
// yanıtında verilmez.
func TestWrongNetworkPeerNotShared(t *testing.T) {
	other := startAddrNode(t, newTestnetChain(t), false, nil)
	b := startAddrNode(t, newChain(t), true, nil, other.addr)
	waitFor(t, "başka ağdaki eş işaretlenmedi", func() bool {
		b.ps.reachedMu.Lock()
		defer b.ps.reachedMu.Unlock()
		_, reached := b.ps.reached[other.addr]
		_, wrong := b.ps.wrongNet[other.addr]
		return reached && wrong
	})
	if got := b.ps.reachedAddrs(time.Now()); len(got) != 0 {
		t.Fatalf("başka ağdaki eş verildi: %v", got)
	}
	b.ps.noteNetwork(other.addr, false, time.Now())
	if got := b.ps.reachedAddrs(time.Now()); len(got) != 1 || got[0] != other.addr {
		t.Fatalf("aynı ağ yanıtından sonra %v", got)
	}
}

// startAddrNode adres taraması kısa aralıklı bir regtest düğümü başlatır; discover keşfi
// bellekteki tabloyla açar.
func startAddrNode(t *testing.T, bc *core.Blockchain, discover bool, seeds []string, peers ...string) *node {
	t.Helper()
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ps.addrScanEvery = 20 * time.Millisecond
	for _, p := range peers {
		ps.AddPeer(p)
	}
	if discover {
		ps.EnableDiscovery(MemoryAddrBook(bc), seeds)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return &node{bc: bc, ps: ps, addr: ln.Addr().String()}
}

// A, B'ye sorar; B yalnızca isteğine yanıt aldığı yapılandırılmış eşini (C) ulaşılan adresiyle
// verir. Gömülü liste kaynağı (S) yanıtta yer almaz.
func TestGetAddrEndToEnd(t *testing.T) {
	c := startNode(t, time.Hour)
	_, genesis := c.bc.Network()
	seedResp, _ := json.Marshal(stamped(message{Type: msgAddr, Network: genesis}))
	seed, _ := rawPeer(t, append(seedResp, '\n'))
	b := startAddrNode(t, newChain(t), true, []string{seed}, c.addr)
	waitFor(t, "B senkronizasyon ve gömülü liste isteği yapmadı", func() bool {
		b.ps.reachedMu.Lock()
		defer b.ps.reachedMu.Unlock()
		_, okC := b.ps.reached[c.addr]
		_, okS := b.ps.reached[seed]
		return okC && okS
	})
	if got := b.ps.reachedAddrs(time.Now()); len(got) != 1 || got[0] != c.addr {
		t.Fatalf("B'nin verdiği adresler %v, beklenen [%s]", got, c.addr)
	}

	a := startAddrNode(t, newChain(t), true, nil, b.addr)
	waitFor(t, "A adres öğrenmedi", func() bool { return a.ps.addrs.Len() == 1 })
	if e := a.ps.addrs.snapshot()[c.addr]; e.Source != b.addr {
		t.Fatalf("öğrenilen kayıt %+v", e)
	}
}

// Keşfi kapalı düğüm getaddr sormaz ve gelen isteğe boş yanıt verir.
func TestDiscoveryOff(t *testing.T) {
	c := startNode(t, time.Hour)
	peer, got := rawPeer(t, nil)
	off := startAddrNode(t, newChain(t), false, nil, c.addr, peer)
	waitRounds(t, off.ps, 1)
	timeout := time.After(500 * time.Millisecond)
	for done := false; !done; {
		select {
		case line := <-got:
			if bytes.Contains(line, []byte(`"type":"getaddr"`)) {
				t.Fatal("keşfi kapalı düğüm getaddr sordu")
			}
		case <-timeout:
			done = true
		}
	}
	a := startAddrNode(t, newChain(t), true, nil)
	if n, err := a.ps.askAddrs(off.addr, time.Now()); err != nil || n != 0 {
		t.Fatalf("keşfi kapalı düğümün yanıtı: %d, %v", n, err)
	}
}

// Keşfi açık düğüm gömülü liste kaynağına da sorar; istek ağ kimliğini taşır.
func TestAsksSeeds(t *testing.T) {
	seed, got := rawPeer(t, nil)
	n := startAddrNode(t, newChain(t), true, []string{seed})
	_, genesis := n.bc.Network()
	select {
	case line := <-got:
		var m message
		if err := json.Unmarshal(line, &m); err != nil || m.Type != msgGetAddr || m.Network != genesis {
			t.Fatalf("gömülü listeye giden istek %s", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gömülü liste sorulmadı")
	}
}

// Başka ağın, ağ kimliği olmayan, sınırı aşan ya da çok büyük yanıt reddedilir; eski sürümlü eş
// (yanıtsız kapatır) ağ hatası verir. Hiçbiri tabloya yazılmaz.
func TestAskAddrsRejects(t *testing.T) {
	other := startAddrNode(t, newTestnetChain(t), true, nil)

	a := startAddrNode(t, newChain(t), true, nil)
	if _, err := a.ps.askAddrs(other.addr, time.Now()); !errors.Is(err, errWrongNetwork) {
		t.Fatalf("başka ağın yanıtı: %v", err)
	}

	_, genesis := a.bc.Network()
	resp := func(m message) []byte {
		raw, _ := json.Marshal(stamped(m))
		return append(raw, '\n')
	}
	many := make([]string, maxAddrsPerMsg+1)
	for i := range many {
		many[i] = fmt.Sprintf("127.0.0.%d:1", i+1)
	}
	huge := resp(message{Type: msgAddr, Network: genesis, Hash: strings.Repeat("0", maxAddrMessageBytes)})
	for name, raw := range map[string][]byte{
		"ağ kimliği yok": resp(message{Type: msgAddr, Addrs: []string{"127.0.0.1:1"}}),
		"sınır aşıldı":   resp(message{Type: msgAddr, Network: genesis, Addrs: many}),
		"çok büyük":      huge,
		"eski sürüm":     nil,
	} {
		addr, _ := rawPeer(t, raw)
		n, err := a.ps.askAddrs(addr, time.Now())
		if err == nil || errors.Is(err, errWrongNetwork) || n != 0 {
			t.Fatalf("%s: %d, %v", name, n, err)
		}
	}
	if a.ps.addrs.Len() != 0 {
		t.Fatal("reddedilen yanıt tabloya yazıldı")
	}
}

// Kendiliğinden gelen addr mesajı yok sayılır.
func TestUnsolicitedAddrIgnored(t *testing.T) {
	n := startAddrNode(t, newChain(t), true, nil)
	_, genesis := n.bc.Network()
	conn, err := net.Dial("tcp", n.addr)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(conn).Encode(stamped(message{Type: msgAddr, Network: genesis, Addrs: []string{"127.0.0.1:5"}}))
	conn.Close()
	time.Sleep(200 * time.Millisecond)
	if n.ps.addrs.Len() != 0 {
		t.Fatal("istenmemiş adres öğrenildi")
	}
}
