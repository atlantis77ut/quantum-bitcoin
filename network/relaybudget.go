package network

import (
	"time"

	"quantum-bitcoin/core"
)

// Gönderen tarafı işlem yayın bütçesi: karşı düğüm bizden gelen işlemleri adres grubu başına
// bir kovayla sınırlar (txGate: txRateBurst ani, txRatePerSec/sn) ve kovası yetmeyen mesajı
// bütünüyle, cezasız bırakır. Her yayın bütün eşlere gittiğinden ve her eş bizi ayrı sayar,
// tek bir gönderen kovası hepsini kapsar. Kovayı karşı düğümünkinden dar tutarız (oran 7/8,
// ani pay 1/2): ağ gecikmesi mesajları karşı tarafta öbekleyebilir, karşı kova tam başlar ve
// bizim harcadığımızdan hızlı dolar; böylece dürüst yayın orada düşmez. Kuyruk grupları da
// taşma boşaltması da bu kovadan harcar; kova yetmeyen grup tutulur (kuyruk okunmaz, dolarsa
// yeni işlemler taşmaya girer), tutulan grup varken taşma boşaltılmaz (sıra korunur, bkz.
// relayoverflow.go). Değerler karşı düğümün sabitleriyle aynı sürümü varsayar. Kova yalnızca
// yayın döngüsünden erişilir (kilit yok). Havuz eşitlemesi yanıtları (serveTxs) sayılmaz:
// çeken düğüm onları txGate'ten geçirmez. Yapılandırılmış eş yoksa gruplar ve taşma kova
// harcanmadan atılır (gönderilecek kimse yok; kuyruk ve taşma da birikmez). Eş var ama
// ulaşılamıyorsa kova yine harcanır: iade, kısa bir kesintide bütün birikimi hızla tüketip
// kaybettirirdi; bedeli eşler dönünce en çok ~relayBurst/relayRatePerSec (≈2,3 sn) düşük hızdır.

const (
	relayRatePerSec = txRatePerSec * 7 / 8
	relayBurst      = txRateBurst / 2
)

// sendBucket gönderilebilecek işlem sayısının jeton kovasıdır.
type sendBucket struct {
	tokens float64
	last   time.Time
}

func newSendBucket(now time.Time) sendBucket {
	return sendBucket{tokens: relayBurst, last: now}
}

func (b *sendBucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = min(relayBurst, b.tokens+now.Sub(b.last).Seconds()*relayRatePerSec)
		b.last = now
	}
}

// avail şu an harcanabilecek tam jeton sayısıdır.
func (b *sendBucket) avail(now time.Time) int {
	b.refill(now)
	return int(b.tokens)
}

// take n jeton varsa harcar.
func (b *sendBucket) take(n int, now time.Time) bool {
	b.refill(now)
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

// spend n jetonu harcar (önce avail ile sınırlanmış olmalı).
func (b *sendBucket) spend(n int) {
	b.tokens -= float64(n)
	if b.tokens < 0 {
		b.tokens = 0
	}
}

// txRelayState yayın döngüsünün durumudur: bekleyen grup ve gönderen kovası.
type txRelayState struct {
	batch  []core.Transaction
	bucket sendBucket
}

// hasPeers etkin giden eş (yapılandırılmış ya da yedek, backup.go) olup olmadığını söyler:
// yalnızca yedekle çalışan düğüm işlemleri sessizce atmasın.
func (ps *P2PServer) hasPeers() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return len(ps.peers)+len(ps.backups) > 0
}

// broadcastTxs bir işlem grubunu yayar; bütün işlem yayınları buradan geçer.
func (ps *P2PServer) broadcastTxs(txs []core.Transaction) {
	ps.txsSent.Add(int64(len(txs)))
	ps.broadcast(message{Type: msgTxs, Txs: txs})
}

// flushTxBatch bekleyen grubu kova yetiyorsa yayar; grup boşsa, gönderildiyse ya da eş yokken
// atıldıysa true.
func (ps *P2PServer) flushTxBatch(st *txRelayState, now time.Time) bool {
	if len(st.batch) == 0 {
		return true
	}
	if !ps.hasPeers() {
		st.batch = nil // eş yok: kova harcanmaz
		return true
	}
	if !st.bucket.take(len(st.batch), now) {
		return false
	}
	ps.broadcastTxs(st.batch)
	st.batch = nil
	return true
}

// relayTick yayın döngüsünün tur adımıdır: bekleyen grubu yaymayı dener; grup tutuluyorsa ya da
// kuyrukta işlem varsa taşma boşaltılmaz (daha eski işlemler önce gider), yoksa taşma kovanın
// izin verdiği kadar boşaltılır.
func (ps *P2PServer) relayTick(st *txRelayState, now time.Time, stop <-chan struct{}) {
	if !ps.flushTxBatch(st, now) || len(ps.txRelayCh) != 0 {
		return
	}
	if !ps.hasPeers() {
		ps.txOverflow.clear() // eş yok: taşma kova harcanmadan atılır
		return
	}
	if n := st.bucket.avail(now); n > 0 {
		st.bucket.spend(ps.relayOverflow(n, stop))
	}
}
