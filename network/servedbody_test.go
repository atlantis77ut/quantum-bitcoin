package network

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"

	"quantum-bitcoin/core"
)

// corruptBodyOnDisk kapalı zincirin dizinindeki bloğun gövdesini başlığıyla tutmayacak biçimde
// bozar (özet alanı sağlam, coinbase tutarı değişir, Merkle kökü tutmaz) ve zinciri yeniden açar.
func corruptBodyOnDisk(t *testing.T, dir, hash string) *core.Blockchain {
	t.Helper()
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.Get([]byte(hash), nil)
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
	if err := db.Put([]byte(hash), data, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()

	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	return bc
}

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

	bc = corruptBodyOnDisk(t, dir, b1.Hash)
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

// Senkronizasyon sonrası yayın: sağlam uç bir kez, indirildiği eş işaretiyle yayın kuyruğuna
// girer; diskte gövdesi bozulmuş uç yayılmaz ve hata kaydedilir. Serve çalıştırılmaz, yayın
// döngüsü kuyruğu boşaltmaz.
func TestSyncRelaySkipsCorruptTip(t *testing.T) {
	dir := t.TempDir()
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	first, closed := bc, false
	t.Cleanup(func() { // sağlam aşamada test düşerse LevelDB kilidi açık kalmasın
		if !closed {
			first.Close()
		}
	})
	mineN(t, bc, 2)
	tip := bc.TipHash()

	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.relaySyncedTip(tip, "eş")
	if n := len(ps.relayCh); n != 1 {
		t.Fatalf("sağlam uç için yayın kuyruğunda %d öğe, beklenen 1", n)
	}
	if it := <-ps.relayCh; it.b == nil || it.b.Hash != tip || it.from != "eş" {
		t.Fatalf("yayın öğesi yanlış: %+v", it)
	}
	if ps.corruptWarn.Load() != 0 {
		t.Fatal("sağlam uç bozuk diye kaydedildi")
	}
	bc.Close()
	closed = true

	bc = corruptBodyOnDisk(t, dir, tip)
	if bc.TipHash() != tip {
		t.Fatalf("yeniden açılan zincirin ucu değişti: %s", bc.TipHash())
	}
	ps = NewP2PServer("127.0.0.1:0", bc)
	ps.relaySyncedTip(tip, "eş")
	if n := len(ps.relayCh); n != 0 {
		t.Fatalf("bozuk uç yayın kuyruğuna girdi (%d öğe)", n)
	}
	if ps.corruptWarn.Load() == 0 {
		t.Fatal("bozuk uç kaydedilmedi")
	}
}
