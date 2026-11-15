package core

import (
	"math"
	"math/big"
)

// Saldırganın yetişme olasılığı (Nakamoto, "Bitcoin: A Peer-to-Peer Electronic Cash System",
// §11). Hashrate payı q olan saldırgan, işlem bloğunun üzerine z blok eklendiği anda gizli bir
// dalla başlar; ilerlemesi beklenen değeri λ = z·q/p olan Poisson dağılımıdır ve geride kaldığı
// her blok farkını (q/p)^fark olasılıkla kapatır (kumarbazın yıkımı). Olasılık
//
//	P(q, z) = Σ_{k≤z} Poisson(k; λ)·(q/p)^(z−k) + Σ_{k>z} Poisson(k; λ)
//
// iki pozitif toplam olarak hesaplanır: makaledeki 1 − Σ… biçimi küçük P'de sayısal olarak
// iptal eder, e^(−λ) de λ > ~745'te sıfıra düşüp derin bloklara P = 1 verdirirdi. Bu yüzden
// Poisson olasılıkları log uzayında yürütülür.
//
// Model sabit hashrate payı, Poisson blok zamanları ve dürüst zincirde beklenen blok süresi
// varsayar; selfish mining, yetim bloklar (q'yu fiilen büyütür) ve zorluk değişimi yoktur.
// Uzlaşma kuralı değildir; yalnızca entegrasyonlara gösterge olarak verilir.

// attackDepthCap hesaplanan en büyük z'dir: P z'de azaldığından daha derin bloklar için
// sınırdaki değer bir üst sınırdır (temkinli) ve uç noktanın maliyeti sabit kalır.
const attackDepthCap = 10_000

// AttackerSuccess hashrate payı q olan saldırganın z blok geriden yetişme olasılığıdır. q ≥ 0,5
// ya da z ≤ 0 ise 1 döner; sonuç her zaman [0, 1] içindedir.
func AttackerSuccess(q float64, z int64) float64 {
	switch {
	case !(q < 0.5) || z <= 0: // NaN da buraya düşer
		return 1
	case q <= 0:
		return 0
	}
	z = min(z, attackDepthCap)
	p := 1 - q
	logR := math.Log(q / p)
	lam := float64(z) * q / p
	logLam := math.Log(lam)
	lp := -lam // log Poisson(0; λ)
	var sum float64
	for k := int64(0); k <= z; k++ {
		if k > 0 {
			lp += logLam - math.Log(float64(k))
		}
		sum += math.Exp(lp + float64(z-k)*logR)
	}
	// Kuyruk: k > z ≥ λ olduğundan terimler en az q/p oranıyla küçülür.
	for k := z + 1; ; k++ {
		lp += logLam - math.Log(float64(k))
		t := math.Exp(lp)
		sum += t
		if t <= sum*1e-17 || t < math.SmallestNonzeroFloat64 {
			break
		}
	}
	return min(sum, 1)
}

// AttackDepth Nakamoto modelinde kullanılacak z'dir: işlem bloğunun üzerindeki blok sayısı
// (Blocks − 1), iki sınırın küçüğüyle kısılır. Model dürüst blokların işlem bloğuyla aynı
// zorlukta ve beklenen sürede geldiğini varsayar; ASERT ise uzun bir duraklamadan sonra açığı
// taban zorluğunda blok seliyle kapatır ve sel z'yi şişirmemelidir:
//
//   - iş: üstteki işin işlem bloğunun kendi işi cinsinden blok sayısı (Work / BlockWork − 1).
//     Duraklamadan önceki bir işlemin üzerindeki sel az iş taşır, sayılmaz;
//   - süre: geçen sürenin karşılık geldiği blok sayısı, FTL (MaxFutureDrift) düşülerek: ucun
//     medyan zamanı bu kadar ileri itilebilir (confirm.go). İşlem bloğunun 11 bloğunun çoğunu
//     kazan biri onun medyan zamanını geride tutarak süreyi bundan fazla da şişirebilir.
func AttackDepth(c Confirmation, targetSpacing int64) int64 {
	if c.Blocks <= 0 || targetSpacing <= 0 || c.Work == nil || c.BlockWork == nil || c.BlockWork.Sign() <= 0 {
		return 0
	}
	byTime := max(0, (c.ElapsedSeconds-MaxFutureDrift)/targetSpacing)
	byWork := new(big.Int).Quo(c.Work, c.BlockWork)
	byWork.Sub(byWork, big.NewInt(1))
	z := min(c.Blocks-1, byTime)
	if byWork.Sign() < 0 {
		return 0
	}
	if byWork.IsInt64() {
		z = min(z, byWork.Int64())
	}
	return z
}

// TargetSpacing ağın hedef blok aralığıdır (saniye).
func (bc *Blockchain) TargetSpacing() int64 { return bc.params.TargetSpacing }
