package core

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Kalıcı eksik veri alarmı (budama hazırlığı, davranışı değiştirmez). Her indeks kaydı gövdesiyle
// aynı batch'te yazılır (storeAndActivateLocked, genesis); ana zincirdeki her bloğun undo ve
// indeks geri alma kaydı zincir güncellemesiyle yazılır. Zincire geçerken (activateLocked)
// bağlanacak bir bloğun gövdesi ya da geri sarılacak bir bloğun undo veya indeks geri alma kaydı
// diskte yoksa düğüm daha çok işli dala geçemez ve hafif zincirde kalır (ebeveyn ve medyan zaman
// indeksten okunur, geri sarılan bloğun gövdesi okunmaz); bugün bu yalnızca disk arızası ya da elle silinmiş veridir, ileride budanmış düğümün
// budama derinliğinden derin reorg'udur. Bu durum eskiden geçici bir hata olarak yalnızca
// loglanırdı; şimdi derin reorg alarmı gibi diske yazılır, yeniden başlatma silmez, onaylara
// güvenilmez ve yalnızca operatör -clear-data-alarm ile temizler.
//
// Alarm geçişi değiştirmez: hata eskisi gibi döner, hiçbir blok geçersiz işaretlenmez, uç
// değişmez. Veri geri gelirse (ör. yedekten) aynı blok yeniden gelince geçiş olur, alarm kalır.
const keyDataAlarm = "missing_data_alarm"

// ErrBlockDataMissing indeksteki bir bloğun gövdesinin ya da undo kaydının diskte olmadığını
// söyler. Kalıcı geçersizlik değildir (blok geçersiz işaretlenmez).
var ErrBlockDataMissing = errors.New("blok verisi diskte yok")

// Eksik verinin türü (MissingDataRecord.Kind).
const (
	dataBody      = "body"
	dataUndo      = "undo"
	dataIndexUndo = "index_undo" // indeks geri alma kaydı (txindex.go)
)

// missingDataError eksik verinin hangi bloğa ait olduğunu taşır. Eski sınıflandırma korunur:
// gövde hatası leveldb.ErrNotFound'u, undo hatası ErrCorruptState'i de sarar.
type missingDataError struct {
	hash, kind string
	cause      error
}

func (e *missingDataError) Error() string {
	return fmt.Sprintf("%v: %s (%s)", ErrBlockDataMissing, e.kind, e.hash)
}

func (e *missingDataError) Unwrap() []error { return []error{ErrBlockDataMissing, e.cause} }

// MissingDataRecord kalıcı alarmın kaydıdır; son olayı ve temizlenmeden bu yana olay sayısını
// tutar.
type MissingDataRecord struct {
	Hash   string `json:"hash"`   // verisi okunamayan blok
	Kind   string `json:"kind"`   // "body", "undo" ya da "index_undo"
	Tip    string `json:"tip"`    // olay anındaki uç
	Target string `json:"target"` // geçilemeyen dalın ucu
	Time   int64  `json:"time"`   // yerel saat, Unix saniye
	Count  int64  `json:"count"`  // temizlenmeden bu yana olay sayısı
}

// dataAlarmLocked activateLocked'ın eksik veri hatasını kalıcı alarma yazar. Aynı eksik veri
// yeniden çarparsa (ör. daha çok işli dalın yeni blokları ya da yeniden gönderilen bloklar,
// hedef değişse de) diske yazmaz ve yeniden loglamaz: eş, PoW'suz yeniden gönderimle fsync ve
// log satırı harcatamaz. Yazma kilidi altında çağrılır; zincir batch'i bu yolda yazılmadığından
// alarm kendi eşzamanlı yazımıyla gider. Yazım başarısız olsa da (alarmın hedeflediği disk
// arızası) alarm bellekte etkinleşir: onaylara güvenilmez, yalnızca yeniden başlatmada kaybolur.
func (bc *Blockchain) dataAlarmLocked(md *missingDataError, target *indexEntry) {
	if a := bc.dataAlarm; a != nil && a.Hash == md.hash && a.Kind == md.kind {
		return
	}
	rec := &MissingDataRecord{Hash: md.hash, Kind: md.kind, Tip: bc.tip.Hash, Target: target.Hash,
		Time: bc.now(), Count: 1}
	if bc.dataAlarm != nil {
		rec.Count = bc.dataAlarm.Count + 1
	}
	data, err := json.Marshal(rec)
	if err == nil {
		err = bc.db.Put([]byte(keyDataAlarm), data, &opt.WriteOptions{Sync: true})
	}
	if err != nil {
		bc.log.Error("eksik veri alarmı diske yazılamadı (yalnızca bellekte etkin)", "err", err)
	}
	bc.dataAlarm = rec
	bc.log.Error("EKSİK VERİ ALARMI: indeksteki bir bloğun verisi diskte yok, daha çok işli zincire geçilemiyor; onaylara güvenmeyin, diski ve yedeği denetleyin",
		"blok", md.hash, "tur", md.kind, "uc", bc.tip.Hash, "hedef", target.Hash, "olay", rec.Count)
}

// loadDataAlarm açılışta kalıcı alarmı okur (açılıştaki kurtarmadan önce).
func (bc *Blockchain) loadDataAlarm() error {
	data, err := bc.db.Get([]byte(keyDataAlarm), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("eksik veri alarmı okunamadı: %w", err)
	}
	var rec MissingDataRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("%w: eksik veri alarmı: %v", ErrCorruptState, err)
	}
	bc.dataAlarm = &rec
	bc.log.Error("EKSİK VERİ ALARMI etkin (önceki çalışmadan): onaylara güvenmeyin; olay incelendikten sonra -clear-data-alarm ile temizleyin",
		"blok", rec.Hash, "tur", rec.Kind, "uc", rec.Tip, "hedef", rec.Target, "zaman", rec.Time, "olay", rec.Count)
	return nil
}

// MissingDataAlarm kalıcı eksik veri alarmı etkinse true döner (yeniden başlatma silmez).
func (bc *Blockchain) MissingDataAlarm() bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.dataAlarm != nil
}

// ClearMissingDataAlarm kalıcı alarmı temizler ve temizlenen kaydı döndürür (alarm yoksa nil).
// Yalnızca operatör eylemiyle (-clear-data-alarm) çağrılır.
func (bc *Blockchain) ClearMissingDataAlarm() (*MissingDataRecord, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	rec := bc.dataAlarm
	if rec == nil {
		return nil, nil
	}
	if err := bc.db.Delete([]byte(keyDataAlarm), &opt.WriteOptions{Sync: true}); err != nil {
		return nil, fmt.Errorf("eksik veri alarmı silinemedi: %w", err)
	}
	bc.dataAlarm = nil
	bc.log.Warn("eksik veri alarmı operatörce temizlendi", "blok", rec.Hash, "tur", rec.Kind,
		"uc", rec.Tip, "hedef", rec.Target, "zaman", rec.Time, "olay", rec.Count)
	return rec, nil
}
