package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// Havuz 4, ayrılan 2 (1'i yalnızca yapılandırılmış eşlere): korunmayan 2, blokla korunan
// paylaşılan 3'e kadar, yapılandırılmış eş toplamın tamamına kadar alabilir.
func TestSlotPoolReservation(t *testing.T) {
	p := newSlotPool(4, 2)
	if !p.tryAcquire(levelNone) || !p.tryAcquire(levelNone) || p.tryAcquire(levelNone) {
		t.Fatal("korunmayan pay 2 olmalı")
	}
	if !p.tryAcquire(levelEarned) || p.tryAcquire(levelEarned) {
		t.Fatal("blokla korunan yapılandırılmış eşlerin payına girmemeli")
	}
	if !p.tryAcquire(levelConfigured) || p.tryAcquire(levelConfigured) {
		t.Fatal("yapılandırılmış eş kendi payını almalı, toplamı aşmamalı")
	}
	if p.acquire(levelNone, 20*time.Millisecond) {
		t.Fatal("dolu korunmayan pay beklemeyle alındı")
	}
	p.release(levelNone)
	if !p.acquire(levelEarned, 20*time.Millisecond) {
		t.Fatal("boşalan yuvayı blokla korunan alamadı")
	}
	// Korunmayan payda yer var ama paylaşılan pay dolu: jeton geri verilmeli.
	if p.tryAcquire(levelNone) || len(p.open) != 1 || len(p.shared) != 3 || len(p.all) != 4 {
		t.Fatalf("jeton sızdı: açık %d, paylaşılan %d, toplam %d", len(p.open), len(p.shared), len(p.all))
	}
	p.release(levelConfigured)
	p.release(levelEarned)
	p.release(levelEarned)
	p.release(levelNone)
	if len(p.open) != 0 || len(p.shared) != 0 || len(p.all) != 0 {
		t.Fatalf("bırakılan jetonlar dengesiz: açık %d, paylaşılan %d, toplam %d", len(p.open), len(p.shared), len(p.all))
	}
}

// Blokla kazanılan koruma en yeni maxRecentBlockGroups grupla sınırlıdır; yapılandırılmış eş
// her zaman korunur.
func TestProtectionRecentBlocks(t *testing.T) {
	p := newProtection()
	p.addConfigured("eş")
	for i := 0; i < maxRecentBlockGroups+2; i++ {
		p.noteBlock(fmt.Sprint("g", i))
	}
	if p.isProtected("g0") || p.isProtected("g1") || !p.isProtected("g2") || !p.isProtected("eş") {
		t.Fatal("en eski iki grup düşmeli, yapılandırılmış eş kalmalı")
	}
	p.noteBlock("g2") // yeniden blok getirdi: en yeni olur
	p.noteBlock("yeni")
	if !p.isProtected("g2") || p.isProtected("g3") || len(p.recent) != maxRecentBlockGroups {
		t.Fatalf("en yeniye göre güncellenmedi: %v", p.recent)
	}
}

// Geri çevirme süresi biter; tablo sınırı aşılmaz.
func TestDiscourage(t *testing.T) {
	p := newProtection()
	now := time.Unix(1_900_000_000, 0)
	p.now = func() time.Time { return now }
	p.discourage("g")
	now = now.Add(discouragePeriod - time.Second)
	if !p.isDiscouraged("g") {
		t.Fatal("süre içinde geri çevrilmedi")
	}
	now = now.Add(time.Second)
	if p.isDiscouraged("g") {
		t.Fatal("süre bitince geri çevrilmeye devam edildi")
	}
	for i := 0; i < maxDiscouraged+5; i++ {
		p.discourage(fmt.Sprint("k", i))
	}
	if len(p.discouraged) > maxDiscouraged {
		t.Fatalf("tablo %d kayda büyüdü", len(p.discouraged))
	}
	p.addConfigured("eş")
	p.discourage("eş")
	if p.isDiscouraged("eş") {
		t.Fatal("yapılandırılmış eş geri çevrildi")
	}
}

func TestAddPeerProtectsGroup(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.AddPeer("203.0.113.5:8001")
	if !ps.prot.isProtected(peerGroup("203.0.113.9")) || ps.prot.isProtected(peerGroup("203.0.114.9")) {
		t.Fatal("yapılandırılmış eşin /24 grubu korunmalı, başkası değil")
	}
}

// Ucu uzatan yeni blok getiren grup korunur; özeti tutmayan blok gönderen grup geri çevrilir.
func TestHandleBlockProtectsAndDiscourages(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	tpl, err := bc.NewTemplate(nil, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := bc.Mine(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := *b
	bad.Nonce++ // özet artık başlığa uymaz
	ps.handleBlock("r", "kötü", &bad)
	if !ps.prot.isDiscouraged("kötü") || ps.prot.isProtected("kötü") {
		t.Fatal("geçersiz blok gönderen grup geri çevrilmedi")
	}
	ps.handleBlock("r", "iyi", b)
	if bc.TipHash() != b.Hash || !ps.prot.isProtected("iyi") || ps.prot.isDiscouraged("iyi") {
		t.Fatal("ucu uzatan blok getiren grup korunmadı")
	}
	ps.handleBlock("r", "geç", b) // bilinen blok: koruma kazandırmaz
	if ps.prot.isProtected("geç") {
		t.Fatal("bilinen blok koruma kazandırdı")
	}
}

func TestMalformedMessage(t *testing.T) {
	var v map[string]interface{}
	if err := json.Unmarshal([]byte("{x"), &v); !malformedMessage(err) {
		t.Fatalf("sözdizimi hatası sayılmadı: %v", err)
	}
	var m message
	typeErr := json.Unmarshal([]byte(`{"type":5}`), &m) // sürüm farkı dürüst eşe de çarpar
	for _, err := range []error{typeErr, io.EOF, io.ErrUnexpectedEOF, errors.New("i/o timeout")} {
		if malformedMessage(err) {
			t.Fatalf("%v bozuk mesaj sayıldı", err)
		}
	}
}

// Geri çevrilen grubun bağlantısı kabul anında kapanır.
func TestDiscouragedClosedAtAccept(t *testing.T) {
	n := startNode(t, time.Hour)
	n.ps.prot.discourage(peerGroup("127.0.0.1"))
	conn, err := net.Dial("tcp", n.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !isConnReset(err) {
		t.Fatalf("bağlantı kapanmadı: %v", err)
	}
}

func isConnReset(err error) bool {
	var ne net.Error
	return err != nil && !(errors.As(err, &ne) && ne.Timeout())
}
