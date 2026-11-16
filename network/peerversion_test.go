package network

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// versionPeer her isteğe verilen sürüm, yetenek ve ağ kimliğiyle yanıt veren sahte eştir.
func versionPeer(t *testing.T, version uint32, services uint64, network string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var req message
			if json.NewDecoder(c).Decode(&req) == nil {
				json.NewEncoder(c).Encode(message{Type: msgHeaders, Version: version, Services: services, Network: network})
			}
			c.Close()
		}
	}()
	return ln.Addr().String()
}

// Son yanıt kaydedilir; daha eski sürüme dönen eşin kaydı da iner.
func TestPeerVersionRecorded(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	newer := versionPeer(t, protocolVersion+1, 1<<5, ps.genesis)
	if _, err := ps.request(newer, message{Type: msgGetHeaders}, 1<<20); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if v, s, ok := ps.peerVersion(newer, now); !ok || v != protocolVersion+1 || s != 1<<5 {
		t.Fatalf("kayıt %d %d %v", v, s, ok)
	}
	if !ps.peerSupports(newer, protocolVersion+1, now) || ps.peerSupports(newer, protocolVersion+2, now) {
		t.Fatal("peerSupports kayda uymuyor")
	}

	// Aynı adrese eski sürümle yanıt: kayıt iner.
	ps.noteReached(newer, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, &message{Version: 3}, now)
	if v, _, ok := ps.peerVersion(newer, now); !ok || v != 3 {
		t.Fatalf("eski sürüme dönen eş: %d %v", v, ok)
	}
	if ps.peerSupports(newer, protocolVersion, now) {
		t.Fatal("sürüm 3'e dönen eş sürüm 4'ü destekliyor sayıldı")
	}
}

// Desteklenmeyen sürümdeki ve başka ağdaki yanıtlar kaydedilmez.
func TestPeerVersionNotRecordedOnReject(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	other := versionPeer(t, protocolVersion, 0, strings.Repeat("ab", 32))
	if _, err := ps.request(other, message{Type: msgGetHeaders}, 1<<20); !errors.Is(err, errWrongNetwork) {
		t.Fatalf("başka ağ: %v", err)
	}
	ps.minVersion = protocolVersion + 1
	old := versionPeer(t, protocolVersion, 0, ps.genesis)
	if _, err := ps.request(old, message{Type: msgGetHeaders}, 1<<20); !errors.Is(err, errPeerVersion) {
		t.Fatalf("eski sürüm: %v", err)
	}
	now := time.Now()
	for _, p := range []string{other, old} {
		if _, _, ok := ps.peerVersion(p, now); ok {
			t.Fatalf("%s reddedilen yanıtla kaydedildi", p)
		}
	}
}

// Bilinmeyen ya da bayat kayıtlı eş yalnızca minVersion'ı destekler; budanan kayıt silinir.
func TestPeerVersionUnknownStaleAndPruned(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	now := time.Now()
	const peer = "127.0.0.1:1"
	if !ps.peerSupports(peer, ps.minVersion, now) || ps.peerSupports(peer, ps.minVersion+1, now) {
		t.Fatal("bilinmeyen eş yalnızca minVersion'ı desteklemeli")
	}
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
	ps.noteReached(peer, addr, &message{Version: protocolVersion}, now)
	if !ps.peerSupports(peer, protocolVersion, now.Add(peerVersionTTL)) {
		t.Fatal("TTL sınırındaki kayıt bayat sayıldı")
	}
	if ps.peerSupports(peer, protocolVersion, now.Add(peerVersionTTL+time.Second)) {
		t.Fatal("bayat kayıt kullanıldı")
	}
	ps.pruneReached() // eş yapılandırılmış değil, yedek değil: kayıt silinir
	if _, _, ok := ps.peerVersion(peer, now); ok {
		t.Fatal("budanan eşin kaydı kaldı")
	}
}
