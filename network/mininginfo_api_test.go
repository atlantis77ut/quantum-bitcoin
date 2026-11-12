package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// /mininginfo pencereyi ve tahmini hashrate'i döndürür; kısa pencerede hashrate verilmez;
// geçersiz pencere 400 olur.
func TestMiningInfoEndpoint(t *testing.T) {
	bc := newChain(t)
	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) (int, map[string]interface{}) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var got map[string]interface{}
		if rec.Code == http.StatusOK {
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, got
	}

	code, got := get("/mininginfo")
	if code != http.StatusOK || got["window_blocks"] != float64(0) || got["hashrate"] != nil || got["stale_rate"] != nil {
		t.Fatalf("genesis = %d %v", code, got)
	}

	mineN(t, bc, 12)
	code, got = get("/mininginfo")
	if code != http.StatusOK || got["window_blocks"] != float64(12) || got["tip_height"] != float64(12) ||
		got["stale_rate"] != float64(0) || got["deep_reorg_alarm"] != false {
		t.Fatalf("12 blok = %d %v", code, got)
	}
	if r, ok := got["hashrate"].(float64); !ok || r <= 0 || got["mean_block_seconds"] == nil {
		t.Fatalf("hashrate yok: %v", got)
	}
	if w, ok := got["work"].(string); !ok || w == "" || w == "0" {
		t.Fatalf("iş %v", got["work"])
	}
	if b, ok := got["next_bits"].(string); !ok || len(b) != 8 {
		t.Fatalf("next_bits %v", got["next_bits"])
	}

	code, got = get("/mininginfo?blocks=5")
	if code != http.StatusOK || got["window_blocks"] != float64(5) || got["hashrate"] != nil || got["mean_block_seconds"] != nil {
		t.Fatalf("kısa pencere = %d %v", code, got)
	}

	for _, u := range []string{"/mininginfo?blocks=0", "/mininginfo?blocks=10081", "/mininginfo?blocks=x", "/mininginfo?blocks=-1"} {
		if code, _ := get(u); code != http.StatusBadRequest {
			t.Errorf("%s = %d, beklenen 400", u, code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mininginfo", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
}
