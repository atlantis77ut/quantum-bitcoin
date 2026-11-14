package core

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// acceptAll bloğu n çağrıyla aynı anda kabul ettirir ve hataları döndürür. Test zincirinin
// saati (newTestChain) eşzamanlı çağrıya uygun değildir; aynı noktadan atomik bir saatle sürer.
func acceptAll(bc *Blockchain, b *Block, n int) []error {
	var clock atomic.Int64
	clock.Store(bc.now())
	bc.now = func() int64 { return clock.Add(1) }
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = bc.AcceptBlock(b)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// Aynı blok aynı anda birden çok yoldan gelince imzalar bir kez doğrulanır; biri kabul eder,
// diğerleri bloğu bilinen olarak görür.
func TestAcceptBlockConcurrentVerifiesOnce(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	b := blockOn(t, bc, tip, alice.Address, signedTx(t, alice, bob.Address, Coin, 1))
	checks := bc.bodyChecks.Load()
	ok := 0
	for _, err := range acceptAll(bc, b, 8) {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrKnownBlock):
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	if ok != 1 || bc.bodyChecks.Load()-checks != 1 || tipHash(t, bc) != b.Hash {
		t.Fatalf("kabul %d, gövde denetimi %d", ok, bc.bodyChecks.Load()-checks)
	}
}

// Gövdesi geçersiz blok aynı anda gelince de bir kez doğrulanır; diğerleri önbellekten reddeder.
func TestAcceptBlockConcurrentInvalidOnce(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	bad := signedTx(t, alice, bob.Address, Coin, 1)
	bad.Amount = 2 * Coin // imzadan sonra değişti; Merkle kökü yine bu işleme göre hesaplanır
	b := blockOn(t, bc, tip, alice.Address, bad)
	checks := bc.bodyChecks.Load()
	sig := 0
	for _, err := range acceptAll(bc, b, 8) {
		switch {
		case errors.Is(err, ErrBadSignature):
			sig++
		case !errors.Is(err, ErrKnownInvalid):
			t.Fatalf("beklenmeyen hata: %v", err)
		}
	}
	if sig != 1 || bc.bodyChecks.Load()-checks != 1 {
		t.Fatalf("imza hatası %d, gövde denetimi %d", sig, bc.bodyChecks.Load()-checks)
	}
}

// Doğrulaması süren blok için gelen çağrı bekler, doğrulayan kabul etmeden bitirirse baştan
// dener ve bloğu kendisi doğrular (bekleme kabulü kaybettirmez).
func TestAcceptBlockWaitsForInflight(t *testing.T) {
	bc := newTestChain(t)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	b := blockOn(t, bc, tip, newKey(t).Address)
	release, busy := bc.inflight.claim(b.Hash)
	if busy != nil {
		t.Fatal("kayıt alınamadı")
	}
	checks := bc.bodyChecks.Load()
	done := make(chan error, 1)
	go func() { done <- bc.AcceptBlock(b) }()
	select {
	case err := <-done:
		t.Fatalf("doğrulaması süren blok beklemeden döndü: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if bc.bodyChecks.Load() != checks {
		t.Fatal("bekleyen çağrı gövdeyi doğruladı")
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("bekleyen çağrı: %v", err)
	}
	if bc.bodyChecks.Load()-checks != 1 || tipHash(t, bc) != b.Hash {
		t.Fatal("blok baştan denenmedi")
	}
	if _, busy := bc.inflight.claim(b.Hash); busy != nil {
		t.Fatal("kayıt bırakılmadı")
	}
}
