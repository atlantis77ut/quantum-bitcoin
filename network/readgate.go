package network

import (
	"errors"
	"net"
	"net/http"

	"quantum-bitcoin/core"
)

// Eski blok okuma sınırı (feat/old-block-reads; core/blockcache.go "Önbellek kaçırma kapısı").
// Önbellekte olmayan bir gövde diskten okunup çözülür; istek sayan oran sınırı bu maliyeti
// görmez, farklı eski bloklarla dolaşan istemci önbelleği atlatır. /block, /latestblock,
// /transaction ve /merkleproof kaçırmada iki sınırdan geçer; önbellekten gelen okumalar ve diğer
// uç noktalar etkilenmez:
//
//   - İstemci başına bedel: kaçırma, isteğin kendi jetonuna ek olarak istemcinin oran kovasından
//     blockMissCost jeton harcar (aynı kova: IPv4 adresi, IPv6 /64, kota dışı /64'lerde /48'in
//     ortak kovası). Yetmezse hiç harcamadan 429 döner; aynı istemcinin önbellekten gelen
//     okumaları sürer. Varsayılanlarla istemci başına saniyede en çok ~2 kaçırma.
//   - Genel sınır: aynı anda en çok blockMissSlots eski blok kaçırması diskten okunur; dolunca
//     beklemeden 503 döner (kapı zincir kilidi altında sorulur, beklemez; yuva disk okuması ve
//     çözme bitene kadar, kilit dışında tutulur).
//   - Uç yuvası: uç kaçırmaları genel sınırı değil kendi tek yuvalarını kullanır (tipMissSlots).
//     Yeni uç önbelleğe ilk API okumasında girer; eski blok yağmuru genel yuvaları doldursa da
//     uç okunabilsin. Ayrı yuva, ucu önbellekten düşürüp (8 kayıt) çok IP'den aynı anda ucu
//     okutan saldırganın eşzamanlı çözme sayısını da bire indirir. Aynı anda gelen ilk okumalardan
//     biri çözer, diğerleri o çözme (~13 ms) bitene kadar 503 + Retry-After alır. Uç yine istemci
//     bedelini öder.
//
// Böylece aynı anda en çok blockMissSlots + tipMissSlots gövde API için çözülür. Bedeli: yeterince
// çok IP'si olan saldırgan yuvaları doldurup dürüst istemcilerin önbellekte olmayan okumalarına
// 503 aldırabilir (bellek ve CPU baskısı yerine yalnızca bu okumalarda erişilebilirlik sınırı;
// önbellekteki bloklar ve diğer uç noktalar etkilenmez).
const (
	blockMissCost  = 5
	blockMissSlots = 2
	tipMissSlots   = 1
)

var (
	errReadRate = errors.New("eski blok okuma sınırı (istemci)")
	errReadBusy = errors.New("eski blok okuma sınırı (genel)")
)

// readGate isteğin istemcisi için önbellek kaçırma kapısını kurar (core.ReadGate).
func (api *APIServer) readGate(r *http.Request) core.ReadGate {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return func(tip bool) (func(), error) {
		slots := api.missSlots
		if tip {
			slots = api.tipSlots
		}
		var release func()
		select {
		case slots <- struct{}{}:
			release = func() { <-slots }
		default:
			return nil, errReadBusy
		}
		if !api.limiter.allowCostHost(ip, blockMissCost) {
			release()
			return nil, errReadRate
		}
		return release, nil
	}
}

// writeReadGateError kapının reddini 429 ya da 503 olarak yazar; err kapı reddi değilse false.
func writeReadGateError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errReadRate):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "çok fazla eski blok okuması"})
	case errors.Is(err, errReadBusy):
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "önbellekte olmayan blok okumaları şu an dolu"})
	default:
		return false
	}
	return true
}
