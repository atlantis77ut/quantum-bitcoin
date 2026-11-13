package network

import (
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// Gönderen kovası karşı düğümünkinden dardır, tam başlar, kesirli dolar ve tavanı aşmaz.
func TestSendBucket(t *testing.T) {
	if relayRatePerSec >= txRatePerSec || relayBurst >= txRateBurst || relayBurst < maxTxsPerMsg {
		t.Fatalf("gönderen kovası (%d/sn, %d) karşı kovadan (%d/sn, %d) dar olmalı ve bir grubu almalı",
			relayRatePerSec, relayBurst, txRatePerSec, txRateBurst)
	}
	t0 := time.Unix(1_900_000_000, 0)
	b := newSendBucket(t0)
	if b.avail(t0) != relayBurst || !b.take(relayBurst, t0) || b.take(1, t0) {
		t.Fatal("ani pay uygulanmadı")
	}
	half := t0.Add(500 * time.Millisecond)
	if got := b.avail(half); got != relayRatePerSec/2 {
		t.Fatalf("yarım saniyede %d jeton, beklenen %d", got, relayRatePerSec/2)
	}
	if got := b.avail(t0.Add(time.Hour)); got != relayBurst {
		t.Fatalf("tavan aşıldı: %d", got)
	}
	if got := b.avail(t0); got != relayBurst {
		t.Fatalf("geri giden saat jeton değiştirdi: %d", got)
	}
	b.spend(relayBurst + 5)
	if b.tokens != 0 {
		t.Fatalf("harcama sıfırın altına indi: %v", b.tokens)
	}
}

func fakeBatch(start, n int) []core.Transaction {
	out := make([]core.Transaction, n)
	for i := range out {
		out[i] = fakeTx(uint64(start + i))
	}
	return out
}

// Kova yetmeyen grup tutulur ve tutulurken taşma boşaltılmaz; kova dolunca önce grup, sonra
// taşma gider. Kuyrukta işlem varken taşma boşaltılmaz.
func TestRelayTickHoldsBatchAndOverflow(t *testing.T) {
	bc := newChain(t)
	ps := NewP2PServer("127.0.0.1:0", bc) // eş yok: yayın kimseye gitmez
	from := fundKey(t, bc)
	tx := policyTx(t, ps.Mempool, from, 5)
	if _, err := ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	ps.txOverflow.mu.Lock()
	ps.txOverflow.pushLocked(tx.ID())
	ps.txOverflow.mu.Unlock()
	stop := make(chan struct{})
	defer close(stop)

	t0 := time.Unix(1_900_000_000, 0)
	st := &txRelayState{batch: fakeBatch(0, maxTxsPerMsg), bucket: sendBucket{tokens: 10, last: t0}}
	ps.relayTick(st, t0, stop)
	if len(st.batch) != maxTxsPerMsg || ps.txsSent.Load() != 0 || ps.txOverflow.len() != 1 {
		t.Fatalf("kova yetmezken: grup %d, gönderilen %d, taşma %d", len(st.batch), ps.txsSent.Load(), ps.txOverflow.len())
	}

	t1 := t0.Add(time.Second)
	ps.relayTick(st, t1, stop)
	if len(st.batch) != 0 || ps.txsSent.Load() != maxTxsPerMsg+1 || ps.txOverflow.len() != 0 {
		t.Fatalf("kova dolunca: grup %d, gönderilen %d, taşma %d", len(st.batch), ps.txsSent.Load(), ps.txOverflow.len())
	}
	if want := 10 + relayRatePerSec - maxTxsPerMsg - 1; st.bucket.avail(t1) != want {
		t.Fatalf("kalan jeton %d, beklenen %d", st.bucket.avail(t1), want)
	}

	// Kuyrukta daha eski işlem varken taşma beklemeli.
	ps.txOverflow.mu.Lock()
	ps.txOverflow.pushLocked(tx.ID())
	ps.txOverflow.mu.Unlock()
	ps.txRelayCh <- fakeTx(999)
	ps.relayTick(st, t1.Add(time.Second), stop)
	if ps.txOverflow.len() != 1 || ps.txsSent.Load() != maxTxsPerMsg+1 {
		t.Fatal("kuyruk boşalmadan taşma boşaltıldı")
	}
}

// Uzun bir pencerede gönderilen işlem sayısı ani pay + oran × süreyi aşmaz.
func TestRelayBudgetWindow(t *testing.T) {
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	t0 := time.Unix(1_900_000_000, 0)
	st := &txRelayState{bucket: newSendBucket(t0)}
	const secs = 30
	next := 0
	for ms := 0; ms <= secs*1000; ms += 50 {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		if len(st.batch) == 0 {
			st.batch = fakeBatch(next, maxTxsPerMsg)
			next += maxTxsPerMsg
		}
		ps.flushTxBatch(st, now)
	}
	sent := ps.txsSent.Load()
	if limit := int64(relayBurst + secs*relayRatePerSec); sent > limit || sent < limit-2*maxTxsPerMsg {
		t.Fatalf("%d sn'de %d işlem gönderildi, üst sınır %d", secs, sent, limit)
	}
}
