package core

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Çok çekirdekli kazım (yalnızca kazıcı; uzlaşma doğrulaması target.go'daki hashMeetsTarget
// ile yapılır ve değişmez).
//
// Midstate yoktur: nonce ilk 64 baytlık SHA-256 bloğundadır (header.go), bu yüzden her
// deneme başlığın iki bloğunu ve ikinci SHA-256'yı baştan hesaplar. İkinci bloğun mesaj
// takvimi şablon başına sabittir, ama onu kullanmak SHA-256 sıkıştırmasını elle yazmayı
// gerektirir ve standart kütüphanenin donanım hızlandırmalı (SHA-NI) yolundan yavaştır.

// MaxMineWorkers kazım iş parçacığı sayısının üst sınırıdır.
const MaxMineWorkers = 1024

// scanCheck iş parçacığının başka bir parçacığın bulduğunu kaç denemede bir denetlediğidir.
const scanCheck = 1 << 10

var errBadMeasure = errors.New("geçersiz hashrate ölçümü parametresi")

// DefaultMineWorkers varsayılan kazım iş parçacığı sayısıdır: tüm çekirdekler.
func DefaultMineWorkers() int {
	return min(max(runtime.NumCPU(), 1), MaxMineWorkers)
}

// SetMineWorkers kazımda kullanılacak iş parçacığı sayısını ayarlar.
func (bc *Blockchain) SetMineWorkers(n int) error {
	if n < 1 || n > MaxMineWorkers {
		return fmt.Errorf("kazım iş parçacığı sayısı 1 ile %d arasında olmalı: %d", MaxMineWorkers, n)
	}
	bc.mineWorkers.Store(int64(n))
	return nil
}

func (bc *Blockchain) mineWorkerCount() int {
	if n := bc.mineWorkers.Load(); n > 0 {
		return int(n)
	}
	return DefaultMineWorkers()
}

// targetBytes hedefi 32 baytlık büyük-endian diziye çevirir; böylece her denemede big.Int
// kurulmaz. 2^256'yı aşan hedef her özeti karşılar.
func targetBytes(t *big.Int) [32]byte {
	var b [32]byte
	if t.Sign() < 0 {
		return b
	}
	if t.BitLen() > 256 {
		for i := range b {
			b[i] = 0xff
		}
		return b
	}
	t.FillBytes(b[:])
	return b
}

// meetsTargetBytes hashMeetsTarget ile aynı kuraldır (özet <= hedef), big.Int'siz.
func meetsTargetBytes(h *[32]byte, tgt *[32]byte) bool {
	return bytes.Compare(h[:], tgt[:]) <= 0
}

// scanNonces başlığı [start, start+n) nonce'larıyla dener (taşma sarar) ve hedefi karşılayan
// ilk nonce'u döndürür. done kurulursa her scanCheck denemede bir denetlenir. tried denenen
// nonce sayısıdır.
func scanNonces(hdr [HeaderSize]byte, start, n uint64, tgt *[32]byte, done *atomic.Bool) (nonce uint64, h [32]byte, ok bool, tried uint64) {
	for i := uint64(0); i < n; i++ {
		if done != nil && i&(scanCheck-1) == 0 && done.Load() {
			return 0, [32]byte{}, false, i
		}
		nonce = start + i
		binary.BigEndian.PutUint64(hdr[0:8], nonce)
		h = hashHeader(hdr[:])
		if meetsTargetBytes(&h, tgt) {
			return nonce, h, true, i + 1
		}
	}
	return 0, [32]byte{}, false, n
}

// mineRound workers parçacıkla [start, start+workers·chunk) aralığını tarar; her parçacık
// kendi chunk'lık dilimini kendi başlık kopyasıyla dener. Biri bulunca diğerleri durur ve
// tüm parçacıklar bitmeden dönülmez. Aynı turda birden çok bulunursa en küçük dilim kazanır.
func mineRound(hdr [HeaderSize]byte, start, chunk uint64, workers int, tgt *[32]byte) (uint64, [32]byte, bool) {
	if workers <= 1 {
		n, h, ok, _ := scanNonces(hdr, start, chunk, tgt, nil)
		return n, h, ok
	}
	type result struct {
		nonce uint64
		hash  [32]byte
		ok    bool
	}
	res := make([]result, workers)
	var found atomic.Bool
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			n, h, ok, _ := scanNonces(hdr, start+uint64(w)*chunk, chunk, tgt, &found)
			if ok {
				res[w] = result{n, h, true}
				found.Store(true)
			}
		}(w)
	}
	wg.Wait()
	for _, r := range res {
		if r.ok {
			return r.nonce, r.hash, true
		}
	}
	return 0, [32]byte{}, false
}

// nextRound sonraki turun başlangıç nonce'unu döndürür; 2^64 nonce tükendiyse false.
func nextRound(nonce, round uint64) (uint64, bool) {
	next := nonce + round
	return next, next > nonce
}

// MeasureHashrate workers parçacıkla en az d süre boyunca Q-BTC başlığını kazımla aynı
// turlarla (mineRound: tur başına parçacık başına mineChunk deneme, en yavaş parçacık
// beklenir) özetler ve saniyedeki özet sayısını döndürür. Lansmanda açıklanan hashrate'in
// herkesçe yeniden üretilmesi içindir (docs/MAINNET-ONCESI.md madde 6a).
func MeasureHashrate(workers int, d time.Duration) (float64, error) {
	if workers < 1 || workers > MaxMineWorkers || d <= 0 {
		return 0, errBadMeasure
	}
	var hdr [HeaderSize]byte
	var tgt [32]byte // sıfır hedef: pratikte hiçbir özet karşılamaz, tur tam çalışır
	per := uint64(mineChunk) * uint64(workers)
	var nonce, total uint64
	begin := time.Now()
	for time.Since(begin) < d {
		mineRound(hdr, nonce, mineChunk, workers, &tgt)
		nonce += per
		total += per
	}
	elapsed := time.Since(begin).Seconds()
	if elapsed <= 0 {
		return 0, errBadMeasure
	}
	return float64(total) / elapsed, nil
}
