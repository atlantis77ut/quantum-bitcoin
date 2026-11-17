package network

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quantum-bitcoin/core"

	"github.com/syndtr/goleveldb/leveldb"
)

// dropKey kapalı zincirin veri dizininden bir anahtarı siler (disk arızası taklidi).
func dropKey(t *testing.T, dir, key string) {
	t.Helper()
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Delete([]byte(key), nil); err != nil {
		t.Fatal(err)
	}
}

// behindChain kazım duraklatma eşiğine (regtest: 2 blok) ulaşmış, geride kalmış bir zincir açar
// ve ucunu döndürür: uç kendi bloğudur; başka bir zincirin ilk bloğu yan blok olarak saklıdır
// ama gövdesi diskten silinmiştir, o zincirin sonraki iki bloğu bu yüzden bağlanamaz.
func behindChain(t *testing.T) (*core.Blockchain, string) {
	t.Helper()
	src := newChain(t)
	mineN(t, src, 3)
	hs, _, _ := src.HeadersRange(1, 3)
	var side []*core.Block
	for _, h := range hs {
		b, err := src.BlockByHash(h.Hash)
		if err != nil {
			t.Fatal(err)
		}
		side = append(side, b)
	}
	if len(side) != 3 {
		t.Fatalf("kaynak zincirde %d blok, beklenen 3", len(side))
	}

	dir := t.TempDir()
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 1)
	tip := bc.TipHash()
	if err := bc.AcceptBlock(side[0]); err != nil { // eşit iş: yan blok
		t.Fatal(err)
	}
	bc.Close()
	dropKey(t, dir, side[0].Hash)
	bc, err = core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	for _, b := range side[1:] {
		if err := bc.AcceptBlock(b); !errors.Is(err, core.ErrBlockDataMissing) {
			t.Fatalf("blok %d: %v, beklenen eksik veri", b.Index, err)
		}
	}
	if bc.TipHash() != tip || bc.BehindBlocks() != 2 || !bc.MiningPaused() {
		t.Fatalf("uç %s, açık %d, duraklama %v", bc.TipHash(), bc.BehindBlocks(), bc.MiningPaused())
	}
	return bc, tip
}

// Bildiği daha çok işli zincirin eşik kadar gerisinde kalan ve ona geçemeyen düğüm kazmaz. Durum
// /mininginfo ve /confirmations'ta görünür; düğüm gerideyken yetişme olasılığı verilmez (eksik
// veri alarmı temizlense de).
func TestMinerPausesWhenBehind(t *testing.T) {
	bc, tip := behindChain(t)
	old := minWorkWait
	minWorkWait = 10 * time.Millisecond
	t.Cleanup(func() { minWorkWait = old })
	m, err := NewMiner(NewP2PServer("127.0.0.1:0", bc), minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	stopMiner := runMiner(t, m)
	time.Sleep(300 * time.Millisecond)
	stopMiner()
	if bc.TipHash() != tip || m.mined.Load() != 0 {
		t.Fatalf("geride kalan düğüm kazdı: uç %s, kazılan %d", bc.TipHash(), m.mined.Load())
	}

	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) map[string]interface{} {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		var got map[string]interface{}
		if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&got) != nil {
			t.Fatalf("%s = %d", url, w.Code)
		}
		return got
	}
	if mi := get("/mininginfo"); mi["behind_blocks"] != float64(2) || mi["mining_paused"] != true {
		t.Fatalf("kazım özeti %v", mi)
	}
	// Eksik veri alarmı temizlense de düğüm geridedir: gösterge alarmdan bağımsızdır.
	if _, err := bc.ClearMissingDataAlarm(); err != nil {
		t.Fatal(err)
	}
	c := get("/confirmations?hash=" + tip)
	if c["behind_blocks"] != float64(2) || c["missing_data_alarm"] != false || c["confirmations"] != float64(1) {
		t.Fatalf("onay yanıtı %v", c)
	}
	if _, ok := c["attacker_success"]; ok {
		t.Fatal("düğüm gerideyken yetişme olasılığı verildi")
	}
}

// Ön-senkronizasyonda yerel veri hatası (burada geri sarılacak ucun undo kaydı diskte yok) eşin
// başarısızlığı sayılmaz: eş, gövdeleri vermeyen eş gibi ertelenmez. Eskiden art arda üç tur
// sonra eşle yeni ön-senkronizasyon 10 dakikadan başlayarak ertelenirdi.
func TestPresyncLocalDataErrorNoBackoff(t *testing.T) {
	src := newChain(t)
	mineN(t, src, 30)
	addr, _ := headerServer(t, src)

	dir := t.TempDir()
	dst, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, dst, 25)
	tip := dst.TipHash()
	dst.Close()
	dropKey(t, dir, "undo_"+tip)
	dst, err = core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dst.Close() })

	ps := NewP2PServer("127.0.0.1:0", dst)
	ps.syncWindow = 8
	stop := make(chan struct{})
	defer close(stop)
	local := 0
	for i := 0; i < 20; i++ {
		err := ps.syncFrom(addr, stop)
		if core.IsLocalDataErr(err) {
			local++
		}
		if pen := ps.presyncBackoff[ps.presyncKey(addr)]; pen != nil {
			t.Fatalf("tur %d: yerel veri hatası eşi erteledi: %+v (hata: %v)", i, pen, err)
		}
	}
	// Daldaki uçtan ağır her blok bir turda saklanır ve geçiş undo kaydında düşer.
	if local <= maxPresyncFails {
		t.Fatalf("yerel veri hatası %d turda görüldü, beklenen %d'ten çok", local, maxPresyncFails)
	}
	if dst.TipHash() != tip || dst.BehindBlocks() == 0 || !dst.MissingDataAlarm() {
		t.Fatalf("uç %s, açık %d, alarm %v", dst.TipHash(), dst.BehindBlocks(), dst.MissingDataAlarm())
	}
}

// -mine-while-behind ile duraklama kapalıyken eşikteki düğüm kazmaya devam eder ve /mininginfo
// duraklamanın kapalı olduğunu gösterir.
func TestMinerMinesWhileBehindWhenOverridden(t *testing.T) {
	bc, tip := behindChain(t)
	bc.SetMineWhileBehind(true)
	h := NewAPIServer(":0", bc).Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/mininginfo", nil))
	var mi map[string]interface{}
	if w.Code != http.StatusOK || json.NewDecoder(w.Body).Decode(&mi) != nil {
		t.Fatalf("/mininginfo = %d", w.Code)
	}
	if mi["behind_blocks"] != float64(2) || mi["mining_paused"] != false || mi["mining_pause_disabled"] != true {
		t.Fatalf("kazım özeti %v", mi)
	}

	m, err := NewMiner(NewP2PServer("127.0.0.1:0", bc), minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	stopMiner := runMiner(t, m)
	deadline := time.Now().Add(20 * time.Second)
	for m.mined.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	stopMiner()
	if m.mined.Load() == 0 || bc.TipHash() == tip {
		t.Fatalf("duraklama kapalıyken kazılmadı: uç %s, kazılan %d", bc.TipHash(), m.mined.Load())
	}
}
