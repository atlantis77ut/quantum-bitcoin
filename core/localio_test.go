package core

import (
	"errors"
	"testing"
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
