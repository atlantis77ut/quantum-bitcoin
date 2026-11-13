package network

import (
	"net"
	"time"
)

// İç anahtar (docs/ES-KIMLIGI.md §4 adım 3; kullanıcı kararı 2026-11-13). Gelen eş iki anahtarla
// tanınır: dış grup (IPv4 /24, IPv6 /48) kotaların ve ortak bütçelerin (bağlantı, çözümleme,
// sunum yuvaları, işlem hız kovası) sınırıdır; iç anahtar (IPv4 /32, IPv6 /64) ceza, geri
// çevirme ve blokla kazanılan korumanın anahtarıdır. Böylece aynı /24'teki bir kiracı komşusunu
// tek bir geçersiz mesajla cezalandıramaz; yeni bir iç anahtar da grubun bütçesini paylaşır,
// bedava yeni bütçe getirmez. Bir grupta innerEscalateWindow içinde innerEscalateK farklı iç
// anahtar cezalanırsa ceza bütün gruba yükselir: /24 içinde adres değiştiren (ya da bir /48
// içinde ~65.000 /64 alan) saldırgan bir pencerede en çok innerEscalateK-1 ceza alabilir.
// Pencere ceza süresinden kısa olduğundan sürekli hız pencere başına bu kadardır (geri
// çevirmede grup başına saatte ~12, eskiden 1); bu, pencere kararının bilinen bedelidir.

const (
	innerEscalateK      = 3
	innerEscalateWindow = 10 * time.Minute
	maxEscalatorGroups  = 10_000
)

// peerID gelen eşin dış grubu ve iç anahtarıdır. Adres IP değilse ikisi de adresin kendisidir.
type peerID struct {
	group string
	inner string
}

// peerIDFor bir hostun anahtarlarıdır; IPv4 eşlemeli IPv6 adresi IPv4 sayılır (peerGroup gibi).
func peerIDFor(host string) peerID {
	ip := net.ParseIP(host)
	if ip == nil {
		return peerID{group: host, inner: host}
	}
	if v4 := ip.To4(); v4 != nil {
		return peerID{group: peerGroup(host), inner: v4.String() + "/32"}
	}
	return peerID{group: peerGroup(host), inner: ip.Mask(net.CIDRMask(64, 128)).String() + "/64"}
}

func peerIDOf(a net.Addr) peerID {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return peerID{group: a.String(), inner: a.String()}
	}
	return peerIDFor(host)
}

type innerHit struct {
	inner string
	at    time.Time
}

// escalator dış grup başına pencere içinde cezalanan farklı iç anahtarları sayar. Kendi kilidi
// yoktur; sahibinin kilidi altında kullanılır. Grup başına en çok innerEscalateK kayıt, en çok
// maxEscalatorGroups grup tutulur.
type escalator struct {
	hits map[string][]innerHit
}

func newEscalator() *escalator {
	return &escalator{hits: map[string][]innerHit{}}
}

// note iç anahtarın cezasını kaydeder; grupta pencere içinde innerEscalateK farklı iç anahtar
// cezalandıysa true döner.
func (e *escalator) note(id peerID, now time.Time) bool {
	if id.inner == id.group {
		return false // IP olmayan adres: iç anahtar grubun kendisidir
	}
	var live []innerHit
	for _, h := range e.hits[id.group] {
		if now.Sub(h.at) < innerEscalateWindow && h.inner != id.inner {
			live = append(live, h)
		}
	}
	live = append(live, innerHit{inner: id.inner, at: now})
	if len(live) > innerEscalateK {
		live = live[len(live)-innerEscalateK:]
	}
	if _, ok := e.hits[id.group]; !ok && len(e.hits) >= maxEscalatorGroups {
		e.pruneLocked(now)
	}
	e.hits[id.group] = live
	return len(live) >= innerEscalateK
}

// pruneLocked süresi dolan kayıtları siler; tablo yine doluysa rastgele grupları unutur (map
// sırası rastgeledir). Tabloyu doldurmak maxEscalatorGroups ayrı grup ister; o zaman başka
// grupların sayaçları da sıfırlanabilir.
func (e *escalator) pruneLocked(now time.Time) {
	for g, hs := range e.hits {
		if now.Sub(hs[len(hs)-1].at) >= innerEscalateWindow {
			delete(e.hits, g)
		}
	}
	for g := range e.hits {
		if len(e.hits) < maxEscalatorGroups {
			return
		}
		delete(e.hits, g)
	}
}
