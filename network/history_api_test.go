package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quantum-bitcoin/core"
)

type historyResp struct {
	Address      string `json:"address"`
	TipHeight    int64  `json:"tip_height"`
	TipHash      string `json:"tip_hash"`
	Next         string `json:"next"`
	Transactions []struct {
		TxID          string `json:"txid"`
		BlockHash     string `json:"block_hash"`
		Height        int64  `json:"height"`
		Index         int    `json:"index"`
		Confirmations int64  `json:"confirmations"`
		Direction     string `json:"direction"`
		Amount        int64  `json:"amount"`
		Fee           int64  `json:"fee"`
		Counterparty  string `json:"counterparty"`
		Coinbase      bool   `json:"coinbase"`
	} `json:"transactions"`
}

// /history adresin ana zincirdeki işlemlerini yeniden eskiye sayfalı döndürür; geçersiz
// parametreler 400 olur.
func TestHistoryEndpoint(t *testing.T) {
	bc := newChain(t)
	from := fundKey(t, bc)
	ps := NewP2PServer(":0", bc)
	tx := policyTx(t, ps.Mempool, from, 7)
	if _, err := ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	txs, err := ps.Mempool.Select(core.MaxTxPerBlock)
	if err != nil || len(txs) != 1 {
		t.Fatalf("havuz seçimi: %d %v", len(txs), err)
	}
	blk, err := bc.AddBlock(txs, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}

	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) (int, historyResp) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		var got historyResp
		if rec.Code == http.StatusOK {
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, got
	}

	code, got := get("/history?address=" + from.Address + "&limit=1")
	if code != http.StatusOK || len(got.Transactions) != 1 || got.TipHash != blk.Hash || got.TipHeight != blk.Index {
		t.Fatalf("ilk sayfa = %d %+v", code, got)
	}
	out := got.Transactions[0]
	if out.TxID != tx.ID() || out.Direction != "out" || out.Amount != 7 || out.Fee != tx.Fee ||
		out.Counterparty != tx.Receiver || out.BlockHash != blk.Hash || out.Index != 1 || out.Confirmations != 1 ||
		got.Next != core.FormatAddrCursor(blk.Index, 1) {
		t.Fatalf("giden kayıt %+v, next %q", out, got.Next)
	}
	code, got = get("/history?address=" + from.Address + "&limit=1&before=" + got.Next)
	if code != http.StatusOK || len(got.Transactions) != 1 || got.Next != "" ||
		!got.Transactions[0].Coinbase || got.Transactions[0].Height != 1 || got.Transactions[0].Direction != "in" {
		t.Fatalf("ikinci sayfa = %d %+v", code, got)
	}
	code, got = get("/history?address=" + tx.Receiver)
	if code != http.StatusOK || len(got.Transactions) != 1 || got.Transactions[0].Direction != "in" ||
		got.Transactions[0].Fee != 0 || got.Transactions[0].Counterparty != from.Address {
		t.Fatalf("alıcı = %d %+v", code, got)
	}

	for _, c := range []struct {
		url  string
		code int
	}{
		{"/history", http.StatusBadRequest},
		{"/history?address=lh", http.StatusBadRequest},
		{"/history?address=" + from.Address + "&limit=0", http.StatusBadRequest},
		{"/history?address=" + from.Address + "&limit=201", http.StatusBadRequest},
		{"/history?address=" + from.Address + "&before=x", http.StatusBadRequest},
		{"/history?address=" + from.Address + "&before=5", http.StatusBadRequest},
	} {
		if code, _ := get(c.url); code != c.code {
			t.Errorf("%s = %d, beklenen %d", c.url, code, c.code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/history?address="+from.Address, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
}
