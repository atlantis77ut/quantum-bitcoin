package network

import (
	"errors"
	"fmt"
	"net"
	"time"

	"quantum-bitcoin/core"
)

// Havuz eşitlemesi: düğüm havuzunu yapılandırılmış eşlerden çeker (gelen bağlantının eşine geri
// bağlanılamadığından itme ile değil, çekme ile). Açılışta (havuz bellekte tutulduğundan
// yeniden başlatmada boştur), yayın kuyruğu taştığında ya da işlem yayını kaçırıldığında
// eksikler en geç mempoolSyncInterval sonra gelir. Her tur: getmempool → inv (en çok
// MempoolMaxTxs kimlik, Select sırasıyla: göndericinin nonce'ları boşluksuz gelir) → eksikler
// maxPullTxs'lik gettxs → txs ile istenir ve havuz politikasıyla (core.Mempool.Add) kabul
// edilir. Çekilen işlemler yeniden yayılmaz: her düğüm kendisi çeker; aksi halde soğuk
// başlangıçta binlerce işlem yayın kuyruğunu taşırıp gerçekten yeni işlemleri düşürürdü.
//
// Çekme yanıtı bizim isteğimizde okunur ve gelen mesaj çözümleme yuvası kullanmaz; bu yüzden
// grup itme mesajından (16) büyüktür. Sunucu tarafında istek sunum yuvasıyla sınırlıdır.
const (
	msgGetMempool = "getmempool" // → inv
	msgInv        = "inv"
	msgGetTxs     = "gettxs" // kimlikler → txs
)

const (
	maxPullTxs          = 64        // bir gettxs isteğindeki en çok kimlik (~480 KiB yanıt)
	maxInvMessageBytes  = 512 << 10 // 5.000 kimlik × ~67 bayt
	maxPullMessageBytes = 1 << 20
	mempoolSyncInterval = 10 * time.Minute
	// mempoolPullBudget eş başına çekme turunun süre bütçesidir: senkronizasyon döngüsü eşlerle
	// sırayla çalıştığından çekme blok senkronizasyonunu en çok bu süre + transferTimeout
	// (~90 sn) geciktirir. İlerleme varken dolarsa sonraki senkronizasyon turunda devam edilir.
	mempoolPullBudget = 30 * time.Second
	// pullFullStop art arda bu kadar ErrMempoolFull'dan sonra tur biter: envanter ücrete göre
	// sıralıdır, sonrakiler de sığmaz.
	pullFullStop = 16
)

// maybePullMempool eşten havuz eşitlemesinin zamanı geldiyse çeker. Düğüm geride kalmışsa
// (senkronizasyon süresi doldu, ön-senkronizasyon sürüyor ya da en az zincir işinin altında)
// eşin ucunda geçerli işlemler bizde nonce farkıyla düşeceğinden tur atlanır. syncFrom
// hatasız bitip düğüm yine de geride kalmışsa (ör. pencere sınırı) çekme yapılır; işlemler
// Mempool.Add'in durum denetimiyle reddedilir, yalnızca bant boşa gider. Yalnızca
// senkronizasyon döngüsünden çağrılır.
func (ps *P2PServer) maybePullMempool(peer string, syncErr error, stop <-chan struct{}) {
	if errors.Is(syncErr, errSyncBudget) || ps.presyncs[peer] != nil || ps.Blockchain.BelowMinChainWork() {
		return
	}
	if last, ok := ps.lastMempoolSync[peer]; ok && time.Since(last) < ps.mempoolSyncEvery {
		return
	}
	ps.lastMempoolSync[peer] = time.Now()
	n, err := ps.pullMempool(peer, stop)
	if errors.Is(err, errSyncBudget) && n > 0 {
		// İlerleme var: kalan eksikler 10 dk değil, sonraki senkronizasyon turunda istenir.
		delete(ps.lastMempoolSync, peer)
	}
	if err != nil {
		ps.log.Warn("eşten havuz eşitlemesi başarısız", "peer", peer, "eklenen", n, "err", err)
		return
	}
	if n > 0 {
		ps.log.Info("eşten havuz eşitlendi", "peer", peer, "eklenen", n)
	}
}

// pullMempool eşin havuzundan bizde eksik işlemleri çeker; eklenen işlem sayısını döndürür.
// Tur mempoolPullBudget ile sınırlıdır (errSyncBudget).
func (ps *P2PServer) pullMempool(peer string, stop <-chan struct{}) (int, error) {
	deadline := time.Now().Add(mempoolPullBudget)
	resp, err := ps.request(peer, message{Type: msgGetMempool}, maxInvMessageBytes)
	if err != nil {
		return 0, err
	}
	if resp.Type != msgInv || len(resp.TxIDs) > core.MempoolMaxTxs {
		return 0, errBadResponse
	}
	for _, id := range resp.TxIDs {
		if !core.IsHashHex(id) {
			return 0, errBadResponse
		}
	}
	missing := ps.Mempool.Missing(resp.TxIDs)
	added, full := 0, 0
	for len(missing) > 0 {
		select {
		case <-stop:
			return added, nil
		default:
		}
		if time.Now().After(deadline) {
			return added, errSyncBudget
		}
		batch := missing[:min(maxPullTxs, len(missing))]
		missing = missing[len(batch):]
		resp, err := ps.request(peer, message{Type: msgGetTxs, TxIDs: batch}, maxPullMessageBytes)
		if err != nil {
			return added, err
		}
		txs, err := checkPulled(resp, batch)
		if err != nil {
			return added, err
		}
		for _, tx := range txs {
			_, err := ps.Mempool.Add(tx)
			switch {
			case err == nil:
				added++
				full = 0
			case invalidTx(err):
				// Yapılandırılmış eş geri çevrilmez; tek yaptırım turu kesmektir.
				return added, fmt.Errorf("%w: geçersiz işlem: %v", errBadResponse, err)
			case errors.Is(err, core.ErrMempoolFull):
				if full++; full >= pullFullStop {
					return added, nil
				}
			default:
				// Durum farkı (nonce, kabul payı, yer değiştirme ücreti, yineleme): olağan.
			}
		}
	}
	return added, nil
}

// checkPulled gettxs yanıtını denetler: tür txs, işlem sayısı istenenden fazla değil ve her
// işlem istenen, yinelenmeyen bir kimliğe sahip.
func checkPulled(resp *message, requested []string) ([]core.Transaction, error) {
	if resp.Type != msgTxs || len(resp.Txs) > len(requested) {
		return nil, errBadResponse
	}
	want := make(map[string]bool, len(requested))
	for _, id := range requested {
		want[id] = true
	}
	for i := range resp.Txs {
		id := resp.Txs[i].ID()
		if !want[id] {
			return nil, fmt.Errorf("%w: istenmeyen ya da yinelenen işlem", errBadResponse)
		}
		delete(want, id)
	}
	return resp.Txs, nil
}

// serveMempool havuzun kimliklerini Select sırasıyla sunar. Düğüm senkronize değilken (en az
// zincir işinin altında) eski havuz sunulmaz.
func (ps *P2PServer) serveMempool(conn net.Conn, remote, group string) {
	ok, lvl := ps.acquireServe(group)
	if !ok {
		return
	}
	defer ps.releaseServe(group, lvl)
	var ids []string
	if !ps.Blockchain.BelowMinChainWork() {
		var err error
		if ids, err = ps.Mempool.Inventory(core.MempoolMaxTxs); err != nil {
			ps.log.Error("havuz envanteri okunamadı", "err", err)
			return
		}
	}
	ps.reply(conn, remote, message{Type: msgInv, TxIDs: ids})
}

// serveTxs istenen kimliklerden havuzda olanları sunar; en çok maxPullTxs kimlik, her biri
// kanonik hex olmalıdır.
func (ps *P2PServer) serveTxs(conn net.Conn, remote, group string, ids []string) {
	if len(ids) > maxPullTxs {
		ps.log.Debug("gettxs çok uzun", "remote", remote, "adet", len(ids))
		return
	}
	for _, id := range ids {
		if !core.IsHashHex(id) {
			ps.log.Debug("geçersiz işlem kimliği", "remote", remote)
			return
		}
	}
	ok, lvl := ps.acquireServe(group)
	if !ok {
		return
	}
	defer ps.releaseServe(group, lvl)
	var txs []core.Transaction
	if !ps.Blockchain.BelowMinChainWork() {
		txs = ps.Mempool.Get(ids)
	}
	ps.reply(conn, remote, message{Type: msgTxs, Txs: txs})
}
