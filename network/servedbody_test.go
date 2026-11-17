package network

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"

	"quantum-bitcoin/core"
)

// Diskte bozulmuş (başlığıyla tutmayan) gövde eşe gönderilmez: istek boş blok yanıtı alır ve
// hata kaydedilir; sağlam blok sunulmaya devam eder.
func TestCorruptBodyNotServed(t *testing.T) {
	dir := t.TempDir()
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 2)
	b1, _, err := bc.MainBlockByHeight(1)
	if err != nil {
		t.Fatal(err)
	}
	bc.Close()

	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.Get([]byte(b1.Hash), nil)
	if err != nil {
		t.Fatal(err)
	}
	var blk core.Block
	if err := json.Unmarshal(raw, &blk); err != nil {
		t.Fatal(err)
	}
	blk.Transactions[0].Amount++ // özet alanı sağlam, işlemler kökle tutmaz
	data, err := json.Marshal(&blk)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte(b1.Hash), data, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()

	bc, err = core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	addr := ln.Addr().String()

	resp, err := ps.request(addr, message{Type: msgGetBlock, Hash: b1.Hash}, maxBlockMessageBytes)
	if err != nil || resp.Block != nil {
		t.Fatalf("bozuk gövde sunuldu ya da yanıt yok: %v", err)
	}
	if ps.corruptWarn.Load() == 0 {
		t.Fatal("bozuk gövde kaydedilmedi")
	}
	tip := bc.TipHash()
	resp, err = ps.request(addr, message{Type: msgGetBlock, Hash: tip}, maxBlockMessageBytes)
	if err != nil || resp.Block == nil || resp.Block.Hash != tip {
		t.Fatalf("sağlam blok sunulmadı: %v", err)
	}
}
