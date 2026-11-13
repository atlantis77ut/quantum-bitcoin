package network

import (
	"time"

	"quantum-bitcoin/core"
)

// Geri yüklenen havuzun yayını (düğüm politikası): kalıcı kayıttan yüklenen işlemler eskiden
// eşlere yeniden yayılmazdı (yalnızca yapılandırılmış eşler havuz eşitlemesiyle çekerdi). Artık
// LoadMempool yüklenen kimlikleri hatırlar; düğüm ilk senkronizasyon turunu bitirip en az zincir
// işini geçince (yükleme eski uca göre yapıldı) bu kimliklerden hâlâ havuzda olanlar o anki
// Select sırasıyla (her gönderenin nonce'ları boşluksuz) yayın taşmasına alınır ve gönderen
// kovasının bütçesiyle yayılır (relaybudget.go). Kanala yazılmaz: taşma sırası değişmezi korunur
// (relayoverflow.go). Taşmaya sığmayanlar havuz eşitlemesine kalır. Yayın bir kez yapılır.

// restoredReadyPoll senkronizasyon beklenirken denetim aralığıdır.
const restoredReadyPoll = time.Second

// LoadMempool havuzun kalıcı kaydını yükler (core.Mempool.Load) ve yüklenen kimlikleri ilk
// senkronizasyondan sonra yayılmak üzere hatırlar. Ağ ve API işlem almadan önce, bir kez
// çağrılmalıdır.
func (ps *P2PServer) LoadMempool() (restored, skipped int, err error) {
	restored, skipped, err = ps.Mempool.Load()
	if err != nil || restored == 0 {
		return restored, skipped, err
	}
	ids, ierr := ps.Mempool.Inventory(core.MempoolMaxTxs)
	if ierr != nil {
		ps.log.Warn("geri yüklenen havuz kimlikleri okunamadı; yeniden yayılmayacak", "err", ierr)
		return restored, skipped, nil
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	ps.restoredMu.Lock()
	ps.restored = set
	ps.restoredMu.Unlock()
	return restored, skipped, nil
}

// restoredReady geri yüklenen işlemlerin yayılabileceğini söyler: ilk senkronizasyon turu bitti
// (eşsiz ya da ulaşılamayan eşlerle de biter) ve düğüm en az zincir işinin üstünde.
func (ps *P2PServer) restoredReady() bool {
	return ps.syncRounds.Load() >= 1 && !ps.Blockchain.BelowMinChainWork()
}

// enqueueRestored hatırlanan kimliklerden hâlâ havuzda olanları o anki Select sırasıyla yayın
// taşmasına alır ve kümeyi boşaltır; alınan sayıyı döndürür.
func (ps *P2PServer) enqueueRestored() int {
	ps.restoredMu.Lock()
	set := ps.restored
	ps.restored = nil
	ps.restoredMu.Unlock()
	if len(set) == 0 {
		return 0
	}
	ids, err := ps.Mempool.Inventory(core.MempoolMaxTxs)
	if err != nil {
		ps.log.Warn("havuz kimlikleri okunamadı; geri yüklenen işlemler yayılmadı", "err", err)
		return 0
	}
	o := ps.txOverflow
	o.mu.Lock()
	defer o.mu.Unlock()
	n, lost := 0, 0
	for _, id := range ids {
		if _, ok := set[id]; !ok {
			continue
		}
		if o.pushLocked(id) {
			n++
		} else {
			lost++
		}
	}
	if lost > 0 {
		ps.log.Debug("yayın taşması dolu; geri yüklenen işlemlerin bir kısmı havuz eşitlemesine kaldı", "adet", lost)
	}
	return n
}

// restoredRelayLoop geri yüklenen işlemleri hazır olunca bir kez taşmaya alır.
func (ps *P2PServer) restoredRelayLoop(stop <-chan struct{}) {
	ps.restoredMu.Lock()
	pending := len(ps.restored) > 0
	ps.restoredMu.Unlock()
	if !pending {
		return
	}
	t := time.NewTicker(restoredReadyPoll)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if ps.restoredReady() {
				if n := ps.enqueueRestored(); n > 0 {
					ps.log.Info("geri yüklenen işlemler yayın kuyruğuna alındı", "adet", n)
				}
				return
			}
		}
	}
}
