package core

import (
	"errors"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

var errDiskFull = errors.New("disk dolu (test)")

// Blok saklanırken yazma hatası yerel disk hatasıdır: blok indekse girmez, uç değişmez, geride
// kalma kaydı kurulmaz ve blok reddedilmiş sayılmaz; ağ katmanı eşi bunun için ertelemez
// (IsLocalDataErr). Disk düzelince aynı blok kabul edilir.
func TestStoreWriteFaultIsLocal(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	b := blockOn(t, bc, g, newKey(t).Address)
	bc.writeFault = func(string) error { return errDiskFull }
	err := bc.AcceptBlock(b)
	bc.writeFault = nil
	if !errors.Is(err, ErrLocalIO) || !errors.Is(err, errDiskFull) || !IsLocalDataErr(err) {
		t.Fatalf("hata %v, beklenen yerel disk hatası", err)
	}
	if _, ok := bc.index[b.Hash]; ok {
		t.Fatal("yazılamayan blok indekste")
	}
	if tipHash(t, bc) != g.Hash || bc.behind != "" || bc.rejected.has(b.Hash) {
		t.Fatalf("uç %s, kayıt %q, reddedildi %v", tipHash(t, bc), bc.behind, bc.rejected.has(b.Hash))
	}
	mustAccept(t, bc, b)
	if tipHash(t, bc) != b.Hash {
		t.Fatal("disk düzeldikten sonra blok bağlanmadı")
	}
}

// Zincire geçişin yazması düşerse blok saklı kalır, geçersiz işaretlenmez ve reddedilmiş
// sayılmaz (dürüst blok disk hatasıyla kaybedilmez); düğüm geride sayılır. Blok yeniden
// gelince geçiş yeniden denenir ve kayıt silinir.
func TestActivateWriteFaultIsLocal(t *testing.T) {
	bc := newTestChain(t)
	m1 := blockOn(t, bc, genesisOf(t, bc), newKey(t).Address)
	mustAccept(t, bc, m1)
	b := blockOn(t, bc, m1, newKey(t).Address)
	bc.writeFault = func(op string) error {
		if op == opChainUpdate {
			return errDiskFull
		}
		return nil
	}
	err := bc.AcceptBlock(b)
	bc.writeFault = nil
	if !errors.Is(err, ErrLocalIO) || !errors.Is(err, errDiskFull) || !IsLocalDataErr(err) {
		t.Fatalf("hata %v, beklenen yerel disk hatası", err)
	}
	e, ok := bc.index[b.Hash]
	if !ok || e.Status == statusInvalid || bc.rejected.has(b.Hash) {
		t.Fatalf("blok indekste %v, geçersiz %v, reddedildi %v", ok, ok && e.Status == statusInvalid, bc.rejected.has(b.Hash))
	}
	if tipHash(t, bc) != m1.Hash || bc.behind != b.Hash || bc.BehindBlocks() != 1 {
		t.Fatalf("uç %s, kayıt %q, açık %d", tipHash(t, bc), bc.behind, bc.BehindBlocks())
	}

	if err := bc.AcceptBlock(b); err != nil {
		t.Fatalf("yeniden gönderimde geçiş: %v", err)
	}
	if tipHash(t, bc) != b.Hash || bc.behind != "" || bc.BehindBlocks() != 0 {
		t.Fatalf("uç %s, kayıt %q, açık %d", tipHash(t, bc), bc.behind, bc.BehindBlocks())
	}
}

// Uzlaşma yolundaki okuma hataları da yerel disk hatasıdır; eksik kayıt (leveldb.ErrNotFound)
// eskisi gibi eksik veri hatası kalır.
func TestReadErrorIsLocal(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	if _, err := bc.getBlock("00" + g.Hash[2:]); !errors.Is(err, ErrBlockDataMissing) || errors.Is(err, ErrLocalIO) {
		t.Fatalf("eksik gövde: %v", err)
	}
	if err := bc.db.Close(); err != nil { // okuma hatası taklidi; temizlikteki ikinci Close ErrClosed döner
		t.Fatal(err)
	}
	if _, err := bc.getBlock(g.Hash); !errors.Is(err, ErrLocalIO) || !IsLocalDataErr(err) {
		t.Fatalf("gövde okuması: %v", err)
	}
	if _, _, err := bc.getRaw(prefixBalance + "x"); !errors.Is(err, ErrLocalIO) {
		t.Fatalf("durum okuması: %v", err)
	}
	if _, err := bc.readInt(prefixBalance + "x"); !errors.Is(err, ErrLocalIO) {
		t.Fatalf("bakiye okuması: %v", err)
	}
	if _, err := bc.loadIndexUndo(g.Hash); !errors.Is(err, ErrLocalIO) {
		t.Fatalf("indeks geri alma okuması: %v", err)
	}
}

// Açılışta son blok, şema ya da genesis kaydı okunamazsa hata yerel disk hatasıdır, şema ya da
// genesis tutmazlığı değil (operatör sağlam dizini başka ağın dizini sanıp silmesin). Eksik
// kayıtlar eskisi gibi sınıflanır.
func TestReadMetaErrors(t *testing.T) {
	stored := map[string]string{keyLastHash: "uc", keySchema: schemaVersion, keyGenesis: testParams.GenesisHash}
	get := func(fail, missing string) func([]byte, *opt.ReadOptions) ([]byte, error) {
		return func(k []byte, _ *opt.ReadOptions) ([]byte, error) {
			v, ok := stored[string(k)]
			switch {
			case string(k) == fail:
				return nil, errDiskFull
			case string(k) == missing || !ok:
				return nil, leveldb.ErrNotFound
			}
			return []byte(v), nil
		}
	}
	if last, found, err := readMeta(get("", ""), testParams); err != nil || !found || string(last) != "uc" {
		t.Fatalf("sağlam kayıt: %q %v %v", last, found, err)
	}
	if _, found, err := readMeta(get("", keyLastHash), testParams); err != nil || found {
		t.Fatalf("yeni dizin: %v %v", found, err)
	}
	for _, k := range []string{keyLastHash, keySchema, keyGenesis} {
		_, found, err := readMeta(get(k, ""), testParams)
		if found || !errors.Is(err, ErrLocalIO) || !errors.Is(err, errDiskFull) || errors.Is(err, ErrSchemaMismatch) ||
			errors.Is(err, ErrGenesisMismatch) {
			t.Fatalf("%s okunamadı: %v %v", k, found, err)
		}
	}
	if _, _, err := readMeta(get("", keySchema), testParams); !errors.Is(err, ErrSchemaMismatch) || errors.Is(err, ErrLocalIO) {
		t.Fatalf("eksik şema: %v", err)
	}
	if _, _, err := readMeta(get("", keyGenesis), testParams); !errors.Is(err, ErrGenesisMismatch) || errors.Is(err, ErrLocalIO) {
		t.Fatalf("eksik genesis: %v", err)
	}
}

// Boşluk denetiminde tarama hatası yerel disk hatasıdır: okunamayan dizin boş sayılıp üstüne
// genesis yazılmaz.
func TestDBEmptyScanError(t *testing.T) {
	db, err := leveldb.OpenFile(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty, err := dbEmpty(db); err != nil || !empty {
		t.Fatalf("yeni dizin: %v %v", empty, err)
	}
	if err := db.Put([]byte("x"), []byte("1"), nil); err != nil {
		t.Fatal(err)
	}
	if empty, err := dbEmpty(db); err != nil || empty {
		t.Fatalf("dolu dizin: %v %v", empty, err)
	}
	if err := db.Close(); err != nil { // tarama hatası taklidi
		t.Fatal(err)
	}
	if empty, err := dbEmpty(db); empty || !errors.Is(err, ErrLocalIO) || !errors.Is(err, leveldb.ErrClosed) {
		t.Fatalf("okunamayan dizin: %v %v", empty, err)
	}
}

// Uzlaşma yolu dışındaki okuma ve yazmalar da yerel disk hatası verir: undo (geçiş yolunda;
// eskiden bozuk durum sayılırdı), açılıştaki indeks ve alarm okumaları, alarm silme, havuz
// kaydı, adres geçmişi, işlem indeksi ve gövde önbelleğinin disk okuması. Eksik gövde önbellek
// yolunda da eksik veri hatasıdır.
func TestOtherReadErrorsAreLocal(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	b1, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	m := NewMempool(bc)
	if _, err := bc.finishBlockRead(blockRead{hash: hexRepeat("ab", 32)}); !errors.Is(err, ErrBlockDataMissing) ||
		errors.Is(err, ErrLocalIO) || errors.Is(err, ErrCorruptState) {
		t.Fatalf("eksik gövde: %v", err)
	}
	if err := bc.db.Close(); err != nil { // okuma hatası taklidi; temizlikteki ikinci Close ErrClosed döner
		t.Fatal(err)
	}
	isLocal := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrLocalIO) || !errors.Is(err, leveldb.ErrClosed) || errors.Is(err, ErrCorruptState) ||
			errors.Is(err, ErrBlockDataMissing) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	_, err = bc.loadUndo(b1.Hash)
	isLocal("undo", err)
	isLocal("indeks", bc.loadIndex())
	isLocal("reorg alarmı", bc.loadReorgAlarm())
	isLocal("eksik veri alarmı", bc.loadDataAlarm())
	bc.deepReorg = &DeepReorgRecord{}
	_, err = bc.ClearDeepReorgAlarm()
	isLocal("reorg alarmı silme", err)
	bc.dataAlarm = &MissingDataRecord{}
	_, err = bc.ClearMissingDataAlarm()
	isLocal("eksik veri alarmı silme", err)
	if bc.deepReorg == nil || bc.dataAlarm == nil {
		t.Fatal("silinemeyen alarm bellekte temizlendi")
	}
	_, _, err = m.Load()
	isLocal("havuz yükleme", err)
	isLocal("havuz kaydı", m.writeSnapshot(nil, []byte("{}")))
	_, err = bc.AddressHistory(miner, "", 10)
	isLocal("adres geçmişi", err)
	_, _, _, err = bc.TxByID(b1.Transactions[0].ID())
	isLocal("işlem indeksi", err)
	_, err = bc.finishBlockRead(blockRead{hash: b1.Hash})
	isLocal("gövde önbelleği", err)
}
