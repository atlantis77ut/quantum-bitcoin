package core

import (
	"errors"
	"math/big"
)

// ErrBadBits compact hedef kodlaması geçersiz, kanonik değil veya PoW sınırının üzerinde.
var ErrBadBits = errors.New("geçersiz hedef (bits) kodlaması")

// powLimit izin verilen en kolay hedeftir (2^252 - 1, eski "1 hex sıfır" zorluğu).
var powLimit = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 252), big.NewInt(1))

// compactToBig Bitcoin tarzı compact hedefi (üs:8 bit | işaret:1 bit | mantis:23 bit)
// tamsayıya çevirir. Negatif, sıfır veya 256 biti aşan değerler geçersizdir.
func compactToBig(bits uint32) (*big.Int, bool) {
	exp := bits >> 24
	mant := bits & 0x007fffff
	if bits&0x00800000 != 0 || mant == 0 {
		return nil, false
	}
	var t *big.Int
	if exp <= 3 {
		t = big.NewInt(int64(mant >> (8 * (3 - exp))))
	} else {
		if exp > 35 { // 3 baytlık mantis 8*(exp-3) bit kaydırılınca 256 biti kesin aşar
			return nil, false
		}
		t = new(big.Int).Lsh(big.NewInt(int64(mant)), uint(8*(exp-3)))
	}
	if t.Sign() == 0 || t.BitLen() > 256 {
		return nil, false
	}
	return t, true
}

// bigToCompact pozitif bir hedefi kanonik compact biçime çevirir (alt baytlar kesilir).
func bigToCompact(t *big.Int) uint32 {
	if t.Sign() <= 0 {
		return 0
	}
	size := uint32((t.BitLen() + 7) / 8)
	var mant uint32
	if size <= 3 {
		mant = uint32(t.Uint64()) << (8 * (3 - size))
	} else {
		mant = uint32(new(big.Int).Rsh(t, uint(8*(size-3))).Uint64())
	}
	if mant&0x00800000 != 0 { // işaret biti kullanılamaz: mantisi bir bayt kaydır
		mant >>= 8
		size++
	}
	return size<<24 | mant
}

// TargetFromBits uzlaşma kurallarına göre compact hedefi çözer: kanonik olmalı,
// sıfırdan büyük ve powLimit'ten küçük ya da eşit olmalı.
func TargetFromBits(bits uint32) (*big.Int, error) {
	t, ok := compactToBig(bits)
	if !ok || bigToCompact(t) != bits || t.Cmp(powLimit) > 0 {
		return nil, ErrBadBits
	}
	return t, nil
}

// DifficultyToBits eski "baştaki hex sıfır sayısı" zorluğunu (d) karşılık gelen
// compact hedefe çevirir: hedef ≈ 2^(256-4d) - 1 (compact kesmesi nedeniyle biraz daha zor).
func DifficultyToBits(d int) (uint32, error) {
	if d < 1 || d > 60 {
		return 0, ErrBadBits
	}
	t := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(256-4*d)), big.NewInt(1))
	bits := bigToCompact(t)
	if _, err := TargetFromBits(bits); err != nil {
		return 0, err
	}
	return bits, nil
}

// BitsForHashrate saniyede hashesPerSec özetle ortalama spacing saniyede bir blok veren
// hedeftir: hedef ≈ 2^256 / (hashesPerSec·spacing). Mainnet başlangıç hedefi lansmanda bununla,
// başlatıcının genesis ile kanıtladığı hashrate'ten seçilir. Sonuç limit'e kırpılır.
func BitsForHashrate(hashesPerSec float64, spacing int64, limitBits uint32) (uint32, error) {
	limit, err := TargetFromBits(limitBits)
	if err != nil {
		return 0, err
	}
	if !(hashesPerSec >= 1 && hashesPerSec <= 1e40) || spacing <= 0 { // NaN ve sonsuz da reddedilir
		return 0, ErrBadBits
	}
	work, _ := new(big.Float).Mul(big.NewFloat(hashesPerSec), big.NewFloat(float64(spacing))).Int(nil)
	if work.Sign() <= 0 {
		return 0, ErrBadBits
	}
	t := new(big.Int).Div(new(big.Int).Lsh(big.NewInt(1), 256), work)
	if t.Cmp(limit) > 0 {
		t = limit
	}
	if t.Sign() <= 0 {
		t = big.NewInt(1)
	}
	bits := bigToCompact(t)
	if _, err := TargetFromBits(bits); err != nil {
		return 0, err
	}
	return bits, nil
}

// LaunchTargets mainnet lansman kontrol listesindeki (madde 6a) başlangıç hedefini ve tabanı
// ölçülmüş hashrate'ten önerir: başlangıç = BitsForHashrate(h, spacing) mainnet tavanına
// (0x1d00ffff ≈ 2^224) kırpılmış, taban hedef cinsinden min(başlangıç · 2^16, tavan). Yalnızca
// öneridir; uzlaşma bunu çağırmaz (Validate yalnızca tavanı ve başlangıç ≤ taban sırasını denetler).
func LaunchTargets(hashesPerSec float64, spacing int64) (startBits, floorBits uint32, err error) {
	startBits, err = BitsForHashrate(hashesPerSec, spacing, mainnetMaxPowLimitBits)
	if err != nil {
		return 0, 0, err
	}
	start, err := TargetFromBits(startBits)
	if err != nil {
		return 0, 0, err
	}
	ceiling, err := TargetFromBits(mainnetMaxPowLimitBits)
	if err != nil {
		return 0, 0, err
	}
	floor := new(big.Int).Lsh(start, 16)
	if floor.Cmp(ceiling) > 0 {
		floor = ceiling
	}
	floorBits = bigToCompact(floor)
	if _, err := TargetFromBits(floorBits); err != nil {
		return 0, 0, err
	}
	return startBits, floorBits, nil
}

// hashMeetsTarget özeti büyük-endian 256 bitlik tamsayı olarak okur; kural: özet <= hedef.
func hashMeetsTarget(h [32]byte, target *big.Int) bool {
	return new(big.Int).SetBytes(h[:]).Cmp(target) <= 0
}

// MeetsTarget hex özetin (kanonik, 64 küçük harf) verilen compact hedefi karşılayıp karşılamadığını söyler.
func MeetsTarget(hashHex string, bits uint32) bool {
	h, ok := decodeHash(hashHex)
	if !ok {
		return false
	}
	t, err := TargetFromBits(bits)
	if err != nil {
		return false
	}
	return hashMeetsTarget(h, t)
}
