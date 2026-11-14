package network

import (
	"errors"
	"sync"
	"time"
)

// Uç beklemesi uyandırma kapısı (düğüm politikası, uzlaşma kuralı değil; kullanıcı kararı
// 2026-11-15, G = 2 sn).
//
// Karşılıklı yapılandırılmış iki düğümde (A ve B birbirinin eşi) B yeni bloğu A'ya iter ve
// A'nın uç beklemesini de uyandırırdı; A gövdeyi ayrıca getblock ile çekerdi. Uç bildirimi itme
// kuyruğa girmeden gittiği için getblock çoğunlukla itmeyle aynı anda yürür ve B'nin yukarı
// hattından aynı blok A'ya iki kez gider.
//
// Kapı: uç bekleyen istemci, isteğindeki dinleme portu (Port) ve bağlantısının IP'siyle etkin
// eşlerimizden biriyle eşleşiyorsa (samePeer) ve yeni uç ona itilecekse, o uç için hemen
// uyandırılmaz:
//   - eşe bir blok yazılıyorsa beklenir: bir eşe bloklar sırayla gider, yazılan çoğunlukla
//     yeni ucun ebeveynidir;
//   - eşe yazılan blok yoksa ve bekleyen itmeleri (eşin kuyruğunda ya da iki itme yuvasının
//     boşalmasını bekleyen) en eskisinin kuyruğa girişinden ve eşe son yazmanın bitişinden
//     wakeGrace sonra hâlâ başlamadıysa, eşe bekleyen bütün
//     blok itmeleri iptal edilir ve istemci uyandırılır (kopyalar bir kez, uç beklemesiyle ya da
//     senkronizasyonla gider);
//   - itme başarısız olursa ya da kuyruk taşmasıyla düşerse hemen uyandırılır;
//   - itme yazıldıysa wakeGrace sonra uyandırılır: istemci bloğu almışsa uç başlığını bilir ve
//     indirmez (gövde doğrulaması sürüyorsa adoptTip bitmesini bekler), almadıysa (ör. çözümleme
//     yuvası doluydu ve okumadan kapattı) indirir.
//
// İptal ile itmenin başlaması kapının kilidi altında yarışır: iptal yalnızca bekleyen itmeyi,
// başlama yalnızca iptal edilmemiş itmeyi değiştirir; önce gelen kazanır. İptal edilen itme
// sırası gelince yazılmadan atlanır, devre kesicide başarı ya da hata sayılmaz.
// Erken başarısız olup yeniden denenecek itme yeniden bekliyor sayılır (yeniden deneme beklemesi
// kapıyı tutmaz); aynı blok eşe beklerken ya da yazılırken ikinci kez kuyruğa konmaz.
//
// Eşleşmeyen istemci (NAT arkası, port bildirmeyen eski düğüm, adla yapılandırılmış eş),
// bloğun kaynağı ve yayın kuyruğu dolduğu için yayınlanmayan blok bugünkü gibi hemen uyandırılır.
// Kayıt yoksa (uç değişti ama yayın henüz işlenmedi, blok relay'den geçmedi ya da izlenen
// bloklardan düştü) eşe bir blok yazılıyorsa beklenir, yoksa hemen uyandırılır; itme engellenmez.
// Süre dolarken kapı hâlâ bekletiyorsa istekteki uç başlığıyla yanıtlanır: istemci bunu
// "değişmedi" sayar ve yeniden açar.
const (
	wakeGrace    = 2 * time.Second        // itme yuvası devri (push.go) ile aynı
	wakeGatePoll = 100 * time.Millisecond // kapı bekletirken yeniden bakma aralığı
	wakeHashes   = 16                     // izlenen en son blok sayısı
)

// errPushCancelled itmenin uyandırma kapısınca iptal edildiğini söyler; gönderim sayılmaz.
var errPushCancelled = errors.New("itme uyandırma kapısınca iptal edildi")

type pushPhase uint8

const (
	phaseQueued  pushPhase = iota // bekliyor: eşin kuyruğunda ya da itme yuvasında
	phaseSending                  // yazılıyor (itme yuvası alındı)
	phaseSent                     // yazıldı
	phaseFailed                   // yazılamadı
	phaseDropped                  // kuyruk taşmasıyla düştü ya da kapı iptal etti
)

// pushState eşe bir bloğun itme evresidir; at evrenin başladığı andır (bekleyen itmede kuyruğa
// giriş, wakeGrace buradan sayılır); seq evrenin ait olduğu kuyruk kopyasıdır (pushID).
type pushState struct {
	phase pushPhase
	at    time.Time
	seq   uint64
}

// pushID eşin kuyruğundaki bir blok kopyasıdır: hash bloğun özeti ("": kapı izlemez), seq
// kopyanın kapıdaki sırası. Kapı durumu yalnızca aynı seq'li kopyaya uygulanır: iptal edilip
// kuyrukta kalmış eski kopya, aynı blok yeniden kuyruğa girince yazılmadan atlanır.
type pushID struct {
	hash string
	seq  uint64
}

// relayRecord bir bloğun yayın durumudur: from bloğun kaynağıdır (ona itilmez; ilk boş olmayan
// kalır), queued bloğun bir kez yayın kuyruğuna girdiğini, dropped yayın kuyruğu dolduğu için
// hiç yayınlanmadığını söyler.
type relayRecord struct {
	at      time.Time
	from    string
	queued  bool
	dropped bool
	peers   map[string]*pushState
}

// wakeGate blok başına yayın ve eş başına itme durumlarını tutar; en çok wakeHashes blok.
type wakeGate struct {
	mu    sync.Mutex
	recs  map[string]*relayRecord
	order []string
	seq   uint64
}

// recordLocked bloğun kaydını döndürür; yoksa oluşturur. Kayıtlar doluysa bekleyen ya da
// yazılan itmesi olmayan en eski kayıt atılır (hepsinde varsa en eskisi).
func (g *wakeGate) recordLocked(hash string, now time.Time) *relayRecord {
	if r := g.recs[hash]; r != nil {
		return r
	}
	if g.recs == nil {
		g.recs = map[string]*relayRecord{}
	}
	if len(g.order) == wakeHashes {
		i := 0
		for j, h := range g.order {
			if !g.recs[h].live() {
				i = j
				break
			}
		}
		delete(g.recs, g.order[i])
		g.order = append(g.order[:i], g.order[i+1:]...)
	}
	r := &relayRecord{at: now, peers: map[string]*pushState{}}
	g.recs[hash] = r
	g.order = append(g.order, hash)
	return r
}

// live kayıtta bekleyen ya da yazılan bir itme olup olmadığını söyler.
func (r *relayRecord) live() bool {
	for _, st := range r.peers {
		if st.phase == phaseQueued || st.phase == phaseSending {
			return true
		}
	}
	return false
}

// stateLocked eşe bloğun itme evresini döndürür; kayıt yoksa nil.
func (g *wakeGate) stateLocked(peer, hash string) *pushState {
	if r := g.recs[hash]; r != nil {
		return r.peers[peer]
	}
	return nil
}

// ownLocked eşe kopyanın durumunu döndürür; mine, durumun bu kopyaya ait olduğunu söyler.
func (g *wakeGate) ownLocked(peer string, id pushID) (st *pushState, mine bool) {
	st = g.stateLocked(peer, id.hash)
	return st, st != nil && st.seq == id.seq
}

// relayed bloğun yayın kuyruğuna girdiğini (ok) ya da kuyruk dolu olduğu için düştüğünü işler.
func (g *wakeGate) relayed(hash, from string, ok bool, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.recordLocked(hash, now)
	if r.from == "" {
		r.from = from
	}
	if ok {
		r.queued, r.dropped = true, false
	} else if !r.queued {
		r.dropped = true
	}
}

// queue eşe bloğun itmesinin kuyruğa girdiğini işler ve kopyanın kimliğini döndürür. Aynı blok
// eşe zaten bekliyor ya da yazılıyorsa false döner: ikinci kopya kuyruğa konmaz (ör.
// senkronizasyon turunun aynı bloğu yeniden yayınlaması).
func (g *wakeGate) queue(peer, hash string, now time.Time) (pushID, bool) {
	if hash == "" {
		return pushID{}, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.recordLocked(hash, now)
	if st := r.peers[peer]; st != nil && (st.phase == phaseQueued || st.phase == phaseSending) {
		return pushID{}, false
	}
	g.seq++
	r.peers[peer] = &pushState{phase: phaseQueued, at: now, seq: g.seq}
	return pushID{hash: hash, seq: g.seq}, true
}

// finish kopyanın bekleyen ya da yazılan itmesinin sonucunu (phaseSent, phaseFailed ya da kuyruk
// taşmasında phaseDropped) işler; iptal edilmiş, bitmiş ya da başka kopyaya ait durumu
// değiştirmez, kayıt oluşturmaz.
func (g *wakeGate) finish(peer string, id pushID, ph pushPhase, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, mine := g.ownLocked(peer, id); mine && (st.phase == phaseQueued || st.phase == phaseSending) {
		st.phase, st.at = ph, now
	}
}

// requeue erken başarısız olup yeniden denenecek kopyayı yeniden bekliyor işaretler: yeniden
// deneme beklemesi ve yuva beklemesi kapıyı tutmaz, wakeGrace yeniden sayılır.
func (g *wakeGate) requeue(peer string, id pushID, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, mine := g.ownLocked(peer, id); mine && st.phase == phaseSending {
		st.phase, st.at = phaseQueued, now
	}
}

// forget eşin kuyruğu boşaltıldığında (backup.go) eşe bekleyen itmeleri düşmüş işaretler.
func (g *wakeGate) forget(peer string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.recs {
		if st := r.peers[peer]; st != nil && st.phase == phaseQueued {
			st.phase = phaseDropped
		}
	}
}

// alive kopyanın iptal edilmediğini söyler (itme yuvası beklenmeden önceki ucuz denetim).
func (g *wakeGate) alive(peer string, id pushID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, mine := g.ownLocked(peer, id)
	switch {
	case st == nil:
		return true
	case !mine:
		return false // aynı bloğun daha yeni kopyası kuyrukta
	}
	return st.phase != phaseDropped
}

// begin itme yuvası alındıktan sonra, yazmadan hemen önce çağrılır: kopya iptal edildiyse ya da
// aynı bloğun daha yeni kopyası kuyruktaysa false (yazılmaz), değilse kopyayı yazılıyor işaretler.
// Kaydı olmayan itme engellenmez.
func (g *wakeGate) begin(peer string, id pushID, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, mine := g.ownLocked(peer, id)
	switch {
	case st == nil:
		return true
	case !mine, st.phase == phaseDropped:
		return false
	case st.phase == phaseQueued:
		st.phase, st.at = phaseSending, now
	}
	return true
}

// sendingLocked eşe şu an bir blok yazılıp yazılmadığını söyler.
func (g *wakeGate) sendingLocked(peer string) bool {
	for _, r := range g.recs {
		if st := r.peers[peer]; st != nil && st.phase == phaseSending {
			return true
		}
	}
	return false
}

// idleSinceLocked eşe bekleyen itmelerin ne zamandan beri ilerlemediğidir: en eski bekleyenin
// kuyruğa girişi ile eşe son yazmanın bitişinin (yazıldı ya da yazılamadı) geç olanı. Böylece
// uzun bir yazmanın ardında bekleyen blok, yazma biter bitmez iptal edilmez.
func (g *wakeGate) idleSinceLocked(peer string, at time.Time) time.Time {
	oldest, last := at, time.Time{}
	for _, r := range g.recs {
		st := r.peers[peer]
		switch {
		case st == nil:
		case st.phase == phaseQueued && st.at.Before(oldest):
			oldest = st.at
		case (st.phase == phaseSent || st.phase == phaseFailed) && st.at.After(last):
			last = st.at
		}
	}
	if last.After(oldest) {
		return last
	}
	return oldest
}

// decide istemci eşi peer'ın (boş: eşleşmedi) hash ucu için şimdi uyandırılıp
// uyandırılmayacağını söyler; cancel, eşe bekleyen itmelerin iptal edilmesi gerektiğini söyler
// (o zaman uyandırma, iptal itmenin başlamasından önce gelirse yapılır: cancelPending).
func (g *wakeGate) decide(peer, hash string, now time.Time) (wake, cancel bool) {
	if peer == "" {
		return true, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.recs[hash]
	if r == nil {
		// Uç değişti ama yayın henüz işlenmedi (ya da blok izlenenlerden düştü): eşe bir blok
		// yazılıyorsa (çoğunlukla bu ucun ebeveyni) beklenir, yoksa hemen uyandırılır.
		return !g.sendingLocked(peer), false
	}
	if r.dropped || (r.from != "" && samePeer(peer, r.from)) {
		return true, false
	}
	st := r.peers[peer]
	if st == nil {
		// Yayın işçisi bu eşin kuyruğuna henüz koymadı; takılırsa sınırlı bekle.
		return !now.Before(r.at.Add(wakeGrace)), false
	}
	switch st.phase {
	case phaseQueued:
		if g.sendingLocked(peer) {
			return false, false // önceki blok yazılıyor; bu blok ardından gelir
		}
		late := !now.Before(g.idleSinceLocked(peer, st.at).Add(wakeGrace))
		return late, late
	case phaseSending:
		return false, false
	case phaseSent:
		return !now.Before(st.at.Add(wakeGrace)), false
	}
	return true, false // phaseFailed, phaseDropped
}

// cancelPending eşe bekleyen bütün blok itmelerini iptal eder; hash'in itmesi bu arada başladıysa
// (ya da eşe bir blok yazılıyorsa) hiçbir şeyi değiştirmez ve false döner.
func (g *wakeGate) cancelPending(peer, hash string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.stateLocked(peer, hash)
	if st == nil || st.phase != phaseQueued || g.sendingLocked(peer) {
		return false
	}
	for _, r := range g.recs {
		if p := r.peers[peer]; p != nil && p.phase == phaseQueued {
			p.phase = phaseDropped
		}
	}
	return true
}

// pushTarget addr'ın (pushSource) etkin eşlerimizden hangisi olduğunu söyler; yoksa "".
func (ps *P2PServer) pushTarget(addr string) string {
	if addr == "" {
		return ""
	}
	for _, p := range ps.activePeers() {
		if samePeer(p, addr) {
			return p
		}
	}
	return ""
}

// wakeReady uç bekleyen eşin hash ucu için şimdi uyandırılıp uyandırılmayacağını söyler. Kapı
// bekleyen itmeleri iptal etmek isterse eder; itme bu arada başladıysa iptal edilemez ve bekletir
// (bir sonraki bakışta itme yazılıyor görünür).
func (ps *P2PServer) wakeReady(peer, hash string, now time.Time) bool {
	wake, cancel := ps.wake.decide(peer, hash, now)
	if !cancel {
		return wake
	}
	return ps.wake.cancelPending(peer, hash)
}
