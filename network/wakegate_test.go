package network

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// set eşe itmenin evresini koşulsuz yazar (yalnızca testlerde durum kurmak için).
func (g *wakeGate) set(peer, hash string, ph pushPhase, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recordLocked(hash, now).peers[peer] = &pushState{phase: ph, at: now}
}

// Kapının kararları: eşleşmeyen istemci, kaydı olmayan ya da yayınlanmayan blok ve kaynak hemen;
// eşe blok yazılırken bekletir; bekleyen itme en eskisinden wakeGrace sonra iptal edilir; yazılan
// itmeden wakeGrace sonra uyandırır.
func TestWakeGateDecide(t *testing.T) {
	t0 := time.Now()
	late := t0.Add(wakeGrace)
	cases := []struct {
		name         string
		setup        func(g *wakeGate)
		at           time.Time
		wake, cancel bool
	}{
		{"kayıt yok", func(*wakeGate) {}, t0, true, false},
		{"kayıt yok, eşe yazılıyor", func(g *wakeGate) { g.set("p", "ebeveyn", phaseSending, t0) }, late, false, false},
		{"yayın kuyruğu dolu", func(g *wakeGate) { g.relayed("h", "", false, t0) }, t0, true, false},
		{"bloğun kaynağı", func(g *wakeGate) { g.relayed("h", "p", true, t0) }, t0, true, false},
		{"kuyruğa girmedi", func(g *wakeGate) { g.relayed("h", "", true, t0) }, t0, false, false},
		{"kuyruğa girmedi, geç", func(g *wakeGate) { g.relayed("h", "", true, t0) }, late, true, false},
		{"bekliyor", func(g *wakeGate) { g.set("p", "h", phaseQueued, t0) }, t0, false, false},
		{"bekliyor, geç", func(g *wakeGate) { g.set("p", "h", phaseQueued, t0) }, late, true, true},
		{"önceki blok yazılıyor", func(g *wakeGate) {
			g.set("p", "ebeveyn", phaseSending, t0)
			g.set("p", "h", phaseQueued, t0)
		}, late.Add(time.Hour), false, false},
		{"en eski bekleyen geç", func(g *wakeGate) {
			g.set("p", "ebeveyn", phaseQueued, t0)
			g.set("p", "h", phaseQueued, late.Add(-time.Second))
		}, late, true, true},
		{"önceki blok yeni yazıldı", func(g *wakeGate) {
			g.set("p", "ebeveyn", phaseSent, late.Add(-time.Second))
			g.set("p", "h", phaseQueued, t0)
		}, late, false, false},
		{"yazılıyor", func(g *wakeGate) { g.set("p", "h", phaseSending, t0) }, late.Add(time.Hour), false, false},
		{"yazıldı", func(g *wakeGate) { g.set("p", "h", phaseSent, t0) }, t0, false, false},
		{"yazıldı, geç", func(g *wakeGate) { g.set("p", "h", phaseSent, t0) }, late, true, false},
		{"başarısız", func(g *wakeGate) { g.set("p", "h", phaseFailed, t0) }, t0, true, false},
		{"düştü", func(g *wakeGate) { g.set("p", "h", phaseDropped, t0) }, t0, true, false},
	}
	for _, c := range cases {
		var g wakeGate
		c.setup(&g)
		if wake, cancel := g.decide("p", "h", c.at); wake != c.wake || cancel != c.cancel {
			t.Fatalf("%s: uyandır %v iptal %v, beklenen %v %v", c.name, wake, cancel, c.wake, c.cancel)
		}
	}
	var g wakeGate
	g.set("p", "h", phaseQueued, t0)
	if wake, _ := g.decide("", "h", late); !wake {
		t.Fatal("eşleşmeyen istemci bekletildi")
	}
}

// İptal ile itmenin başlaması yarışır, önce gelen kazanır; iptal eşe bekleyen bütün itmeleri
// kapsar; kaydı olmayan itme engellenmez.
func TestWakeGateCancelBeginRace(t *testing.T) {
	now := time.Now()
	var g wakeGate
	g.set("p", "h", phaseQueued, now)
	if !g.begin("p", pushID{hash: "h"}, now) || g.cancelPending("p", "h") {
		t.Fatal("başlamış itme iptal edildi")
	}
	var c wakeGate
	c.set("p", "ebeveyn", phaseQueued, now)
	c.set("p", "h", phaseQueued, now)
	c.set("q", "h", phaseQueued, now)
	if !c.cancelPending("p", "h") {
		t.Fatal("bekleyen itme iptal edilmedi")
	}
	if c.alive("p", pushID{hash: "h"}) || c.alive("p", pushID{hash: "ebeveyn"}) ||
		c.begin("p", pushID{hash: "ebeveyn"}, now) {
		t.Fatal("iptal edilen itme yazılabilir")
	}
	if !c.alive("q", pushID{hash: "h"}) || !c.begin("p", pushID{hash: "kayıtsız"}, now) {
		t.Fatal("başka eşin ya da kaydı olmayan itme engellendi")
	}
}

// Kayıt güncellemeleri: aynı blok eşe iki kez kuyruğa girmez; sonuç iptal edilmiş itmeyi
// değiştirmez ve kayıt oluşturmaz; yeniden deneme bekleyen itme wakeGrace'i yeniden sayar;
// kaynak ve yayın durumu ikinci yayında bozulmaz; boşaltılan kuyruğun itmeleri düşer.
func TestWakeGateRecordUpdates(t *testing.T) {
	t0 := time.Now()
	var g wakeGate
	first, ok1 := g.queue("p", "h", t0)
	_, ok2 := g.queue("p", "h", t0)
	_, ok3 := g.queue("q", "h", t0)
	if !ok1 || ok2 || !ok3 {
		t.Fatal("ikinci kopya kuyruğa girdi ya da başka eş engellendi")
	}
	g.finish("p", first, phaseSent, t0)
	if again, ok := g.queue("p", "h", t0); !ok || again.seq == first.seq {
		t.Fatal("yazılmış blok yeniden kuyruğa giremedi")
	}
	g.finish("p", pushID{hash: "yok"}, phaseSent, t0)
	if g.recs["yok"] != nil {
		t.Fatal("sonuç kayıt oluşturdu")
	}
	g.set("p", "iptal", phaseDropped, t0)
	g.finish("p", pushID{hash: "iptal"}, phaseSent, t0)
	if g.alive("p", pushID{hash: "iptal"}) {
		t.Fatal("sonuç iptali geri aldı")
	}

	// İptal edilip kuyrukta kalan eski kopya, blok yeniden kuyruğa girince yazılmaz; eski kopyanın
	// taşmayla düşmesi ya da sonucu yeni kopyanın durumunu değiştirmez.
	var c wakeGate
	old, _ := c.queue("p", "h", t0)
	if !c.cancelPending("p", "h") {
		t.Fatal("bekleyen itme iptal edilmedi")
	}
	cur, ok := c.queue("p", "h", t0)
	if !ok || c.alive("p", old) || c.begin("p", old, t0) {
		t.Fatal("iptal edilen eski kopya yazılabilir")
	}
	c.finish("p", old, phaseDropped, t0)
	if !c.begin("p", cur, t0) {
		t.Fatal("yeni kopya yazılamadı")
	}
	c.finish("p", old, phaseSent, t0)
	if wake, _ := c.decide("p", "h", t0.Add(time.Hour)); wake {
		t.Fatal("eski kopyanın sonucu yazılan yeni kopyanın durumunu değiştirdi")
	}

	var r wakeGate
	r.set("p", "h", phaseSending, t0)
	t1 := t0.Add(time.Minute)
	r.requeue("p", pushID{hash: "h"}, t1)
	if wake, _ := r.decide("p", "h", t1); wake {
		t.Fatal("yeniden deneme beklerken hemen uyandı")
	}
	if wake, cancel := r.decide("p", "h", t1.Add(wakeGrace)); !wake || !cancel {
		t.Fatal("yeniden deneme beklemesi wakeGrace'ten uzun tuttu")
	}

	var y wakeGate
	y.relayed("h", "p", true, t0)
	y.relayed("h", "", false, t0)
	if wake, _ := y.decide("p", "h", t0); !wake {
		t.Fatal("ikinci yayın kaynağı sildi")
	}
	if wake, _ := y.decide("q", "h", t0); wake {
		t.Fatal("dolu kuyruktaki ikinci yayın bloğu yayınlanmamış saydırdı")
	}

	var f wakeGate
	f.set("p", "h", phaseQueued, t0)
	f.set("p", "y", phaseSending, t0)
	f.forget("p")
	if f.alive("p", pushID{hash: "h"}) || !f.alive("p", pushID{hash: "y"}) {
		t.Fatal("boşaltma bekleyeni düşürmedi ya da yazılanı düşürdü")
	}
}

// Kapı en çok wakeHashes blok izler; en eski atılır (atılan blok için istemci hemen uyanır).
func TestWakeGateBounded(t *testing.T) {
	var g wakeGate
	now := time.Now()
	for i := 0; i <= wakeHashes; i++ {
		g.set("p", fmt.Sprint(i), phaseSent, now)
	}
	if len(g.recs) != wakeHashes || len(g.order) != wakeHashes {
		t.Fatalf("%d kayıt, %d sıra", len(g.recs), len(g.order))
	}
	if wake, _ := g.decide("p", "0", now); !wake {
		t.Fatal("atılan blok için istemci bekletildi")
	}
	if wake, _ := g.decide("p", fmt.Sprint(wakeHashes), now); wake {
		t.Fatal("izlenen blok için istemci uyandı")
	}
	// Bekleyen ya da yazılan itmesi olan kayıt atılmaz; yerine en eski bitmiş kayıt atılır.
	var l wakeGate
	l.set("p", "canlı", phaseSending, now)
	for i := 0; i < wakeHashes; i++ {
		l.set("p", fmt.Sprint(i), phaseSent, now)
	}
	if l.recs["canlı"] == nil || l.recs["0"] != nil {
		t.Fatal("canlı kayıt atıldı")
	}
}

// Blok kuyruğu özetleri sırayla taşır; taşan en eskinin özeti döner.
func TestPeerSenderBlockHashes(t *testing.T) {
	var s peerSender
	now := time.Now()
	for i := 0; i < peerBlockQueue; i++ {
		if _, dropped := s.pushBlock([]byte{byte(i)}, pushID{hash: fmt.Sprint(i)}, now); dropped.hash != "" {
			t.Fatalf("taşmadan düştü: %q", dropped.hash)
		}
	}
	if _, dropped := s.pushBlock([]byte{9}, pushID{hash: "9"}, now); dropped.hash != "0" {
		t.Fatalf("taşan %q", dropped.hash)
	}
	var got []string
	for {
		data, id, block, ok := s.nextItem()
		if !ok {
			break
		}
		if !block || fmt.Sprint(int(data[0])) != id.hash {
			t.Fatalf("veri %v özet %q", data, id.hash)
		}
		got = append(got, id.hash)
	}
	if fmt.Sprint(got) != "[1 2 3 9]" {
		t.Fatalf("sıra %v", got)
	}
}

// gateSend bir eşe giden itmenin test kancasındaki kaydıdır.
type gateSend struct {
	peer string
	done chan error
}

// gateServer uyandırma kapısı için bir sunucu kurar: her itme sends'e yazılır ve sonucunu kendi
// done kanalından bekler; yayın işçisi çalışır, itme yuvası devralınmaz.
func gateServer(t *testing.T, peers ...string) (*P2PServer, *core.Blockchain, chan gateSend) {
	t.Helper()
	bc := newChain(t)
	ps := tipTestServer(t, bc)
	ps.pushLim.stall = time.Hour
	ps.pushLim.sendChunk = time.Hour
	for _, p := range peers {
		ps.AddPeer(p)
	}
	sends := make(chan gateSend, 16)
	ps.send = func(peer string, _ []byte) error {
		s := gateSend{peer: peer, done: make(chan error, 1)}
		sends <- s
		return <-s.done
	}
	stop := make(chan struct{})
	go ps.relayLoop(stop)
	t.Cleanup(func() { close(stop) })
	return ps, bc, sends
}

// relayNext zincire bir blok ekler ve yayınlar.
func relayNext(t *testing.T, ps *P2PServer, bc *core.Blockchain) *core.Block {
	t.Helper()
	mineN(t, bc, 1)
	b, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	ps.relay(b)
	return b
}

func nextSend(t *testing.T, sends chan gateSend) gateSend {
	t.Helper()
	select {
	case s := <-sends:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("itme başlamadı")
		return gateSend{}
	}
}

func noSend(t *testing.T, sends chan gateSend, d time.Duration) {
	t.Helper()
	select {
	case s := <-sends:
		t.Fatalf("beklenmeyen itme: %s", s.peer)
	case <-time.After(d):
	}
}

const gateTarget = "127.0.0.1:28555"

// Bloğu itmeyle alacak eş itme sürerken uyandırılmaz; itme yazılınca wakeGrace sonra uyanır.
// Eşleşmeyen istemci hemen uyanır.
func TestWakeGateHoldsPushTarget(t *testing.T) {
	ps, bc, sends := gateServer(t, gateTarget)
	known := bc.TipHash()
	gated := waitTipReplyFrom(t, ps, pid("hedef"), known, gateTarget)
	other := waitTipReplyFrom(t, ps, pid("diğer"), known, "")
	b := relayNext(t, ps, bc)
	s := nextSend(t, sends)
	if m := recvTip(t, other, time.Second); m.Headers[0].Hash != b.Hash {
		t.Fatalf("eşleşmeyen istemci: %+v", m.Headers)
	}
	noTip(t, gated, 500*time.Millisecond)
	s.done <- nil
	sent := time.Now()
	m := recvTip(t, gated, wakeGrace+2*time.Second)
	if m.Headers[0].Hash != b.Hash || time.Since(sent) < wakeGrace-200*time.Millisecond {
		t.Fatalf("itme yazıldıktan %v sonra uyandı: %+v", time.Since(sent), m.Headers)
	}
}

// İtme başarısız olursa eş hemen uyandırılır.
func TestWakeGateFailedPushWakes(t *testing.T) {
	ps, bc, sends := gateServer(t, gateTarget)
	gated := waitTipReplyFrom(t, ps, pid("hedef"), bc.TipHash(), gateTarget)
	b := relayNext(t, ps, bc)
	s := nextSend(t, sends)
	noTip(t, gated, 300*time.Millisecond)
	s.done <- errors.New("yazılamadı")
	if m := recvTip(t, gated, time.Second); m.Headers[0].Hash != b.Hash {
		t.Fatalf("başarısız itmeden sonra: %+v", m.Headers)
	}
}

// Ebeveynin itmesi yazılırken çocuk için uyandırılmaz (ebeveyni olmayan çocuk başlığı
// senkronizasyona kalırdı); ebeveyn ve çocuk sırayla itilir, çocuk yazılınca wakeGrace sonra uyanır.
func TestWakeGateHoldsBehindParent(t *testing.T) {
	ps, bc, sends := gateServer(t, gateTarget)
	known := bc.TipHash()
	parent := relayNext(t, ps, bc)
	sp := nextSend(t, sends)
	gated := waitTipReplyFrom(t, ps, pid("hedef"), known, gateTarget)
	child := relayNext(t, ps, bc)
	waitFor(t, "çocuk kuyruğa girmedi", ps.senderFor(gateTarget).hasBlock)
	noTip(t, gated, wakeGrace+500*time.Millisecond)
	sp.done <- nil
	sc := nextSend(t, sends)
	noTip(t, gated, 500*time.Millisecond)
	sc.done <- nil
	if m := recvTip(t, gated, wakeGrace+2*time.Second); m.Headers[0].Hash != child.Hash {
		t.Fatalf("ebeveyn %s ve çocuktan sonra: %+v", parent.Hash, m.Headers)
	}
}

// İki itme yuvası başka eşlerce tutulurken yuva bekleyen itme wakeGrace sonra iptal edilir ve eş
// uyandırılır; yuvalar boşalınca iptal edilen itme yazılmaz ve devre kesicide hata sayılmaz.
func TestWakeGateCancelsSlotWait(t *testing.T) {
	ps, bc, sends := gateServer(t, "127.0.0.2:1", "127.0.0.3:1")
	relayNext(t, ps, bc)
	holders := []gateSend{nextSend(t, sends), nextSend(t, sends)}
	ps.AddPeer(gateTarget)
	gated := waitTipReplyFrom(t, ps, pid("hedef"), bc.TipHash(), gateTarget)
	b := relayNext(t, ps, bc)
	start := time.Now()
	noTip(t, gated, wakeGrace-time.Second)
	if m := recvTip(t, gated, wakeGrace+2*time.Second); m.Headers[0].Hash != b.Hash {
		t.Fatalf("yuva beklerken: %+v", m.Headers)
	}
	if time.Since(start) < wakeGrace-time.Second {
		t.Fatalf("%v sonra uyandı", time.Since(start))
	}
	// Tutucular ilk bloğu bitirir, sonra ikinci bloğu yazar; hedefe hiçbir şey yazılmaz.
	for _, h := range holders {
		h.done <- nil
	}
	for range holders {
		s := nextSend(t, sends)
		if s.peer == gateTarget {
			t.Fatal("iptal edilen itme yazıldı")
		}
		s.done <- nil
	}
	noSend(t, sends, 500*time.Millisecond)
	if !ps.senderFor(gateTarget).healthy() {
		t.Fatal("iptal edilen itme hata sayıldı")
	}
}
