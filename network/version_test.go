package network

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// rawPeer gelen her bağlantının ilk satırını got'a koyar ve varsa resp'i yanıt olarak yazar.
func rawPeer(t *testing.T, resp []byte) (string, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []byte, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if line, err := bufio.NewReader(c).ReadBytes('\n'); err == nil {
				got <- line
				if resp != nil {
					c.Write(resp)
				}
			}
			c.Close()
		}
	}()
	return ln.Addr().String(), got
}

func recvLine(t *testing.T, got <-chan []byte) []byte {
	t.Helper()
	select {
	case line := <-got:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("eşe mesaj gelmedi")
		return nil
	}
}

// Yayın, istek ve yanıt kodlanırken sürümümüzü taşır.
func TestMessagesStamped(t *testing.T) {
	want := []byte(fmt.Sprintf(`"version":%d`, protocolVersion))
	resp, err := json.Marshal(stamped(message{Type: msgInv}))
	if err != nil {
		t.Fatal(err)
	}
	addr, got := rawPeer(t, append(resp, '\n'))
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.AddPeer(addr)

	ps.broadcast(message{Type: msgTxs})
	if line := recvLine(t, got); !bytes.Contains(line, want) {
		t.Fatalf("yayın sürümsüz: %s", line)
	}
	if _, err := ps.request(addr, message{Type: msgGetMempool}, maxInvMessageBytes); err != nil {
		t.Fatal(err)
	}
	if line := recvLine(t, got); !bytes.Contains(line, want) {
		t.Fatalf("istek sürümsüz: %s", line)
	}

	a, b := net.Pipe()
	go func() {
		ps.reply(a, "r", message{Type: msgInv})
		a.Close()
	}()
	line, err := bufio.NewReader(b).ReadBytes('\n')
	b.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(line, want) {
		t.Fatalf("yanıt sürümsüz: %s", line)
	}
}

// Sürümsüz yanıt minPeerVersion 0 iken kabul edilir; sınırın altındaki yanıt errPeerVersion
// döner (geçici hata, ceza yok).
func TestRequestVersionGate(t *testing.T) {
	addr, _ := rawPeer(t, []byte(`{"type":"inv"}`+"\n"))
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	if _, err := ps.request(addr, message{Type: msgGetMempool}, maxInvMessageBytes); err != nil {
		t.Fatalf("sürümsüz yanıt reddedildi: %v", err)
	}
	ps.minVersion = protocolVersion
	if _, err := ps.request(addr, message{Type: msgGetMempool}, maxInvMessageBytes); !errors.Is(err, errPeerVersion) {
		t.Fatalf("eski sürümdeki yanıt: %v, beklenen errPeerVersion", err)
	}
}

// Sınırın altındaki geçerli blok doğrulamaya gelmeden cezasız bırakılır; sınırdaki ya da daha
// yeni sürümdeki aynı blok işlenir.
func TestInboundVersionGate(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.minVersion = protocolVersion + 1
	tpl, err := bc.NewTemplate(nil, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	blk, err := bc.Mine(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	deliver := func(v uint32) {
		a, c := net.Pipe()
		go func() {
			json.NewEncoder(a).Encode(message{Type: msgBlock, Block: blk, Version: v})
			a.Close()
		}()
		ps.handleConnection(c, "g")
	}
	before := bc.TipHash()
	deliver(protocolVersion)
	if bc.TipHash() != before {
		t.Fatal("desteklenmeyen sürümdeki blok işlendi")
	}
	if ps.prot.isDiscouraged("g") {
		t.Fatal("sürüm farkı cezalandırıldı")
	}
	deliver(protocolVersion + 1)
	if bc.TipHash() != blk.Hash {
		t.Fatal("desteklenen sürümdeki blok işlenmedi")
	}
}

// Varsayılan sınırda sürümsüz (eski) ve daha yeni sürümlü mesajlar işlenir; bilinmeyen alanlar
// ve yetenek bitleri yok sayılır.
func TestDefaultVersionAccepts(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	tpl, err := bc.NewTemplate(nil, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	blk, err := bc.Mine(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(blk)
	if err != nil {
		t.Fatal(err)
	}
	a, c := net.Pipe()
	go func() {
		fmt.Fprintf(a, `{"type":"block","block":%s,"version":%d,"services":18446744073709551615,"yeni":1}`+"\n",
			raw, protocolVersion+5)
		a.Close()
	}()
	ps.handleConnection(c, "g")
	if bc.TipHash() != blk.Hash {
		t.Fatal("daha yeni sürümdeki blok işlenmedi")
	}

	tpl, err = bc.NewTemplate(nil, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	next, err := bc.Mine(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	a2, c2 := net.Pipe() // ayrı değişken: ilk yazıcı a'yı hâlâ kapatıyor olabilir
	go func() {
		json.NewEncoder(a2).Encode(struct {
			Type  string      `json:"type"`
			Block *core.Block `json:"block"`
		}{msgBlock, next})
		a2.Close()
	}()
	ps.handleConnection(c2, "g")
	if bc.TipHash() != next.Hash {
		t.Fatal("sürümsüz blok işlenmedi")
	}
}
