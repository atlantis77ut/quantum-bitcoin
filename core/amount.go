package core

import "fmt"

// AmountDecimals, QBTC'nin ondalık basamak sayısıdır (Coin = 10^AmountDecimals).
const AmountDecimals = 8

// FormatAmount taban birim cinsinden tutarı "12.34000000" biçiminde QBTC'ye çevirir.
// Kayan nokta kullanılmaz; math.MinInt64 dahil tüm int64 değerleri doğru biçimlenir.
func FormatAmount(v int64) string {
	sign := ""
	u := uint64(v)
	if v < 0 {
		sign = "-"
		u = -u // ikiye tümleyen: MinInt64 için de doğru mutlak değeri verir
	}
	return fmt.Sprintf("%s%d.%0*d", sign, u/uint64(Coin), AmountDecimals, u%uint64(Coin))
}
