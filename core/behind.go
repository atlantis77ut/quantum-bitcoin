package core

import (
	"errors"
	"math"
	"math/big"
)

// Geride kalma kaydı ve kazım duraklaması ("takılı düğüm"; kullanıcı kararları 2026-11-17,
// devils-advocate sonrası). Uzlaşma kuralı değil, düğüm politikasıdır.
//
// Zincire geçiş (activateLocked) uçtan ağır ve geçersiz işaretlenmemiş bir hedefe ulaşamadan
// dönerse düğüm bildiği en çok işli zincirde değildir: hedef kaydedilir. Neden ayırt edilmez
// (eksik ya da bozuk kayıt, indeks tutmazlığı, okuma ya da yazma hatası, saatin geri gitmesi):
// kayıt bir teşhis değil olgudur ve hata türlerinin doğru sınıflanmasına bağlı kalmaz. Kalıcı
// kural ihlalinde hedef geçersiz işaretlendiğinden kayıt oluşmaz. Kayıt yalnızca bellektedir;
// açılıştaki kurtarma denemeleri (recoverBestLocked) onu yeniden kurar.
//
// Kayıt, hedef ana zincire girince, geçersiz işaretlenince ya da uç ondan kesin olarak daha çok
// iş taşıyınca silinir. Eşit işte silinmez (ilk görülen kalır kuralı gereği düğüm kendi dalında
// durur; öteki dalın sonraki bloğu kaydı yeniden öne geçirir, log satırı yinelenmez), ama düğüm
// geride sayılmaz: BehindBlocks 0'dır.
//
// İki sonucu vardır:
//
//   - Gösterge: /mininginfo ve /confirmations'taki behind_blocks (kaç blokluk iş geride). Sıfır
//     değilse onaylara güvenilmez: düğüm bayat zincirde olabilir.
//   - Kazım duraklaması: açık Params.ReorgAlarmDepth blokluk işe (~2 saat) ulaşınca kazım durur
//     (MiningPaused): ağ o dalda saatlerdir ilerliyor, düğüm bayat ucun üstüne boşa kazıyordur.
//
// Eşik bilinçli olarak büyüktür. Bütün düğümleri aynı blokta düşüren bir bağlama hatası (ör.
// diske yazılan gövdenin geri okunamaması) çıkarsa o bloğu kimse bağlayamaz; küçük bir eşik,
// bloğun üstüne birkaç blok kazan herkese bütün dürüst madencileri durdurma anahtarı verirdi.
// ~2 saatlik iş farkını ancak hashrate çoğunluğu açabilir; azınlık bunu başaramaz, madenciler
// kardeş blok kazarak zinciri sürdürür. Bedeli, takılı madencinin durmadan önce saatlerce bayat
// zincire kazmasıdır. İkinci bedel: o sistemik senaryoda hashrate çoğunluğunu kısa süre (1–2
// saat) tutan biri geçilemeyen dalı eşik kadar öne geçirip kazmayı bırakırsa bütün dürüst
// madenciler aynı anda durur. Açığı kapatacak bloğu kazan kalmadığından duraklama kendiliğinden
// bitmez, yeniden başlatma da kaydı yeniden kurar; ağ düzeltme sürümüne kadar durur (eskiden
// kardeş zincir sürerdi). Eşiğe ancak art arda onlarca başarısız denemeyle (her yeni blok bir
// denemedir) ulaşıldığından geçici bir hata kazımı duraklatmaz; zamanlayıcıyla yeniden deneme
// gerekmez.

// ErrMiningPaused düğümün, bildiği daha çok işli zincirin kazım duraklatma eşiği kadar gerisinde
// kaldığını ve ona geçemediğini söyler (Mine).
var ErrMiningPaused = errors.New("kazım duraklatıldı: düğüm bildiği daha çok işli zincirin gerisinde ve ona geçemiyor")

// miningPauseLead kazımı duraklatan açıktır (blok cinsinden iş): derin reorg alarmıyla aynı
// ölçü, TargetSpacing'den türetilir (~2 saat).
func (p *Params) miningPauseLead() int64 { return p.ReorgAlarmDepth }

// IsLocalDataErr hatanın yerel veri hatası olduğunu söyler: indeksteki bir bloğun kaydı diskte
// yok ya da yerel durum bozuk. Eşin kusuru değildir; ağ katmanı eşi bunun için ertelemez.
// Sarılmamış LevelDB okuma ve yazma hataları (disk dolu dahil) bu sınıfta değildir (ayırt
// edilemiyor): ön-senkronizasyonda eşin başarısızlığı sayılırlar.
func IsLocalDataErr(err error) bool {
	return errors.Is(err, ErrBlockDataMissing) || errors.Is(err, ErrCorruptState)
}

// noteBehindLocked activateLocked'ın sonunda geride kalma kaydını günceller: err denemenin
// sonucudur. Yazma kilidi altında ya da tek iş parçacıklı açılışta çağrılır.
func (bc *Blockchain) noteBehindLocked(target *indexEntry, err error) {
	prev := bc.behind
	bc.behind = ""
	if e, ok := bc.index[prev]; ok && e.Status != statusInvalid && !bc.isMain(e) && bc.tip.work.Cmp(e.work) <= 0 {
		bc.behind = prev // hâlâ geçilmemiş ve uçtan hafif değil
	}
	if err != nil && target.Status != statusInvalid && target.work.Cmp(bc.tip.work) > 0 {
		if cur, ok := bc.index[bc.behind]; !ok || target.work.Cmp(cur.work) > 0 {
			bc.behind = target.Hash
		}
	}
	switch {
	case prev == "" && bc.behind != "":
		// Geçiş başına tek satır: kayıt dururken yeni hedefler ve yeniden gönderimler loglanmaz.
		bc.log.Error("GERİDE: düğüm bildiği daha çok işli zincire geçemiyor; onaylara güvenmeyin, nedeni giderin (eksik ya da bozuk veri, disk, saat). Açık kazım duraklatma eşiğine ulaşırsa kazım durur",
			"hedef", target.Hash, "hedef_yukseklik", target.Height, "uc", bc.tip.Hash, "uc_yukseklik", bc.tip.Height,
			"geride_blok", bc.behindLeadLocked(), "esik", bc.params.miningPauseLead(), "neden", err)
	case prev != "" && bc.behind == "":
		bc.log.Info("düğüm bildiği en çok işli zincirde; geride kalma kaydı silindi", "uc", bc.tip.Hash, "uc_yukseklik", bc.tip.Height)
	}
}

// behindLeadLocked kayıtlı hedefin uçtan kaç blokluk iş önde olduğunu döndürür; düğüm geride
// değilse 0. Birim ucun kendi blok işidir: geçilemeyen dalın zorluğu ölçüyü değiştiremez. İş
// farkı en yakın tam bloğa yuvarlanır (blok başına zorluk ASERT ile az da olsa değişir); hedef
// uçtan ağırsa sonuç en az 1'dir.
func (bc *Blockchain) behindLeadLocked() int64 {
	if bc.behind == "" {
		return 0
	}
	e, ok := bc.index[bc.behind]
	if !ok || e.Status == statusInvalid || e.work.Cmp(bc.tip.work) <= 0 {
		return 0
	}
	unit, err := blockWork(bc.tip.Bits)
	if err != nil || unit.Sign() <= 0 {
		return 1
	}
	// round(fark / birim) = (2·fark + birim) / (2·birim)
	lead := new(big.Int).Sub(e.work, bc.tip.work)
	lead.Lsh(lead, 1).Add(lead, unit).Div(lead, new(big.Int).Lsh(unit, 1))
	switch {
	case !lead.IsInt64():
		return math.MaxInt64
	case lead.Sign() <= 0:
		return 1
	}
	return lead.Int64()
}

// BehindBlocks düğümün, bildiği ama geçemediği daha çok işli zincirin kaç blokluk iş gerisinde
// olduğunu döndürür (0: geride değil). Sıfır değilse onaylara güvenilmez.
func (bc *Blockchain) BehindBlocks() int64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.behindLeadLocked()
}

// MiningPaused geride kalma açığı kazım duraklatma eşiğine ulaştıysa true döner: kazım yapılmaz.
func (bc *Blockchain) MiningPaused() bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.miningPausedLocked()
}

func (bc *Blockchain) miningPausedLocked() bool {
	return bc.behind != "" && bc.behindLeadLocked() >= bc.params.miningPauseLead()
}
