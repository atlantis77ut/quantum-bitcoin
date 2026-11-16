package core

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
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

// Önbellek kaçırmasında disk okuması ve çözme zincir kilidi bırakıldıktan sonra yapılır: okuma
// diskte beklerken yazma kilidi alınabilir. release kaçırma başına bir kez çağrılır, gövde
// kilitsiz okunduktan sonra bozuk çıksa da.
func TestBlockLoadOutsideLock(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	var blocks []*Block
	for range 4 {
		b, err := bc.AddBlock(nil, miner)
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, b)
	}
	var releases atomic.Int32
	gate := func(bool) (func(), error) { return func() { releases.Add(1) }, nil }

	entered, proceed := make(chan struct{}), make(chan struct{})
	bc.beforeBlockLoad = func() {
		entered <- struct{}{}
		<-proceed
	}
	reads := []struct {
		name string
		want *Block
		read func() (*Block, error)
	}{
		{"TxByID", blocks[0], func() (*Block, error) {
			b, _, _, err := bc.Reads(gate).TxByID(blocks[0].Transactions[0].ID())
			return b, err
		}},
		{"MainBlockByHeight", blocks[1], func() (*Block, error) {
			b, _, err := bc.Reads(gate).MainBlockByHeight(blocks[1].Index)
			return b, err
		}},
		{"LatestBlock", blocks[3], func() (*Block, error) { return bc.Reads(gate).LatestBlock() }},
	}
	for i, c := range reads {
		if _, ok := bc.blockCache.get(c.want.Hash); ok {
			t.Fatalf("%s: blok baştan önbellekte", c.name)
		}
		var got *Block
		done := make(chan error, 1)
		go func() {
			b, err := c.read()
			got = b
			done <- err
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: disk okumasına ulaşılmadı", c.name)
		}
		free := bc.mu.TryLock()
		if free {
			bc.mu.Unlock()
		}
		proceed <- struct{}{}
		err := <-done
		if !free {
			t.Fatalf("%s: okuma diskte beklerken zincir kilidi tutuluyor", c.name)
		}
		if err != nil || got.Hash != c.want.Hash {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, ok := bc.blockCache.get(c.want.Hash); !ok || releases.Load() != int32(i+1) {
			t.Fatalf("%s: önbelleğe girmedi ya da release %d", c.name, releases.Load())
		}
	}

	bc.beforeBlockLoad = nil
	bad := blocks[2]
	if err := bc.db.Put([]byte(bad.Hash), []byte("{"), nil); err != nil {
		t.Fatal(err)
	}
	before := releases.Load()
	if _, _, err := bc.Reads(gate).MainBlockByHash(bad.Hash); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("bozuk gövde: %v", err)
	}
	if _, ok := bc.blockCache.get(bad.Hash); ok || releases.Load() != before+1 {
		t.Fatalf("bozuk gövde önbelleğe girdi ya da release %d", releases.Load()-before)
	}
}
