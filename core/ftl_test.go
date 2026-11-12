package core

import (
	"errors"
	"testing"
)

func TestTimestampRuleConstants(t *testing.T) {
	if MaxFutureDrift != 300 || MedianTimeSpan != 11 {
		t.Fatalf("FTL %d, MTP %d", MaxFutureDrift, MedianTimeSpan)
	}
}

// futureBlock ucun üzerine, yerel saatin `ahead` saniye ilerisinde bir blok kazar.
func futureBlock(t *testing.T, bc *Blockchain, ahead int64, txs ...Transaction) *Block {
	t.Helper()
	prev, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	all := append([]Transaction{coinbaseWithFees(newKey(t).Address, txs...)}, txs...)
	b := &Block{Index: prev.Index + 1, Timestamp: bc.now() + ahead, Transactions: all, PrevHash: prev.Hash}
	mineAt(t, bc, b)
	return b
}

func fixedClock(bc *Blockchain, at int64) *int64 {
	clock := at
	bc.now = func() int64 { return clock }
	return &clock
}

// FTL'i aşan blokta imza doğrulanmaz ve blok hiçbir önbelleğe girmez: tekrar tekrar
// gönderilse de doğrulama yaptırmaz, saat yetişince aynı blok kabul edilebilir.
func TestFutureBlockRejectedBeforeSignatures(t *testing.T) {
	bc := newTestChain(t)
	clock := fixedClock(bc, 1_900_000_000)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	bad := signedTx(t, alice, bob.Address, Coin, 1)
	bad.Amount++ // imza artık geçersiz

	b := futureBlock(t, bc, MaxFutureDrift+1, bad)
	checks := bc.bodyChecks.Load()
	for i := 0; i < 3; i++ {
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
			t.Fatalf("deneme %d: %v, beklenen ErrTimestampFuture (imzadan önce)", i, err)
		}
	}
	if bc.bodyChecks.Load() != checks || bc.rejected.has(b.Hash) {
		t.Fatal("gelecek tarihli blok doğrulandı ya da önbelleğe alındı")
	}
	*clock++
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("saat yetişince gövde doğrulanmalı: %v", err)
	}
}

// Sınır: tam FTL kabul edilir, bir saniye ötesi gövde doğrulanmadan düşer.
func TestFutureBoundaries(t *testing.T) {
	bc := newTestChain(t)
	fixedClock(bc, 1_900_000_000)
	if err := bc.AcceptBlock(futureBlock(t, bc, MaxFutureDrift)); err != nil {
		t.Fatalf("tam FTL: %v", err)
	}
	checks := bc.bodyChecks.Load()
	if err := bc.AcceptBlock(futureBlock(t, bc, MaxFutureDrift+1)); !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("FTL+1: %v", err)
	}
	if bc.bodyChecks.Load() != checks {
		t.Fatal("FTL dışı blok doğrulandı")
	}
}
