package core

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"testing"
	"time"
)

// Kazıcının bayt karşılaştırması uzlaşmanın big.Int kuralıyla (özet <= hedef) sınırlarda
// birebir aynı sonucu verir.
func TestMeetsTargetBytesMatchesBig(t *testing.T) {
	one := big.NewInt(1)
	targets := []*big.Int{
		big.NewInt(0),
		big.NewInt(1),
		big.NewInt(0xff),
		new(big.Int).Lsh(one, 200),
		new(big.Int).Sub(new(big.Int).Lsh(one, 224), one),
		new(big.Int).Set(powLimit),
		new(big.Int).Sub(new(big.Int).Lsh(one, 256), one),
	}
	if mainnet, err := TargetFromBits(mainnetMaxPowLimitBits); err == nil {
		targets = append(targets, mainnet)
	} else {
		t.Fatal(err)
	}
	toHash := func(v *big.Int) ([32]byte, bool) {
		var h [32]byte
		if v.Sign() < 0 || v.BitLen() > 256 {
			return h, false
		}
		v.FillBytes(h[:])
		return h, true
	}
	for _, tg := range targets {
		tb := targetBytes(tg)
		for _, d := range []int64{-2, -1, 0, 1, 2} {
			h, ok := toHash(new(big.Int).Add(tg, big.NewInt(d)))
			if !ok {
				continue
			}
			if got, want := meetsTargetBytes(&h, &tb), hashMeetsTarget(h, tg); got != want {
				t.Fatalf("hedef %x, fark %d: bayt %v, big.Int %v", tg, d, got, want)
			}
		}
	}
	// 2^256'yı aşan hedef her özeti karşılar (MineBlock'un kural dışı test hedefleri gibi).
	huge := targetBytes(new(big.Int).Lsh(one, 300))
	var top [32]byte
	for i := range top {
		top[i] = 0xff
	}
	if !meetsTargetBytes(&top, &huge) {
		t.Fatal("2^256'yı aşan hedef en büyük özeti karşılamıyor")
	}
}

func TestNextRound(t *testing.T) {
	if n, ok := nextRound(0, 4); !ok || n != 4 {
		t.Fatalf("0+4: %d %v", n, ok)
	}
	if _, ok := nextRound(math.MaxUint64-3, 4); ok {
		t.Fatal("tam 2^64'te tükenme algılanmadı")
	}
	if _, ok := nextRound(math.MaxUint64-1, 3); ok {
		t.Fatal("2^64'ü aşan turda tükenme algılanmadı")
	}
}

// Taşmada nonce sarar ve bulunan nonce başlıkla gerçekten eşleşir.
func TestScanNoncesWraps(t *testing.T) {
	var hdr [HeaderSize]byte
	all := targetBytes(new(big.Int).Lsh(big.NewInt(1), 256))
	n, _, ok, tried := scanNonces(hdr, math.MaxUint64, 2, &all, nil)
	if !ok || n != math.MaxUint64 || tried != 1 {
		t.Fatalf("ilk nonce: %d %v %d", n, ok, tried)
	}
	var none [32]byte
	if _, _, ok, tried := scanNonces(hdr, math.MaxUint64, 3, &none, nil); ok || tried != 3 {
		t.Fatalf("sıfır hedef: %v %d", ok, tried)
	}
}

func TestSetMineWorkers(t *testing.T) {
	bc := newTestChain(t)
	for _, bad := range []int{0, -1, MaxMineWorkers + 1} {
		if err := bc.SetMineWorkers(bad); err == nil {
			t.Fatalf("%d kabul edildi", bad)
		}
	}
	if got := bc.mineWorkerCount(); got != DefaultMineWorkers() {
		t.Fatalf("varsayılan %d, beklenen %d", got, DefaultMineWorkers())
	}
	if err := bc.SetMineWorkers(3); err != nil || bc.mineWorkerCount() != 3 {
		t.Fatalf("3: %v %d", err, bc.mineWorkerCount())
	}
}

// Çok parçacıkla kazılan bloklar geçerlidir: özet başlıkla eşleşir ve zincir kabul eder.
// İkinin kuvveti olmayan parçacık sayısı ve küçük dilimler tur sınırlarını sınar.
func TestMineMultiWorker(t *testing.T) {
	for _, workers := range []int{1, 2, 3, 8} {
		bc := newTestChain(t)
		if err := bc.SetMineWorkers(workers); err != nil {
			t.Fatal(err)
		}
		bc.mineChunk = 7
		miner := newKey(t).Address
		for i := 0; i < 4; i++ {
			b, err := bc.AddBlock(nil, miner)
			if err != nil {
				t.Fatalf("%d parçacık, blok %d: %v", workers, i, err)
			}
			if b.CalculateHash() != b.Hash || !MeetsTarget(b.Hash, b.Bits) {
				t.Fatalf("%d parçacık: geçersiz özet %s", workers, b.Hash)
			}
		}
	}
}

// Çok parçacıkta durdurma ve eskimiş şablon tek parçacıktaki gibi döner.
func TestMineMultiWorkerStopAndStale(t *testing.T) {
	bc := newTestChain(t)
	if err := bc.SetMineWorkers(4); err != nil {
		t.Fatal(err)
	}
	bc.mineChunk = 1
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
}

func TestMeasureHashrate(t *testing.T) {
	for _, bad := range []struct {
		w int
		d time.Duration
	}{{0, time.Millisecond}, {MaxMineWorkers + 1, time.Millisecond}, {1, 0}, {1, -time.Second}} {
		if _, err := MeasureHashrate(bad.w, bad.d); err == nil {
			t.Fatalf("%d parçacık, %v kabul edildi", bad.w, bad.d)
		}
	}
	rate, err := MeasureHashrate(2, 50*time.Millisecond)
	if err != nil || !(rate > 0) {
		t.Fatalf("ölçüm: %v %v", rate, err)
	}
}

// go test -run '^$' -bench Hashrate ./core: tek ve tüm çekirdekte Q-BTC başlığı hashrate'i.
func BenchmarkHashrate(b *testing.B) {
	for _, workers := range []int{1, DefaultMineWorkers()} {
		b.Run("parcacik="+strconv.Itoa(workers), func(b *testing.B) {
			var hdr [HeaderSize]byte
			var tgt [32]byte
			b.ResetTimer()
			start := time.Now()
			per := uint64(b.N/workers + 1)
			mineRound(hdr, 0, per, workers, &tgt)
			b.ReportMetric(float64(per)*float64(workers)/time.Since(start).Seconds(), "hash/s")
		})
	}
}
