package core

import (
	"encoding/json"
	"errors"
	"testing"
)

// Zincire geçerken indeksteki bir bloğun gövdesi ya da undo kaydı diskte yoksa geçiş eskisi gibi
// olmaz (uç değişmez, hiçbir blok geçersiz işaretlenmez, ret önbelleğine girmez) ve kalıcı eksik
// veri alarmı verilir. Aynı olay yeniden yazılmaz; veri geri gelince geçiş olur, alarm kalır,
// yeniden açılışta sürer ve yalnızca ClearMissingDataAlarm ile temizlenir.
func TestMissingDataAlarm(t *testing.T) {
	// Ana zincir g-m1-m2, daha çok işli dal fork-s1-s2-s3. Eksik anahtar her okuma yolunu ayrı
	// sınar: dal gövdesi, çatal ebeveyni, medyan zamanın derin atası, geri sarılan bloğun
	// gövdesi (işlem indeksi silinirken) ve undo kaydı.
	for _, c := range []struct {
		name   string
		forkM1 bool // dal m1'den ayrılır (yoksa genesis'ten)
		key    func(g, m1, m2, s1 *Block) (hash, kind string)
	}{
		{"dal gövdesi", false, func(g, m1, m2, s1 *Block) (string, string) { return s1.Hash, dataBody }},
		{"çatal ebeveyni", false, func(g, m1, m2, s1 *Block) (string, string) { return g.Hash, dataBody }},
		{"medyan zaman atası", true, func(g, m1, m2, s1 *Block) (string, string) { return g.Hash, dataBody }},
		{"geri sarılan gövde", false, func(g, m1, m2, s1 *Block) (string, string) { return m1.Hash, dataBody }},
		{"geri sarılan undo", false, func(g, m1, m2, s1 *Block) (string, string) { return m2.Hash, dataUndo }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bc := openTestChain(t, dir)
			g := genesisOf(t, bc)
			main := extend(t, bc, g, 2, newKey(t).Address)
			mustAccept(t, bc, main...)
			fork, n := g, 3
			if c.forkM1 {
				fork, n = main[0], 2
			}
			side := extend(t, bc, fork, n, newKey(t).Address)
			last := side[len(side)-1]
			mustAccept(t, bc, side[:len(side)-1]...) // eşit iş: uç değişmez
			if tipHash(t, bc) != main[1].Hash {
				t.Fatal("eşit işli dal ucu değiştirdi")
			}

			hash, kind := c.key(g, main[0], main[1], side[0])
			key := hash
			if kind == dataUndo {
				key = prefixUndo + hash
			}
			saved, err := bc.db.Get([]byte(key), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := bc.db.Delete([]byte(key), nil); err != nil {
				t.Fatal(err)
			}

			err = bc.AcceptBlock(last)
			if !errors.Is(err, ErrBlockDataMissing) {
				t.Fatalf("hata %v, beklenen eksik veri", err)
			}
			if tipHash(t, bc) != main[1].Hash {
				t.Fatal("eksik veriyle uç değişti")
			}
			for _, e := range bc.index {
				if e.Status == statusInvalid || bc.rejected.has(e.Hash) {
					t.Fatalf("blok %d geçersiz işaretlendi", e.Height)
				}
			}
			rec := bc.dataAlarm
			if rec == nil || rec.Hash != hash || rec.Kind != kind || rec.Target != last.Hash ||
				rec.Tip != main[1].Hash || rec.Count != 1 || !bc.MissingDataAlarm() {
				t.Fatalf("alarm kaydı %+v", rec)
			}

			// Aynı olay yeniden yazılmaz (sayı artmaz).
			if err := bc.AcceptBlock(last); !errors.Is(err, ErrBlockDataMissing) {
				t.Fatalf("yeniden gönderim: %v", err)
			}
			if bc.dataAlarm.Count != 1 {
				t.Fatalf("aynı olay sayıyı artırdı: %d", bc.dataAlarm.Count)
			}

			// Veri geri gelince geçiş olur (hiçbir şey yanlışlıkla geçersiz işaretlenmemişti).
			if err := bc.db.Put([]byte(key), saved, nil); err != nil {
				t.Fatal(err)
			}
			if err := bc.AcceptBlock(last); err != nil {
				t.Fatalf("veri geri geldikten sonra: %v", err)
			}
			if tipHash(t, bc) != last.Hash {
				t.Fatal("veri geri geldikten sonra dala geçilmedi")
			}
			if !bc.MissingDataAlarm() {
				t.Fatal("alarm geçişle kendiliğinden silindi")
			}
			bc.Close()

			bc2 := openTestChain(t, dir)
			if !bc2.MissingDataAlarm() {
				t.Fatal("alarm yeniden açılışta kayboldu")
			}
			if conf, err := bc2.ConfirmationOf(last.Hash); err != nil || !conf.MissingData {
				t.Fatalf("onay bilgisi alarmı göstermiyor: %+v, %v", conf, err)
			}
			if m, err := bc2.MiningInfo(1); err != nil || !m.MissingData {
				t.Fatalf("kazım özeti alarmı göstermiyor: %+v, %v", m, err)
			}
			got, err := bc2.ClearMissingDataAlarm()
			if err != nil || got == nil || got.Hash != hash || got.Kind != kind {
				t.Fatalf("temizlenen kayıt %+v, %v", got, err)
			}
			if rec, err := bc2.ClearMissingDataAlarm(); rec != nil || err != nil {
				t.Fatalf("alarm yokken temizleme %+v, %v", rec, err)
			}
			bc2.Close()
			if openTestChain(t, dir).MissingDataAlarm() {
				t.Fatal("temizlenen alarm yeniden açılışta geri geldi")
			}
		})
	}
}

// Aynı eksik veri hedef değişse de sayıyı artırmaz (yeniden gönderimle fsync harcatılamaz);
// farklı bir eksik veri artırır.
func TestMissingDataAlarmCounts(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	main := extend(t, bc, g, 2, newKey(t).Address)
	mustAccept(t, bc, main...)
	side := extend(t, bc, g, 4, newKey(t).Address)
	mustAccept(t, bc, side[:2]...)
	if err := bc.db.Delete([]byte(side[0].Hash), nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range side[2:] {
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrBlockDataMissing) {
			t.Fatalf("blok %d: %v", b.Index, err)
		}
		if a := bc.dataAlarm; a.Count != 1 || a.Hash != side[0].Hash || a.Target != side[2].Hash {
			t.Fatalf("blok %d: kayıt %+v", b.Index, a)
		}
	}
	// s1 geri gelir, s2 kaybolur: yeni olay.
	if err := bc.db.Put([]byte(side[0].Hash), mustJSON(t, side[0]), nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.db.Delete([]byte(side[1].Hash), nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.AcceptBlock(side[3]); !errors.Is(err, ErrBlockDataMissing) {
		t.Fatalf("ikinci olay: %v", err)
	}
	if a := bc.dataAlarm; a.Count != 2 || a.Hash != side[1].Hash || a.Target != side[3].Hash {
		t.Fatalf("ikinci olay kaydı %+v", a)
	}
}

// Alarm yazımı başarısız olsa da (disk arızası) alarm bellekte etkinleşir.
func TestMissingDataAlarmWriteFailure(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	if err := bc.db.Close(); err != nil {
		t.Fatal(err)
	}
	bc.mu.Lock()
	bc.dataAlarmLocked(&missingDataError{hash: g.Hash, kind: dataBody}, bc.tip)
	bc.mu.Unlock()
	if !bc.MissingDataAlarm() {
		t.Fatal("yazılamayan alarm bellekte etkinleşmedi")
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Bozuk gövde eskisi gibi ErrCorruptState'tir, eksik veri değildir: alarm verilmez.
func TestCorruptBodyNoDataAlarm(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	main := extend(t, bc, g, 2, newKey(t).Address)
	mustAccept(t, bc, main...)
	side := extend(t, bc, g, 3, newKey(t).Address)
	mustAccept(t, bc, side[:2]...)
	if err := bc.db.Put([]byte(side[0].Hash), []byte("{"), nil); err != nil {
		t.Fatal(err)
	}
	err := bc.AcceptBlock(side[2])
	if !errors.Is(err, ErrCorruptState) || errors.Is(err, ErrBlockDataMissing) {
		t.Fatalf("hata %v", err)
	}
	if bc.MissingDataAlarm() {
		t.Fatal("bozuk gövde eksik veri alarmı verdi")
	}
}

// Eksik veri hatası eski sınıflandırmayı da taşır: gövdede leveldb.ErrNotFound, undo'da
// ErrCorruptState.
func TestMissingDataErrorClassification(t *testing.T) {
	bc := newTestChain(t)
	if _, err := bc.getBlock(hexRepeat("ab", 32)); !errors.Is(err, ErrBlockDataMissing) || errors.Is(err, ErrCorruptState) {
		t.Fatalf("gövde: %v", err)
	}
	if _, err := bc.loadUndo(hexRepeat("ab", 32)); !errors.Is(err, ErrBlockDataMissing) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("undo: %v", err)
	}
}
