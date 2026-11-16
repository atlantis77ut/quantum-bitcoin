package core

import (
	"errors"
	"testing"
)

// Kapı yalnızca önbellek kaçırmasında, uç bilgisiyle sorulur; reddederse gövde okunmaz ve
// önbelleğe girmez; izin verirse release bir kez çağrılır.
func TestReadGate(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	old, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	tip, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}

	deny := errors.New("reddedildi")
	var calls, releases int
	var lastTip bool
	gate := func(allow bool) ReadGate {
		return func(isTip bool) (func(), error) {
			calls++
			lastTip = isTip
			if !allow {
				return nil, deny
			}
			return func() { releases++ }, nil
		}
	}

	if _, _, err := bc.Reads(gate(false)).MainBlockByHash(old.Hash); !errors.Is(err, deny) {
		t.Fatalf("reddedilen kaçırma: %v", err)
	}
	if _, ok := bc.blockCache.get(old.Hash); ok || calls != 1 || lastTip {
		t.Fatalf("reddedilen okuma önbelleğe girdi ya da kapı yanlış soruldu (çağrı %d, uç %v)", calls, lastTip)
	}
	if _, _, _, err := bc.Reads(gate(false)).TxByID(old.Transactions[0].ID()); !errors.Is(err, deny) {
		t.Fatalf("TxByID kapıdan geçmedi: %v", err)
	}

	b, _, err := bc.Reads(gate(true)).MainBlockByHeight(old.Index)
	if err != nil || b.Hash != old.Hash || releases != 1 {
		t.Fatalf("izin verilen kaçırma %v, release %d", err, releases)
	}
	calls = 0
	if _, _, err := bc.Reads(gate(false)).MainBlockByHash(old.Hash); err != nil || calls != 0 {
		t.Fatalf("önbellekten okuma kapıya soruldu (çağrı %d): %v", calls, err)
	}

	if _, err := bc.Reads(gate(false)).LatestBlock(); !errors.Is(err, deny) || !lastTip {
		t.Fatalf("uç okuması uç olarak sorulmadı: %v %v", err, lastTip)
	}
	if b, err := bc.Reads(gate(true)).LatestBlock(); err != nil || b.Hash != tip.Hash || releases != 2 {
		t.Fatalf("uç okuması %v, release %d", err, releases)
	}
	if _, err := bc.Reads(nil).LatestBlock(); err != nil { // nil kapı: eski davranış
		t.Fatal(err)
	}
}
