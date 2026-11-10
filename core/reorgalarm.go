package core

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Kalıcı derin reorg alarmı (kullanıcı kararı 2026-11-11). Params.ReorgAlarmDepth'ten derin
// bir reorg, zincir güncellemesiyle aynı LevelDB batch'inde keyReorgAlarm'a yazılır; yeniden
// başlatma alarmı silmez. Alarmı yalnızca operatör, olayı inceledikten sonra
// -clear-reorg-alarm ile temizler; temizleme kayıtla birlikte loglanır.
//
// Alarm tek düğümün gözlemidir: reorg sırasında kapalı olan ya da sonradan sıfırdan eşitlenen
// düğüm eski dalı hiç geri sarmadığından alarm vermez. Eşik olay başınadır; eşiğin altındaki
// art arda reorg'lar alarm vermez (birikimli pencere yok).
const keyReorgAlarm = "reorg_alarm"

// DeepReorgRecord kalıcı alarmın kaydıdır; son derin reorg'u ve temizlenmeden bu yana olay
// sayısını tutar.
type DeepReorgRecord struct {
	Depth      int64  `json:"depth"`       // geri sarılan blok sayısı
	ForkHeight int64  `json:"fork_height"` // ortak atanın yüksekliği
	OldTip     string `json:"old_tip"`
	NewTip     string `json:"new_tip"`
	Time       int64  `json:"time"`  // yerel saat, Unix saniye
	Count      int64  `json:"count"` // temizlenmeden bu yana derin reorg sayısı
}

// reorgAlarmLocked derinlik eşiği aşıyorsa yeni alarm kaydını batch'e yazar ve döndürür;
// bellekteki alarm, batch diske yazıldıktan sonra çağıran tarafından güncellenir.
func (bc *Blockchain) reorgAlarmLocked(batch *leveldb.Batch, depth int64, fork, newTip *indexEntry) (*DeepReorgRecord, error) {
	if depth <= bc.params.ReorgAlarmDepth {
		return nil, nil
	}
	rec := &DeepReorgRecord{Depth: depth, ForkHeight: fork.Height, OldTip: bc.tip.Hash, NewTip: newTip.Hash,
		Time: bc.now(), Count: 1}
	if bc.deepReorg != nil {
		rec.Count = bc.deepReorg.Count + 1
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	batch.Put([]byte(keyReorgAlarm), data)
	return rec, nil
}

// loadReorgAlarm açılışta kalıcı alarmı okur.
func (bc *Blockchain) loadReorgAlarm() error {
	data, err := bc.db.Get([]byte(keyReorgAlarm), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reorg alarmı okunamadı: %w", err)
	}
	var rec DeepReorgRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("%w: reorg alarmı: %v", ErrCorruptState, err)
	}
	bc.deepReorg = &rec
	bc.log.Error("DERİN REORG ALARMI etkin (önceki çalışmadan): onaylara güvenmeyin; olay incelendikten sonra -clear-reorg-alarm ile temizleyin",
		"derinlik", rec.Depth, "catal", rec.ForkHeight, "eski_uc", rec.OldTip, "yeni_uc", rec.NewTip,
		"zaman", rec.Time, "olay", rec.Count)
	return nil
}

// ClearDeepReorgAlarm kalıcı alarmı temizler ve temizlenen kaydı döndürür (alarm yoksa nil).
// Yalnızca operatör eylemiyle (-clear-reorg-alarm) çağrılır.
func (bc *Blockchain) ClearDeepReorgAlarm() (*DeepReorgRecord, error) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	rec := bc.deepReorg
	if rec == nil {
		return nil, nil
	}
	if err := bc.db.Delete([]byte(keyReorgAlarm), &opt.WriteOptions{Sync: true}); err != nil {
		return nil, fmt.Errorf("reorg alarmı silinemedi: %w", err)
	}
	bc.deepReorg = nil
	bc.log.Warn("derin reorg alarmı operatörce temizlendi", "derinlik", rec.Depth, "catal", rec.ForkHeight,
		"eski_uc", rec.OldTip, "yeni_uc", rec.NewTip, "zaman", rec.Time, "olay", rec.Count)
	return rec, nil
}
