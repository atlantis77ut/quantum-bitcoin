package core

// Ödül fonksiyonunun taşmasız kalması için Validate'in uyguladığı üst sınırlar:
// maxSubsidy*maxSlowStart < 2^63 (rampadaki çarpım) ve ödülün kendisi makul düzeyde kalır.
const (
	maxSubsidy   int64 = 1_000 * Coin
	maxSlowStart int64 = 1_000_000
)

// BlockSubsidy verilen yükseklikteki bloğun basabileceği yeni para miktarıdır (ücretler
// hariç). Genesis (yükseklik 0) değer basmaz; negatif yükseklik ağdan gelebileceği için
// panik yerine 0 döner (blok zaten checkBody'de düşer). 1 ≤ h < SlowStartBlocks aralığında
// ödül doğrusal rampadır (InitialSubsidy*h/SlowStartBlocks); tam ödül h = SlowStartBlocks
// yüksekliğinde başlar. Sonrasında InitialSubsidy her
// HalvingInterval blokta yarılanır ve TailSubsidy'nin altına inmez (kuyruk emisyonu).
// Parametreler Validate'ten geçmiş olmalıdır.
func BlockSubsidy(height int64, p *Params) int64 {
	if height <= 0 {
		return 0
	}
	if height < p.SlowStartBlocks {
		return p.InitialSubsidy * height / p.SlowStartBlocks
	}
	halvings := height / p.HalvingInterval
	if halvings >= 63 {
		return p.TailSubsidy
	}
	if r := p.InitialSubsidy >> uint(halvings); r > p.TailSubsidy {
		return r
	}
	return p.TailSubsidy
}

// SupplyAt genesis'ten verilen yüksekliğe kadar (dahil) basılmış toplam ödüldür (ücretler
// el değiştirir, arzı artırmaz). Kuyruk başladıktan sonra arz doğrusal büyür; tavan yoktur.
// Taşma olacak kadar büyük yükseklikte false döner.
func SupplyAt(height int64, p *Params) (int64, bool) {
	var total int64
	if height <= 0 {
		return 0, true
	}
	// Rampa: 1..min(height, SlowStartBlocks-1), tek tek (en fazla maxSlowStart adım).
	h := int64(1)
	for ; h < p.SlowStartBlocks && h <= height; h++ {
		total += BlockSubsidy(h, p)
	}
	// Dönemler: [h, dönem sonu] aralığında ödül sabittir.
	for h <= height {
		end := (h/p.HalvingInterval+1)*p.HalvingInterval - 1
		if end > height || end < h { // end < h: çarpım taştı
			end = height
		}
		r := BlockSubsidy(h, p)
		if r == p.TailSubsidy {
			end = height // kuyrukta ödül artık değişmez
		}
		n := end - h + 1
		if n > (maxInt64-total)/r {
			return 0, false
		}
		total += n * r
		if end == height {
			break
		}
		h = end + 1
	}
	return total, true
}

const maxInt64 = int64(^uint64(0) >> 1)
