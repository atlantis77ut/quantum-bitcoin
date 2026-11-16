package network

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quantum-bitcoin/core"

	"github.com/syndtr/goleveldb/leveldb"
)

// Kalıcı eksik veri alarmı (core/dataalarm.go) /confirmations ve /mininginfo'da görünür;
// alarm varken saldırganın yetişme olasılığı verilmez (düğüm hafif zincirde kalmış olabilir).
func TestMissingDataAlarmAPI(t *testing.T) {
	dir := t.TempDir()
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 3)
	tip := bc.TipHash()
	bc.Close()

	// Alarm diskteki kayıttan yüklenir (yeniden başlatma silmez).
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := `{"hash":"` + strings.Repeat("ab", 32) + `","kind":"body","tip":"` + tip + `","target":"` +
		strings.Repeat("cd", 32) + `","time":1,"count":1}`
	if err := db.Put([]byte("missing_data_alarm"), []byte(rec), nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
	bc, err = core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	if !bc.MissingDataAlarm() {
		t.Fatal("alarm yüklenmedi")
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
	c := get("/confirmations?hash=" + tip)
	if c["missing_data_alarm"] != true || c["deep_reorg_alarm"] != false || c["confirmations"] != float64(1) {
		t.Fatalf("onay yanıtı %v", c)
	}
	if _, ok := c["attacker_success"]; ok {
		t.Fatal("alarm varken yetişme olasılığı verildi")
	}
	if _, ok := c["attacker_success_depth"]; ok {
		t.Fatal("alarm varken yetişme derinliği verildi")
	}
	if m := get("/mininginfo"); m["missing_data_alarm"] != true {
		t.Fatalf("kazım özeti %v", m)
	}

	if _, err := bc.ClearMissingDataAlarm(); err != nil {
		t.Fatal(err)
	}
	c = get("/confirmations?hash=" + tip)
	if c["missing_data_alarm"] != false || c["attacker_success"] == nil {
		t.Fatalf("temizlendikten sonra %v", c)
	}
}

// Bugünkü düğüm tam geçmişlidir: budanma biti konmaz (Services 0, eski düğümlerle aynı).
func TestNoPrunedServiceBit(t *testing.T) {
	if servicePruned == 0 || localServices&servicePruned != 0 {
		t.Fatalf("yetenek bitleri %d / %d", localServices, servicePruned)
	}
	if m := stamped(message{Type: msgBlock}); m.Services&servicePruned != 0 {
		t.Fatalf("budanma biti gönderildi: %d", m.Services)
	}
}

// Sunulamayan blok "bende yok" yanıtıdır: boş msgBlock. Gövdesi diskte olmayan blok da böyle
// yanıtlanır; istemci bunu ceza vermeden başarısız istek sayar (PeerHasBlock false, hata yok).
func TestServeMissingBodyNotFound(t *testing.T) {
	dir := t.TempDir()
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 2)
	tip := bc.TipHash()
	bc.Close()
	db, err := leveldb.OpenFile(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Delete([]byte(tip), nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
	bc, err = core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	if _, err := bc.BlockByHash(tip); err == nil {
		t.Fatal("silinen gövde okundu")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go NewP2PServer("127.0.0.1:0", bc).Serve(ln)
	t.Cleanup(func() { ln.Close() })
	client := NewP2PServer("127.0.0.1:0", newChain(t))
	ok, err := client.PeerHasBlock(ln.Addr().String(), tip)
	if err != nil || ok {
		t.Fatalf("eksik gövde: var=%v hata=%v", ok, err)
	}
}
