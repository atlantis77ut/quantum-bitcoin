package core

import "math/big"

// En az zincir işi (MinChainWork; kullanıcı kararı 2026-11-09). Uzlaşma kuralı değil, düğüm
// politikasıdır: sürüme gömülü bu değerin altında birikmiş işi olan bir zincire düğüm
// senkronize olmaz, ağdan kendiliğinden duyurulan blokları kabul etmez ve kazım yapmaz.
// Böylece yeni açılmış bir düğüme (ucu genesis) taban zorluğunda ucuzca kazılmış sahte bir
// zincir yutturulamaz. Değer yüksek seçilirse yalnızca canlılık bozulur (düğüm güncelleme ya
// da gerçek zinciri bekler); geçersiz bir zincir hiçbir koşulda kabul ettirilmez.
//
// Sürüm politikası: her sürümde ana zincirin 1–2 hafta önceki bir bloğunun birikmiş işi
// yazılır ve değer bağımsız bir ikinci kişi tarafından kendi düğümünden doğrulanır.
// Lansmanda ve henüz değer belirlenmemiş ağlarda 0'dır (kapı kapalı).
//
// Ön-senkronizasyon (presync.go) sayesinde eşiğin oturum penceresinin (MaxSyncHeaders)
// ötesinde olması senkronizasyonu kilitlemez: başlıklar sabit bellekte sayılır, eşik
// geçilince parça parça yeniden indirilir.
//
// Kapılar: senkronizasyonda gövde indirme (HeaderSync.MoreWork/Missing) ve ağdan duyurulan
// bloklar (RelayBelowMinWork). AcceptBlock'ta kapı YOKTUR: senkronizasyonla indirilen gerçek
// zincirin ilk blokları tek tek eşiğin altındadır; onları başlık zincirinin toplam işi
// yetkilendirir. Kendi kazdığımız bloklar da kapıdan geçmez; madenci eşiğin altında bekler.

// minChainWork ağın en az zincir işidir (nil ise 0).
func (p *Params) minChainWork() *big.Int {
	if p.MinChainWork == nil {
		return new(big.Int)
	}
	return p.MinChainWork
}

// enoughWorkLocked w işli bir başlık zincirinin gövdelerinin indirilmeye değer olup
// olmadığını söyler: ucun işini geçmeli ve en az zincir işine ulaşmalı. Kilit altında çağrılır.
func (bc *Blockchain) enoughWorkLocked(w *big.Int) bool {
	return w != nil && w.Cmp(bc.tip.work) > 0 && w.Cmp(bc.params.minChainWork()) >= 0
}

// BelowMinChainWork ucun birikmiş işi en az zincir işinin altındaysa true döner: düğüm henüz
// senkronize değildir; onaylar güvenilmez ve kazım yapılmaz.
func (bc *Blockchain) BelowMinChainWork() bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.tip.work.Cmp(bc.params.minChainWork()) < 0
}

// RelayBelowMinWork ağdan duyurulan bloğun zinciri en az zincir işine ulaşmıyorsa true
// döner; böyle blok doğrulanmadan bırakılır. Ebeveyni bilinmeyen blok için false döner
// (yetim yolu senkronizasyonu tetikler, senkronizasyon kendi kapısından geçer); hedefi
// geçersiz blok için de false döner (AcceptBlock reddeder).
func (bc *Blockchain) RelayBelowMinWork(b *Block) bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	minWork := bc.params.minChainWork()
	if minWork.Sign() == 0 {
		return false
	}
	parent, ok := bc.index[b.PrevHash]
	if !ok {
		return false
	}
	w, err := blockWork(b.Bits)
	if err != nil {
		return false
	}
	return new(big.Int).Add(parent.work, w).Cmp(minWork) < 0
}
