package network

import (
	"sort"
	"sync"
	"time"

	"quantum-bitcoin/core"
)

// Yer değiştirme yayın kapısı (düğüm politikası, uzlaşma kuralı değil): ücret artışlı yer
// değiştirme, gönderen kovasını (relaybudget.go, 56 işlem/sn, bütün eşler için ortak) havuzu
// büyütmeden tekrar tekrar harcatabilen tek yoldur; yeni işlemler havuz yuvası ister, yuvaları
// havuz sınırı ve tahliye ücreti sınırlar. Yuva başına 10 yer değiştirme sınırı (core) bir ani
// pay bırakıyordu: tek bir gelen grup kabul edilen yer değiştirmelerle bütün yayın bütçemizi
// tüketebilirdi. Kapı bunu (gönderen, nonce) yuvası başına relayReplaceCooldown'da en çok bir
// yayına indirir: yuvanın ilk yayını hemen gider; son yayından sonra relayReplaceCooldown içinde
// gelen yer değiştirme bekletilir (son gelen kazanır) ve süre dolunca, hâlâ havuzdaysa yayılır.
// Dürüst ücret artışı genellikle ilk yayından dakikalar sonra gelir ve hemen gider.
//
// Sıra: bekleyen bir yuva varken aynı göndericinin daha yüksek nonce'lu yayınları da arkasında
// tutulur ve nonce sırasıyla yayılır (yer değiştirme sonraki işlemleri düşürmüş olabilir; yeni
// n+1 eski n'yi tutan eşte ErrReplaceFee ile reddedilir, yetim tamponu bunu kurtarmaz). Bekleyen
// işlemler kimlikle tutulur ve yayın anında havuzdan okunur (kazılan, değiştirilen, tahliye
// edilen atlanır). Kapı bellektedir ve sınırlıdır (relayGateMax yuva; aşılırsa eski kayıtlar
// unutulur, yalnızca sonraki yer değiştirme hemen gider).

const (
	relayReplaceCooldown = 30 * time.Second
	relayGateFlush       = 5 * time.Second
	relayGateMax         = 2 * core.MempoolMaxTxs
)

type relaySlotKey struct {
	sender string
	nonce  uint64
}

// senderHold bir göndericinin bekletilen yayınlarıdır: pending süresi dolmamış yuvalardaki yer
// değiştirmeler, held onların arkasında tutulan daha yüksek nonce'lar (nonce → kimlik).
type senderHold struct {
	pending map[uint64]string
	held    map[uint64]string
}

func (h *senderHold) minPending() uint64 {
	first := true
	var m uint64
	for n := range h.pending {
		if first || n < m {
			m, first = n, false
		}
	}
	return m
}

// relayGate yuva başına son yayın zamanını ve bekletilen yayınları tutar; kendi kilidi vardır ve
// kilit altında havuz ya da yayın çağrısı yapılmaz.
type relayGate struct {
	mu    sync.Mutex
	last  map[relaySlotKey]time.Time
	holds map[string]*senderHold
}

func newRelayGate() *relayGate {
	return &relayGate{last: map[relaySlotKey]time.Time{}, holds: map[string]*senderHold{}}
}

// admit tx'in şimdi yayılıp yayılmayacağını söyler; yayılmayacaksa bekletir.
func (g *relayGate) admit(tx *core.Transaction, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := relaySlotKey{tx.Sender, tx.Nonce}
	if h := g.holds[tx.Sender]; h != nil {
		if _, ok := h.pending[tx.Nonce]; ok {
			h.pending[tx.Nonce] = tx.ID()
			return false
		}
		if tx.Nonce > h.minPending() {
			h.held[tx.Nonce] = tx.ID()
			return false
		}
	}
	if t, ok := g.last[key]; ok && now.Sub(t) < relayReplaceCooldown {
		h := g.holds[tx.Sender]
		if h == nil {
			h = &senderHold{pending: map[uint64]string{}, held: map[uint64]string{}}
			g.holds[tx.Sender] = h
		}
		h.pending[tx.Nonce] = tx.ID()
		// Bu yuvanın arkasında tutulması gereken daha yüksek nonce'lar zaten yayıldı; yalnızca
		// bundan sonra gelenler tutulur.
		return false
	}
	if len(g.last) >= relayGateMax {
		g.pruneLocked(now)
	}
	g.last[key] = now
	return true
}

// pruneLocked süresi dolmuş kayıtları (bekleyen yuvanınkiler dahil: kaydı olmayan bekleyen yuva
// sonraki boşaltmada çıkar) siler; sınır yine aşılıyorsa bekletilmeyen rastgele kayıtları unutur.
func (g *relayGate) pruneLocked(now time.Time) {
	for k, t := range g.last {
		if now.Sub(t) >= relayReplaceCooldown {
			delete(g.last, k)
		}
	}
	for k := range g.last {
		if len(g.last) < relayGateMax {
			return
		}
		if h := g.holds[k.sender]; h != nil {
			if _, ok := h.pending[k.nonce]; ok {
				continue
			}
		}
		delete(g.last, k)
	}
}

// due süresi dolan bekletilmiş yayınları gönderici başına nonce sırasıyla çıkarır: her
// göndericide, süresi dolmamış ilk bekleyen yuvaya kadar olanlar. Yayılanların yuva zamanı
// now olur. Süresi dolmuş kayıtlar da burada silinir.
func (g *relayGate) due(now time.Time) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []string
	for sender, h := range g.holds {
		nonces := make([]uint64, 0, len(h.pending)+len(h.held))
		for n := range h.pending {
			nonces = append(nonces, n)
		}
		for n := range h.held {
			nonces = append(nonces, n)
		}
		sort.Slice(nonces, func(i, j int) bool { return nonces[i] < nonces[j] })
		for _, n := range nonces {
			key := relaySlotKey{sender, n}
			id, isPending := h.pending[n]
			if !isPending {
				id = h.held[n]
			}
			if t, ok := g.last[key]; ok && now.Sub(t) < relayReplaceCooldown {
				// Tutulan işlemin kendi yuvası da yakın zamanda yayıldıysa (yer değiştirme) o da
				// süresini bekler: held yolu yuva sınırını atlatamaz.
				if !isPending {
					delete(h.held, n)
					h.pending[n] = id
				}
				break
			}
			delete(h.pending, n)
			delete(h.held, n)
			g.last[key] = now
			out = append(out, id)
		}
		if len(h.pending) == 0 && len(h.held) == 0 {
			delete(g.holds, sender)
		}
	}
	for k, t := range g.last {
		if now.Sub(t) >= relayReplaceCooldown {
			if h := g.holds[k.sender]; h == nil {
				delete(g.last, k)
			}
		}
	}
	if len(g.last) > relayGateMax {
		g.pruneLocked(now)
	}
	return out
}

// relayTxAt yayın kapısından geçen işlemi yayın kuyruğuna koyar.
func (ps *P2PServer) relayTxAt(tx core.Transaction, now time.Time) {
	if ps.relayGate.admit(&tx, now) {
		ps.enqueueRelay(tx)
	}
}

// flushRelayGate süresi dolan bekletilmiş yayınları havuzdan okuyup kuyruğa koyar.
func (ps *P2PServer) flushRelayGate(now time.Time) {
	ids := ps.relayGate.due(now)
	if len(ids) == 0 {
		return
	}
	for _, tx := range ps.Mempool.Get(ids) {
		ps.enqueueRelay(tx)
	}
}

func (ps *P2PServer) relayGateLoop(stop <-chan struct{}) {
	t := time.NewTicker(relayGateFlush)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ps.flushRelayGate(time.Now())
		}
	}
}
