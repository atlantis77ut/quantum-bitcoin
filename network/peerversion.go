package network

import "time"

// Eş başına sürüm kaydı (düğüm politikası; docs/MAINNET-ONCESI.md). TTL ve "son yanıt geçerli"
// kuralı uzlaşma dışı varsayılanlardır, ilk okuyan yayınla birlikte yeniden değerlendirilir.
// Tek yönlü yayınlar (blok, işlem grubu) pazarlık yapamaz; ileride yeni bir biçim yalnızca onu
// desteklediğini bildiren eşe gönderilebilsin diye eşin son bildirdiği protokol sürümü ve yetenek
// bitleri tutulur.
//
// Kaynak yalnızca kendi isteklerimizin (request) yanıtlarıdır: aradığımız adres yayın listesindeki
// dizgeyle aynıdır (activePeers hem senkronizasyonun hem yayının hedefidir). Gelen mesajlardan
// kaydedilmez: kaynak portu geçicidir ve Port alanı doğrulanmaz, gelen mesaj bir arama adresine
// bağlanamaz. Desteklenmeyen sürümdeki ve başka ağdaki yanıtlar kaydedilmez (request).
//
// Son yanıt geçerlidir (en yükseği değil): eski sürüme dönen eşin kaydı da iner. Kayıt
// peerVersionTTL'den eskiyse bilinmiyor sayılır; bilinmeyen eş minVersion'da varsayılır. Kayıt
// yalnızca ipucudur: sürüm alanı doğrulanmaz (yüksek sürüm bildiren eş yalnızca kendine yeni
// biçimi gönderttirir, düşük bildiren eski biçimi alır) ve TTL içinde eş eski sürüme dönebilir.
// Bu yüzden yeni biçim yine eski düğümlerin cezasız yok sayabileceği biçimde olmalıdır (p2p.go,
// "Geçiş kuralları"). Kayıtlar reached ile birlikte budanır (pruneReached).

// peerVersionTTL bir sürüm kaydının geçerli sayıldığı süredir. Senkronizasyon turları
// syncInterval aralığıyla etkin eşlere istek atar; on turdur yanıt vermeyen eşin kaydı bayat
// sayılır (eş minVersion'da varsayılır: güvenli yön).
const peerVersionTTL = 10 * syncInterval

// peerVersion eşin son yanıtında bildirdiği sürüm ve yetenek bitleridir; kayıt yoksa ya da
// peerVersionTTL'den eskiyse ok false'tur.
func (ps *P2PServer) peerVersion(peer string, now time.Time) (version uint32, services uint64, ok bool) {
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	r, found := ps.reached[peer]
	if !found || now.Sub(r.at) > peerVersionTTL {
		return 0, 0, false
	}
	return r.version, r.services, true
}

// peerSupports eşin v sürümündeki bir biçimi anladığının bilinip bilinmediğini söyler. minVersion
// ve altı her eş için doğrudur; üstü için taze bir kayıt gerekir.
func (ps *P2PServer) peerSupports(peer string, v uint32, now time.Time) bool {
	if v <= ps.minVersion {
		return true
	}
	got, _, ok := ps.peerVersion(peer, now)
	return ok && got >= v
}
