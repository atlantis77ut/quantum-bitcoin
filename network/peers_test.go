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
		p.noteBlock(pid(fmt.Sprint("g", i)))
	}
	if p.isProtected(pid("g0")) || p.isProtected(pid("g1")) || !p.isProtected(pid("g2")) || !p.isProtected(pid("eş")) {
		t.Fatal("en eski iki grup düşmeli, yapılandırılmış eş kalmalı")
	}
	p.noteBlock(pid("g2")) // yeniden blok getirdi: en yeni olur
	p.noteBlock(pid("yeni"))
	if !p.isProtected(pid("g2")) || p.isProtected(pid("g3")) || len(p.recent) != maxRecentBlockGroups {
		t.Fatalf("en yeniye göre güncellenmedi: %v", p.recent)
	}
}

// Geri çevirme süresi biter; tablo sınırı aşılmaz.
func TestDiscourage(t *testing.T) {
	p := newProtection()
	now := time.Unix(1_900_000_000, 0)
	p.now = func() time.Time { return now }
	p.discourage(pid("g"), discouragePeriod)
	now = now.Add(discouragePeriod - time.Second)
	if !p.isDiscouraged(pid("g")) {
		t.Fatal("süre içinde geri çevrilmedi")
	}
	now = now.Add(time.Second)
	if p.isDiscouraged(pid("g")) {
		t.Fatal("süre bitince geri çevrilmeye devam edildi")
	}
	for i := 0; i < maxDiscouraged+5; i++ {
		p.discourage(pid(fmt.Sprint("k", i)), discouragePeriod)
	}
	if len(p.discouraged) > maxDiscouraged {
		t.Fatalf("tablo %d kayda büyüdü", len(p.discouraged))
	}
	p.addConfigured("eş")
	p.discourage(pid("eş"), discouragePeriod)
	if p.isDiscouraged(pid("eş")) {
		t.Fatal("yapılandırılmış eş geri çevrildi")
	}
}

func TestAddPeerProtectsGroup(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	ps.AddPeer("203.0.113.5:8001")
	if !ps.prot.isProtected(peerIDFor("203.0.113.9")) || ps.prot.isProtected(peerIDFor("203.0.114.9")) {
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
	ps.handleBlock("r", pid("kötü"), &bad)
	if !ps.prot.isDiscouraged(pid("kötü")) || ps.prot.isProtected(pid("kötü")) {
		t.Fatal("geçersiz blok gönderen grup geri çevrilmedi")
	}
	ps.handleBlock("r", pid("iyi"), b)
	if bc.TipHash() != b.Hash || !ps.prot.isProtected(pid("iyi")) || ps.prot.isDiscouraged(pid("iyi")) {
		t.Fatal("ucu uzatan blok getiren grup korunmadı")
	}
	ps.handleBlock("r", pid("geç"), b) // bilinen blok: koruma kazandırmaz
	if ps.prot.isProtected(pid("geç")) {
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
	errs := []error{typeErr, io.EOF, io.ErrUnexpectedEOF, errors.New("i/o timeout")}
	// Tür ya da aralık dışı sürüm alanı: mesaj çözülmez ve cezasız bırakılır.
	for _, s := range []string{`{"type":"block","version":"x"}`, `{"type":"block","version":4294967296}`,
		`{"type":"block","version":-1}`, `{"type":"block","services":-1}`} {
		var m message
		err := json.Unmarshal([]byte(s), &m)
		if err == nil {
			t.Fatalf("%s çözüldü", s)
		}
		errs = append(errs, err)
	}
	for _, err := range errs {
		if malformedMessage(err) {
			t.Fatalf("%v bozuk mesaj sayıldı", err)
		}
	}
}

// Geri çevrilen grubun bağlantısı kabul anında kapanır.
func TestDiscouragedClosedAtAccept(t *testing.T) {
	n := startNode(t, time.Hour)
	n.ps.prot.discourage(peerIDFor("127.0.0.1"), discouragePeriod)
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
