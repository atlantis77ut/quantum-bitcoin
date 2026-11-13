package network

import (
	"fmt"
	"testing"
	"time"
)

// pid, IP olmayan test anahtarı için grubu ve iç anahtarı aynı olan kimliktir.
func pid(s string) peerID { return peerID{group: s, inner: s} }

func TestPeerIDFor(t *testing.T) {
	for _, c := range []struct{ host, group, inner string }{
		{"203.0.113.7", "203.0.113.0/24", "203.0.113.7/32"},
		{"::ffff:203.0.113.7", "203.0.113.0/24", "203.0.113.7/32"},
		{"2001:db8:1:2:aaaa::1", "2001:db8:1::/48", "2001:db8:1:2::/64"},
		{"pipe", "pipe", "pipe"},
	} {
		if got := peerIDFor(c.host); got.group != c.group || got.inner != c.inner {
			t.Fatalf("%s: %+v", c.host, got)
		}
	}
	if peerIDFor("203.0.113.7").group != peerGroup("203.0.113.7") {
		t.Fatal("dış grup peerGroup'tan farklı")
	}
}

// Aynı iç anahtarın tekrarı yükseltmez; pencere dışına düşen ceza sayılmaz; pencere içinde
// innerEscalateK farklı iç anahtar yükseltir.
func TestEscalator(t *testing.T) {
	e := newEscalator()
	t0 := time.Unix(1_900_000_000, 0)
	a, b, c := peerIDFor("203.0.113.1"), peerIDFor("203.0.113.2"), peerIDFor("203.0.113.3")
	for i := 0; i < 5; i++ {
		if e.note(a, t0) {
			t.Fatal("aynı iç anahtar yükseltti")
		}
	}
	e.note(b, t0)
	if e.note(c, t0.Add(innerEscalateWindow)) {
		t.Fatal("pencere dışındaki cezalar sayıldı")
	}
	e2 := newEscalator()
	e2.note(a, t0)
	e2.note(b, t0.Add(time.Minute))
	if !e2.note(c, t0.Add(2*time.Minute)) {
		t.Fatal("üç farklı iç anahtar yükseltmedi")
	}
	if e2.note(pid("x"), t0) || e2.note(pid("x"), t0) || e2.note(pid("x"), t0) {
		t.Fatal("IP olmayan anahtar yükseltti")
	}
	if len(e2.hits[a.group]) > innerEscalateK {
		t.Fatalf("grup kaydı %d", len(e2.hits[a.group]))
	}
	for i := 0; i < maxEscalatorGroups+5; i++ {
		e2.note(peerIDFor(fmt.Sprintf("10.%d.%d.1", i/256, i%256)), t0)
	}
	if len(e2.hits) > maxEscalatorGroups {
		t.Fatalf("tablo %d gruba büyüdü", len(e2.hits))
	}
}

// İşlem cezası iç anahtarındır: komşu etkilenmez, grup kovasını paylaşır; üç farklı iç anahtar
// cezalanınca bütün grup cezalanır.
func TestTxGateInnerKey(t *testing.T) {
	g := newTxGate()
	now := time.Unix(1_900_000_000, 0)
	g.now = func() time.Time { return now }
	a, b, c := peerIDFor("203.0.113.1"), peerIDFor("203.0.113.2"), peerIDFor("203.0.113.3")
	n := peerIDFor("203.0.113.9")
	g.penalize(a)
	if g.allow(a, 1) || !g.allow(n, 1) {
		t.Fatal("ceza iç anahtarla sınırlı değil")
	}
	if !g.allow(b, txRateBurst-1) || g.allow(n, 1) {
		t.Fatal("grup kovası paylaşılmıyor")
	}
	now = now.Add(time.Second)
	g.penalize(b)
	g.penalize(c)
	if g.allow(n, 1) {
		t.Fatal("üç iç anahtar cezalanınca grup cezalanmadı")
	}
	before := len(g.bans)
	g.penalize(peerIDFor("203.0.113.50")) // grubu cezalı eş tabloya kayıt eklemez
	if len(g.bans) != before {
		t.Fatalf("cezalı grupta yeni kayıt: %d → %d", before, len(g.bans))
	}
	if !g.allow(peerIDFor("203.0.114.9"), 1) {
		t.Fatal("başka grup etkilendi")
	}
	now = now.Add(badTxPenalty)
	if !g.allow(n, 1) {
		t.Fatal("grup cezası bitmedi")
	}
}

// Geri çevirme iç anahtarındır; üç farklı iç anahtar grubu geri çevirir; daha kısa ceza süren
// uzun cezayı kısaltmaz; yapılandırılmış grup hiç geri çevrilmez.
func TestDiscourageInnerKey(t *testing.T) {
	p := newProtection()
	now := time.Unix(1_900_000_000, 0)
	p.now = func() time.Time { return now }
	a, b, c := peerIDFor("203.0.113.1"), peerIDFor("203.0.113.2"), peerIDFor("203.0.113.3")
	n := peerIDFor("203.0.113.9")
	p.discourage(a, discouragePeriod)
	if !p.isDiscouraged(a) || p.isDiscouraged(n) {
		t.Fatal("geri çevirme iç anahtarla sınırlı değil")
	}
	p.discourage(a, time.Minute) // kısa ceza süren uzun cezayı kısaltmaz
	now = now.Add(2 * time.Minute)
	if !p.isDiscouraged(a) {
		t.Fatal("kısa ceza uzun cezayı kısalttı")
	}
	p.discourage(b, time.Minute)
	p.discourage(c, time.Minute)
	if !p.isDiscouraged(n) {
		t.Fatal("üç iç anahtar geri çevrilince grup geri çevrilmedi")
	}
	now = now.Add(discouragePeriod)
	if p.isDiscouraged(n) || p.isDiscouraged(a) {
		t.Fatal("süre bitince geri çevirme sürdü")
	}

	cfg := peerIDFor("198.51.100.1")
	p.addConfigured(cfg.group)
	for i := 1; i <= innerEscalateK; i++ {
		id := peerIDFor(fmt.Sprintf("198.51.100.%d", i))
		p.discourage(id, discouragePeriod)
		p.badDifficulty(id)
		p.badDifficulty(id)
	}
	if p.isDiscouraged(cfg) || p.isDiscouraged(peerIDFor("198.51.100.200")) {
		t.Fatal("yapılandırılmış grup geri çevrildi")
	}
}

// Kazanılmış koruma bloğu getiren iç anahtarındır ve bir grup listede en çok bir yer tutar.
func TestRecentInnerKey(t *testing.T) {
	p := newProtection()
	a, n := peerIDFor("203.0.113.1"), peerIDFor("203.0.113.9")
	p.noteBlock(a)
	if !p.isProtected(a) || p.isProtected(n) {
		t.Fatal("koruma iç anahtarla sınırlı değil")
	}
	p.noteBlock(n)
	if len(p.recent) != 1 || !p.isProtected(n) || p.isProtected(a) {
		t.Fatalf("grup birden çok yer tuttu: %v", p.recent)
	}
	for i := 0; i < maxRecentBlockGroups; i++ {
		p.noteBlock(peerIDFor(fmt.Sprintf("203.0.113.%d", 10+i)))
	}
	if len(p.recent) != 1 {
		t.Fatalf("tek /24 listeyi doldurdu: %d", len(p.recent))
	}
}

// ErrBadDifficulty kademelidir: ilk ihlal uyarı, ikincisi badDifficultyPenalty, sonrakiler ikiye
// katlanır (tavan discouragePeriod); son ceza bittikten sonra strikeReset boyunca ihlal olmazsa
// sayaç sıfırlanır; uyarılar da grup yükseltmesine sayılır.
func TestBadDifficultyGraded(t *testing.T) {
	p := newProtection()
	now := time.Unix(1_900_000_000, 0)
	p.now = func() time.Time { return now }
	a := peerIDFor("203.0.113.1")
	want := []time.Duration{0, badDifficultyPenalty, 2 * badDifficultyPenalty, 4 * badDifficultyPenalty,
		discouragePeriod, discouragePeriod}
	for i, w := range want {
		if d := p.badDifficulty(a); d != w {
			t.Fatalf("%d. ihlal: %v, beklenen %v", i+1, d, w)
		}
		if (w > 0) != p.isDiscouraged(a) {
			t.Fatalf("%d. ihlal: geri çevirme %v", i+1, p.isDiscouraged(a))
		}
	}
	now = now.Add(discouragePeriod) // tavan cezası bitti: sayaç hemen sıfırlanmaz
	if d := p.badDifficulty(a); d != discouragePeriod {
		t.Fatalf("tavan cezasından hemen sonra: %v", d)
	}
	now = now.Add(discouragePeriod + strikeReset)
	if d := p.badDifficulty(a); d != 0 {
		t.Fatalf("sayaç sıfırlanmadı: %v", d)
	}

	q := newProtection()
	q.now = p.now
	for i := 1; i <= innerEscalateK; i++ {
		if d := q.badDifficulty(peerIDFor(fmt.Sprintf("192.0.2.%d", i))); d != 0 {
			t.Fatalf("ilk ihlal geri çevirdi: %v", d)
		}
	}
	if !q.isDiscouraged(peerIDFor("192.0.2.200")) {
		t.Fatal("uyarılar grup yükseltmesine sayılmadı")
	}
}

// Uçtan uca: bilinen ebeveynde yanlış hedefli ilk blok uyarıdır, ikincisi eşi geri çevirir.
func TestHandleBlockBadDifficultyGraded(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc)
	tpl, err := bc.NewTemplate(nil, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	bad := *tpl.Block
	bad.Bits ^= 1 // ebeveyn bilinir: beklenen hedef tutmaz (PoW'dan önce reddedilir)
	id := peerIDFor("203.0.113.1")
	ps.handleBlock("r", id, &bad)
	if ps.prot.isDiscouraged(id) {
		t.Fatal("ilk ihlal geri çevirdi")
	}
	ps.handleBlock("r", id, &bad)
	if !ps.prot.isDiscouraged(id) || ps.prot.isDiscouraged(peerIDFor("203.0.113.9")) {
		t.Fatal("ikinci ihlal yalnızca eşi geri çevirmedi")
	}
}
