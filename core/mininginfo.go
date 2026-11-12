package core

import (
	"fmt"
	"math"
	"math/big"
)

// Kazım izleme özeti (salt okunur, uzlaşma kuralı değil): son N ana zincir bloğunun işi, medyan
// zamana dayalı süresi, bundan tahmini ağ hashrate'i, zorluk ve bu düğümün sakladığı yan blok
// sayısı. Hashrate ve süre MTP farkından hesaplanır (confirm.go ile aynı) ve son blokların çoğunu
// kazan biri tarafından iki yöne de oynatılabilir: damgaları FTL'ye dayayarak süreyi en çok FTL
// (300 sn) kadar şişirip tahmini düşürebilir ya da MTP+1'e dayayarak süreyi kısaltıp tahmini
// birkaç kat büyütebilir (bedeli ASERT'in zorluğu artırmasıdır). MTP pencereyi ~5 blok geriden
// izlediği için pencere MedianTimeSpan bloktan kısaysa hashrate verilmez. Yan blok sayısı bu
// düğümün o an sakladığı ana zincir dışı bloklardır, ağın yetim oranı değildir: geçersiz
// işaretlenenler, yükseklik/derinlik sınırına takılan ya da en az zincir işinin altında bırakılan
// bloklar ve eşlerin hiç iletmedikleri sayılmaz (eksik sayım); senkronizasyonda henüz ucu
// geçmemiş ağır dalın saklanan gövdeleri ana zincire geçene kadar yan blok sayılır (geçici fazla
// sayım). Selfish mining'i tek başına tespit etmez.

// Kazım özeti pencere sınırları (blok).
const (
	MiningInfoDefaultBlocks = 120
	MiningInfoMaxBlocks     = 10080
)

// MiningInfo bir anlık görüntüdeki kazım özetidir.
type MiningInfo struct {
	TipHeight int64
	TipHash   string
	TipBits   uint32 // ucun hedefi
	NextBits  uint32 // uç+1 için beklenen hedef (yalnızca ebeveyne bağlı)

	// Difficulty ağın en kolay hedefinin (PowLimitBits) sonraki hedefe oranıdır (taban = 1).
	Difficulty float64

	WindowBlocks int64    // pencere: (uç − WindowBlocks, uç] ana zincir blokları; genesis sayılmaz
	Work         *big.Int // penceredeki blokların toplam işi

	// ElapsedSeconds MTP(uç) − MTP(uç − WindowBlocks). HasRate false ise hashrate ve ortalama
	// süre anlamsızdır (pencere MedianTimeSpan'dan kısa ya da süre ≤ 0).
	ElapsedSeconds   int64
	HasRate          bool
	Hashrate         float64 // tahmini özet/sn = Work / ElapsedSeconds
	MeanBlockSeconds float64 // ElapsedSeconds / WindowBlocks

	// StaleBlocks penceredeki yüksekliklerde saklanan ana zincir dışı bloklardır (yaklaşık; yukarıya bakın).
	StaleBlocks int64

	TargetSpacing int64
	DeepReorg     bool
	BelowMinWork  bool
}

// MiningInfo son blocks ana zincir bloğunun kazım özetini döndürür; pencere uç yüksekliğiyle
// sınırlanır (uç genesis ise pencere boştur).
func (bc *Blockchain) MiningInfo(blocks int64) (MiningInfo, error) {
	if blocks < 1 || blocks > MiningInfoMaxBlocks {
		return MiningInfo{}, fmt.Errorf("pencere 1 ile %d arasında olmalı: %d", MiningInfoMaxBlocks, blocks)
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	tip := bc.tip
	info := MiningInfo{
		TipHeight:     tip.Height,
		TipHash:       tip.Hash,
		TipBits:       tip.Bits,
		Work:          new(big.Int),
		TargetSpacing: bc.params.TargetSpacing,
		DeepReorg:     bc.deepReorg != nil,
		BelowMinWork:  tip.work.Cmp(bc.params.minChainWork()) < 0,
	}
	next, err := bc.expectedBitsLocked(tip.Hash, nil)
	if err != nil {
		return MiningInfo{}, err
	}
	info.NextBits = next
	limit, err := TargetFromBits(bc.params.PowLimitBits)
	if err != nil {
		return MiningInfo{}, err
	}
	target, err := TargetFromBits(next)
	if err != nil {
		return MiningInfo{}, err
	}
	if target.Sign() > 0 {
		if d, _ := new(big.Rat).SetFrac(limit, target).Float64(); !math.IsInf(d, 0) && !math.IsNaN(d) {
			info.Difficulty = d
		}
	}

	n := blocks
	if n > tip.Height {
		n = tip.Height
	}
	info.WindowBlocks = n
	if n == 0 {
		return info, nil
	}
	start, ok := bc.index[bc.main[tip.Height-n]]
	if !ok || start.Height != tip.Height-n {
		return MiningInfo{}, fmt.Errorf("%w: %d yüksekliğinde ana zincir bloğu yok", ErrCorruptState, tip.Height-n)
	}
	info.Work.Sub(tip.work, start.work)
	for h := tip.Height - n + 1; h <= tip.Height; h++ {
		c := bc.perHeight[h]
		if c < 1 {
			return MiningInfo{}, fmt.Errorf("%w: %d yüksekliğinde saklanan blok sayısı %d", ErrCorruptState, h, c)
		}
		info.StaleBlocks += int64(c - 1)
	}
	tipMTP, err := bc.mtpLocked(tip.Hash, nil)
	if err != nil {
		return MiningInfo{}, err
	}
	startMTP, err := bc.mtpLocked(start.Hash, nil)
	if err != nil {
		return MiningInfo{}, err
	}
	info.ElapsedSeconds = tipMTP - startMTP
	if n >= MedianTimeSpan && info.ElapsedSeconds > 0 {
		r, _ := new(big.Rat).SetFrac(info.Work, big.NewInt(info.ElapsedSeconds)).Float64()
		if !math.IsInf(r, 0) && !math.IsNaN(r) {
			info.HasRate = true
			info.Hashrate = r
			info.MeanBlockSeconds = float64(info.ElapsedSeconds) / float64(n)
		}
	}
	return info, nil
}
