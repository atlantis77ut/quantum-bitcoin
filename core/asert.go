package core

import "math/big"

// Zorluk ayarlaması: ASERT (aserti3-2d, Bitcoin Cash 2020-11-15 belirtimi), her blokta.
// Kullanıcı kararı (2026-11-08, devils-advocate sonrası): yarı ömür 2 saat, çapa 1. blok,
// testnet'te en düşük zorluk kuralı yok, regtest'te yeniden hedefleme yok.
//
// Hedef yalnızca ebeveyne ve çapaya bağlıdır, bloğun kendi zaman damgasına bağlı değildir:
// madencinin zaman damgasını tazelemesi hedefi değiştirmez. Formül çapaya göre mutlaktır
// (ideal takvimden sapmanın üstel fonksiyonu); hata birikmez ve emisyon takvime bağlı kalır.
//
// Çapa 1. bloktur, genesis değil: genesis'in zamanı ile kazımın başladığı an arasındaki boşluk
// ASERT'e "takvimin gerisinde" görünür ve hedefi 2^(boşluk/yarı ömür) kat kolaylaştırırdı
// (lansmanda gizli instamine). 1. blok Params.Bits ile kazılır; zamanı ideal takvimin
// başlangıcıdır. 2. blok da çapanın hedefini taşır (ebeveyni çapanın kendisidir), ayarlama
// 3. bloktan itibaren işler.

// referenceHalfLife yarı ömürdür (saniye). Zaman cinsinden sabittir: blok süresi lansmandan
// önce 30 sn'ye inerse yarı ömür 240 blok olur. Tek bir bloğun zaman damgasını FTL kadar ileri
// atmanın kazancı 2^(FTL/yarı ömür) − 1 ≈ %2,9'dur ve birikmez (Validate sınırı denetler).
const referenceHalfLife int64 = 2 * 60 * 60

// minHalfLifePerFTL: yarı ömür en az FTL'in bu katı olmalı (tek blok kazancı ≤ ~%5).
const minHalfLifePerFTL = 14

// asertTarget aserti3-2d tamsayı algoritmasıdır, belirtimdeki tanımlarla:
// timeDelta = değerlendirilen bloğun zamanı − çapanın ebeveyninin zamanı,
// heightDelta = değerlendirilen bloğun yüksekliği − çapanın yüksekliği. Sonuç, değerlendirilen
// bloğun çocuğunun hedefidir. Belirtimdeki 64 bit ara değerler taşmasın diye big.Int
// kullanılır; bölme sıfıra doğru keser (belirtimdeki gibi; Python'un // işleci değil).
func asertTarget(anchorTarget *big.Int, timeDelta, heightDelta, spacing, halfLife int64, maxTarget *big.Int) *big.Int {
	// exponent = trunc((timeDelta − spacing·(heightDelta+1)) · 2^16 / halfLife), 16.16 sabit nokta
	num := new(big.Int).Mul(big.NewInt(spacing), new(big.Int).Add(big.NewInt(heightDelta), big.NewInt(1)))
	num.Sub(big.NewInt(timeDelta), num)
	num.Lsh(num, 16)
	exponent := num.Quo(num, big.NewInt(halfLife))

	shifts := new(big.Int).Rsh(exponent, 16) // aritmetik kaydırma (eksi sonsuza doğru)
	frac := new(big.Int).Sub(exponent, new(big.Int).Lsh(shifts, 16)).Uint64()

	// Ağın ömrü boyunca ulaşılamayacak kaydırmalar doğrudan sınıra kırpılır (bellek tavanı).
	if !shifts.IsInt64() || shifts.Int64() > 512 {
		return new(big.Int).Set(maxTarget)
	}
	if shifts.Int64() < -512 {
		return big.NewInt(1)
	}
	n := shifts.Int64()

	// 2^frac ≈ 1 + (195766423245049·f + 971821376·f² + 5127·f³ + 2^47) >> 48
	f := new(big.Int).SetUint64(frac)
	f2 := new(big.Int).Mul(f, f)
	f3 := new(big.Int).Mul(f2, f)
	poly := new(big.Int).Mul(big.NewInt(195766423245049), f)
	poly.Add(poly, new(big.Int).Mul(big.NewInt(971821376), f2))
	poly.Add(poly, new(big.Int).Mul(big.NewInt(5127), f3))
	poly.Add(poly, new(big.Int).Lsh(big.NewInt(1), 47))
	factor := poly.Rsh(poly, 48)
	factor.Add(factor, big.NewInt(65536))

	next := new(big.Int).Mul(anchorTarget, factor)
	if n < 0 {
		next.Rsh(next, uint(-n))
	} else {
		next.Lsh(next, uint(n))
	}
	next.Rsh(next, 16)
	if next.Sign() == 0 {
		return big.NewInt(1)
	}
	if next.Cmp(maxTarget) > 0 {
		return new(big.Int).Set(maxTarget)
	}
	return next
}

// nextBits ebeveyni (parentHeight, parentTime) olan bloğun taşıması gereken hedeftir;
// anchorTime ebeveynin zincirindeki 1. bloğun zamanıdır (parentHeight >= 1 ise anlamlı).
func (p *Params) nextBits(parentHeight, parentTime, anchorTime int64) uint32 {
	if p.NoRetarget || parentHeight < 1 {
		return p.Bits // regtest ya da 1. blok (çapa) başlangıç hedefini taşır
	}
	anchor, err := TargetFromBits(p.Bits)
	if err != nil {
		return p.Bits // Validate başlangıç hedefini denetler; buraya gelinmez
	}
	// Çapanın ideal zamanı kendi zamanıdır: ebeveynin ideal zamanı
	// anchorTime + spacing·(parentHeight−1). Belirtimin "çapanın ebeveyni" tanımına
	// heightDelta = parentHeight − 2 ile çevrilir (exponent aynı kalır).
	t := asertTarget(anchor, parentTime-anchorTime, parentHeight-2, p.TargetSpacing, p.HalfLife, powLimit)
	return bigToCompact(t)
}
