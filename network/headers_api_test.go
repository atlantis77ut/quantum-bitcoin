package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quantum-bitcoin/core"
)

func TestHeaderEndpoints(t *testing.T) {
	bc := newChain(t)
	mineN(t, bc, 3)
	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec
	}

	var tip core.BlockHeader
	if rec := get("/header"); rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&tip) != nil ||
		tip.Hash != bc.TipHash() || tip.Index != 3 {
		t.Fatalf("uç başlığı %d %+v", rec.Code, tip)
	}
	var byHash core.BlockHeader
	if rec := get("/header?hash=" + strings.ToUpper(tip.PrevHash)); rec.Code != http.StatusOK ||
		json.NewDecoder(rec.Body).Decode(&byHash) != nil || byHash.Index != 2 {
		t.Fatalf("özetle başlık %d %+v", rec.Code, byHash)
	}

	type page struct {
		TipHeight int64              `json:"tip_height"`
		TipHash   string             `json:"tip_hash"`
		Headers   []core.BlockHeader `json:"headers"`
	}
	var p page
	if rec := get("/headers?from=1&count=2"); rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&p) != nil ||
		p.TipHeight != 3 || p.TipHash != tip.Hash || len(p.Headers) != 2 ||
		p.Headers[0].Index != 1 || p.Headers[1].PrevHash != p.Headers[0].Hash {
		t.Fatalf("sayfa %d %+v", rec.Code, p)
	}
	rec := get("/headers?from=9223372036854775807&count=500")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"headers":[]`) {
		t.Fatalf("uç ötesi sayfa %d %s", rec.Code, rec.Body.String())
	}
	var def page
	if rec := get("/headers?from=0"); rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&def) != nil ||
		len(def.Headers) != 4 {
		t.Fatalf("varsayılan sayfa %d, %d başlık", rec.Code, len(def.Headers))
	}

	zero := "/header?hash=" + strings.Repeat("0", 64)
	for url, want := range map[string]int{
		zero:                        http.StatusNotFound,
		"/header?hash=zz":           http.StatusBadRequest,
		"/headers":                  http.StatusBadRequest,
		"/headers?from=-1":          http.StatusBadRequest,
		"/headers?from=x":           http.StatusBadRequest,
		"/headers?from=0&count=0":   http.StatusBadRequest,
		"/headers?from=0&count=501": http.StatusBadRequest,
		"/headers?from=0&count=1e9": http.StatusBadRequest,
	} {
		if rec := get(url); rec.Code != want {
			t.Errorf("%s = %d, beklenen %d", url, rec.Code, want)
		}
	}
}
