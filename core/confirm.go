package core

import "math/big"

// İş ve zamana dayalı onay göstergesi (CLAUDE.md mainnet engeli). Blok sayısı tek başına
// kesinlik ölçüsü değildir: mutlak ASERT'te uzun bir duraklamadan sonra biriken açık, taban
// zorluğunda saniyeler içinde kazılan bir blok seli olarak boşalır ve "100 onay" anlamsızlaşır.
// Bu yüzden onaylar üç ölçüyle birlikte bildirilir:
//
//   - Work: bloğun ve üzerine eklenen tüm blokların toplam işi (onu geri almak için yeniden
//     yapılması gereken iş). Taban zorluğundaki sel neredeyse iş eklemez.
//   - ElapsedSeconds: ucun medyan zamanı ile bloğun medyan zamanı arasındaki fark. Saniyeler
//     içinde kazılan bir sel onu ancak gerçek saat kadar ilerletir; son 11 bloğun çoğunu
//     kazan biri damgaları FTL'ye dayayarak en çok FTL (300 sn) kadar şişirebilir, eşikler
//     bu payı hesaba katmalıdır.
//   - Blocks: klasik onay sayısı.
//
// Entegrasyonlar (borsa vb.) üçünü birden eşik olarak kullanmalı; ayrıca derin reorg alarmı
// ya da en az zincir işinin altındaki uç onayların hiçbirine güvenilmemesi demektir.
// Uzlaşma kuralı değildir.

// Confirmation bir bloğun ana zincirdeki onay bilgisidir.
type Confirmation struct {
	Blocks         int64    // uç = 1; ana zincirde değilse 0
	Work           *big.Int // bloğun ve torunlarının toplam işi (ana zincirde değilse 0)
	ElapsedSeconds int64    // MTP(uç) − MTP(blok); ana zincirde değilse 0
	DeepReorg      bool     // derin reorg alarmı: onaylara güvenilmez
	BelowMinWork   bool     // uç en az zincir işinin altında: düğüm senkronize değil
}

// ConfirmationOf bir bloğun iş ve zamana dayalı onay bilgisini döndürür.
func (bc *Blockchain) ConfirmationOf(hash string) (Confirmation, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	c := Confirmation{
		Work:         new(big.Int),
		DeepReorg:    bc.deepReorg,
		BelowMinWork: bc.tip.work.Cmp(bc.params.minChainWork()) < 0,
	}
	e, ok := bc.index[hash]
	if !ok || !bc.isMain(e) {
		return c, nil
	}
	c.Blocks = bc.tip.Height - e.Height + 1
	parentWork := new(big.Int)
	if e.Height > 0 {
		p, ok := bc.index[e.Prev]
		if !ok {
			return c, ErrCorruptState
		}
		parentWork = p.work
	}
	c.Work.Sub(bc.tip.work, parentWork)
	tipMTP, err := bc.mtpLocked(bc.tip.Hash, nil)
	if err != nil {
		return c, err
	}
	blockMTP, err := bc.mtpLocked(e.Hash, nil)
	if err != nil {
		return c, err
	}
	c.ElapsedSeconds = tipMTP - blockMTP
	return c, nil
}

// WorkInBlocks işi, ağın başlangıç hedefinde (Params.Bits) kazılmış blok sayısı cinsinden
// ifade eder: insan okuması için sabit bir ölçek (taban zorluğundaki sel çok küçük çıkar).
func (bc *Blockchain) WorkInBlocks(w *big.Int) float64 {
	ref, err := blockWork(bc.params.Bits)
	if err != nil || ref.Sign() == 0 || w == nil {
		return 0
	}
	f, _ := new(big.Rat).SetFrac(w, ref).Float64()
	return f
}
