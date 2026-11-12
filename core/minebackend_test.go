package core

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// fakeBackend kazım arka ucu seam'ini GPU'suz sınar.
type fakeBackend struct {
	calls int
	scan  func(hdr [HeaderSize]byte, tgt [32]byte, start, count uint64) (uint64, bool, error)
}

func (f *fakeBackend) Scan(hdr [HeaderSize]byte, tgt [32]byte, start, count uint64) (uint64, bool, error) {
	f.calls++
	return f.scan(hdr, tgt, start, count)
}

func (f *fakeBackend) RoundSize() uint64 {
	return 64
}

func (f *fakeBackend) Name() string {
	return "sahte"
}

// cpuScan doğru davranan arka ucun CPU karşılığıdır.
func cpuScan(hdr [HeaderSize]byte, tgt [32]byte, start, count uint64) (uint64, bool, error) {
	n, _, ok, _ := scanNonces(hdr, start, count, &tgt, nil)
	return n, ok, nil
}

// Doğru arka uçla kazılan blok geçerlidir ve arka uç kullanımda kalır.
func TestMineBackendUsed(t *testing.T) {
	bc := newTestChain(t)
	fb := &fakeBackend{scan: cpuScan}
	bc.SetMineBackend(fb)
	b, err := bc.AddBlock(nil, newKey(t).Address)
	if err != nil {
		t.Fatal(err)
	}
	if fb.calls == 0 || !bc.MineBackendActive() {
		t.Fatalf("arka uç çağrıları %d, etkin %v", fb.calls, bc.MineBackendActive())
	}
	if b.CalculateHash() != b.Hash || !MeetsTarget(b.Hash, b.Bits) {
		t.Fatalf("geçersiz özet %s", b.Hash)
	}
}

// Arka ucun hedefi karşılamayan nonce'u CPU doğrulamasında yakalanır: blok bu nonce'la
// dönmez, arka uç devre dışı kalır ve kazım CPU ile geçerli blok üretir.
func TestMineBackendBadNonceFallsBack(t *testing.T) {
	bc := newTestChain(t)
	var bad uint64
	fb := &fakeBackend{scan: func(hdr [HeaderSize]byte, tgt [32]byte, start, count uint64) (uint64, bool, error) {
		for n := start; ; n++ {
			binary.BigEndian.PutUint64(hdr[0:8], n)
			h := hashHeader(hdr[:])
			if !meetsTargetBytes(&h, &tgt) {
				bad = n
				return n, true, nil
			}
		}
	}}
	bc.SetMineBackend(fb)
	b, err := bc.AddBlock(nil, newKey(t).Address)
	if err != nil {
		t.Fatal(err)
	}
	if fb.calls != 1 || bc.MineBackendActive() {
		t.Fatalf("arka uç çağrıları %d, etkin %v", fb.calls, bc.MineBackendActive())
	}
	if b.CalculateHash() != b.Hash || !MeetsTarget(b.Hash, b.Bits) {
		t.Fatalf("geçersiz özet %s (sahte nonce %d)", b.Hash, bad)
	}
}

// Arka uç hatası da kalıcı devre dışı bırakır ve kazım CPU ile sürer.
func TestMineBackendErrorFallsBack(t *testing.T) {
	bc := newTestChain(t)
	fb := &fakeBackend{scan: func([HeaderSize]byte, [32]byte, uint64, uint64) (uint64, bool, error) {
		return 0, false, errors.New("sürücü hatası")
	}}
	bc.SetMineBackend(fb)
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	if fb.calls != 1 || bc.MineBackendActive() {
		t.Fatalf("arka uç çağrıları %d, etkin %v", fb.calls, bc.MineBackendActive())
	}
}

// Arka uçla da durdurma ve eskimiş şablon denetimleri turlar arasında sürer.
func TestMineBackendStopAndStale(t *testing.T) {
	bc := newTestChain(t)
	bc.SetMineBackend(&fakeBackend{scan: cpuScan})
	tmpl, err := bc.NewTemplate(nil, newKey(t).Address)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	close(stop)
	if _, err := bc.Mine(tmpl, stop); !errors.Is(err, ErrMiningStopped) {
		t.Fatalf("durdurma: %v", err)
	}
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.Mine(tmpl, nil); !errors.Is(err, ErrStaleTemplate) {
		t.Fatalf("eskimiş şablon: %v", err)
	}
	if !bc.MineBackendActive() {
		t.Fatal("arka uç gereksiz yere devre dışı kaldı")
	}
}

func TestMeasureBackendHashrate(t *testing.T) {
	if _, err := MeasureBackendHashrate(nil, time.Millisecond); err == nil {
		t.Fatal("nil arka uç kabul edildi")
	}
	fb := &fakeBackend{scan: cpuScan}
	if _, err := MeasureBackendHashrate(fb, 0); err == nil {
		t.Fatal("sıfır süre kabul edildi")
	}
	rate, err := MeasureBackendHashrate(fb, 10*time.Millisecond)
	if err != nil || !(rate > 0) || fb.calls == 0 {
		t.Fatalf("ölçüm: %v %v %d", rate, err, fb.calls)
	}
}
