package network

import (
	"time"
)

// Ağ kimliği (protokol sürümü 4): her mesaj gönderenin genesis özetini taşır (stamp, Network).
// Amaç yanlış yapılandırmayı (testnet düğümünü mainnet'e bağlamak gibi) erken ve açıkça
// yakalamaktır; alan kimlik doğrulamaz, saldırıya karşı savunma değildir (eş istediği özeti
// yazabilir, sürüm alanı gibi).
//
// Kurallar:
//   - Alanı boş gelen mesaj kabul edilir (sürüm 4'ten eski düğümler).
//   - Gelen tek yönlü itmeler (msgBlock, msgTxs) başka ağdansa işlenmeden bırakılır: başka ağın
//     işlemi imza alanı yüzünden geçersiz görünür ve dürüst ama yanlış yapılandırılmış eşe işlem
//     cezası verdirirdi; bloğu da yetim olarak boşuna işlenirdi. Ceza ve geri çevirme yoktur.
//   - Gelen istekler (getblock, getheaders, getaddr, waittip, ...) her zaman yanıtlanır: yanıt
//     bizim ağ kimliğimizi taşır, isteyen tarafın denetimi (request) farkı kesin olarak görür.
//     İsteği düşürmek isteyene yalnızca EOF gösterir, geçici ağ hatası sanılıp yeniden denenirdi.
//   - Giden isteğin yanıtı başka ağdansa request errWrongNetwork döner ve eşi ulaşılmış saymaz.
//     Yapılandırılmış eş için operatöre seyrek bir uyarı yazılır; ötekiler Debug'dır (gelen
//     trafik saldırgan denetimindedir, log taşkını olmasın).

// wrongNetWarnEvery aynı yapılandırılmış eş için "başka ağda" uyarısının en sık aralığıdır.
const wrongNetWarnEvery = time.Hour

// otherNetwork n'nin dolu ve bizim ağ kimliğimizden farklı olduğunu söyler.
func (ps *P2PServer) otherNetwork(n string) bool {
	return n != "" && n != ps.genesis
}

// dropOtherNetwork başka ağdan gelen tek yönlü itmeyi bırakır (true: mesaj işlenmez).
func (ps *P2PServer) dropOtherNetwork(msg message, remote string) bool {
	if !ps.otherNetwork(msg.Network) || (msg.Type != msgBlock && msg.Type != msgTxs) {
		return false
	}
	ps.log.Debug("başka ağdan gelen itme bırakıldı", "remote", remote, "type", msg.Type, "ag", msg.Network)
	return true
}

// warnOtherNetwork başka ağda yanıt veren eşi kaydeder: yapılandırılmış eşse en çok
// wrongNetWarnEvery'de bir Warn, değilse Debug.
func (ps *P2PServer) warnOtherNetwork(peer, network string, now time.Time) {
	configured := false
	for _, p := range ps.Peers() {
		if p == peer {
			configured = true
			break
		}
	}
	if !configured {
		ps.log.Debug("eş başka bir ağda", "es", peer, "ag", network)
		return
	}
	ps.netWarnMu.Lock()
	last, seen := ps.netWarn[peer]
	warn := !seen || now.Sub(last) >= wrongNetWarnEvery
	if warn {
		ps.netWarn[peer] = now
	}
	ps.netWarnMu.Unlock()
	if warn {
		ps.log.Warn("yapılandırılmış eş başka bir ağda (farklı genesis); -network ve eş adresini denetleyin",
			"es", peer, "es_agi", network, "bizim_ag", ps.genesis)
	}
}
