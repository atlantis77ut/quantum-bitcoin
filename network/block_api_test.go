package network

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"quantum-bitcoin/core"
)

type blockPage struct {
	Header        core.BlockHeader `json:"header"`
	Confirmations int64            `json:"confirmations"`
	TxCount       int              `json:"tx_count"`
	Offset        int              `json:"offset"`
	Transactions  []blockTx        `json:"transactions"`
}

func TestBlockEndpoint(t *testing.T) {
	bc := newChain(t)
	from := fundKey(t, bc)
	ps := NewP2PServer(":0", bc)
	var sent []core.Transaction
	for i := 0; i < 3; i++ {
		tx := policyTx(t, ps.Mempool, from, int64(i+1))
		if _, err := ps.Mempool.Add(tx); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, tx)
	}
	txs, err := ps.Mempool.Select(core.MaxTxPerBlock)
	if err != nil || len(txs) != 3 {
		t.Fatalf("havuz seçimi: %d %v", len(txs), err)
	}
	blk, err := bc.AddBlock(txs, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, 2)

	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec
	}
	page := func(url string) blockPage {
		t.Helper()
		var p blockPage
		rec := get(url)
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&p) != nil {
			t.Fatalf("%s = %d %s", url, rec.Code, rec.Body.String())
		}
		return p
	}
	byHash := "/block?hash=" + blk.Hash

	// Tam blok: başlık, onay, coinbase + 3 işlem, sıra ve kimlikler bloktakiyle aynı.
	p := page(byHash)
	if p.Header.Hash != blk.Hash || p.Header.Index != blk.Index || p.Header.MerkleRoot != blk.MerkleRoot ||
		p.Confirmations != 3 || p.TxCount != 4 || p.Offset != 0 || len(p.Transactions) != 4 {
		t.Fatalf("blok sayfası %+v", p)
	}
	for i, tx := range p.Transactions {
		if tx.Index != i || tx.TxID != blk.Transactions[i].ID() || tx.Transaction.ID() != tx.TxID {
			t.Fatalf("işlem %d: sıra %d, kimlik %s", i, tx.Index, tx.TxID)
		}
	}
	if !p.Transactions[0].IsCoinbase || p.Transactions[1].Sender != from.Address {
		t.Fatalf("işlem sırası beklenmiyor: %+v", p.Transactions[0].Transaction)
	}

	// Yükseklikle aynı blok; sayfalama.
	if q := page(fmt.Sprintf("/block?height=%d&offset=1&limit=2", blk.Index)); q.Header.Hash != blk.Hash ||
		q.TxCount != 4 || q.Offset != 1 || len(q.Transactions) != 2 || q.Transactions[0].Index != 1 ||
		q.Transactions[1].Index != 2 {
		t.Fatalf("sayfa %+v", q)
	}
	if rec := get(byHash + "&offset=4"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"transactions":[]`) {
		t.Fatalf("sondan sonraki sayfa %d %s", rec.Code, rec.Body.String())
	}
	if q := page("/block?height=0"); q.Header.Index != 0 || q.Confirmations != bc.TipHeader().Index+1 {
		t.Fatalf("genesis %+v", q.Header)
	}

	// txid ile tek işlem.
	want := sent[1].ID()
	if q := page(byHash + "&txid=" + want); len(q.Transactions) != 1 || q.Transactions[0].TxID != want ||
		q.Transactions[0].Index != 2 || q.Offset != 2 || q.Transactions[0].Amount != 2 {
		t.Fatalf("txid sorgusu %+v", q)
	}

	// Oran sınırı sunucu başınadır: hata durumları taze bir sunucuyla denenir ki istek sayısı
	// ani istek sınırına takılmasın.
	h = NewAPIServer(":0", bc).Handler()
	zero := strings.Repeat("0", 64)
	for _, c := range []struct {
		url  string
		code int
	}{
		{"/block", http.StatusBadRequest},
		{byHash + "&height=1", http.StatusBadRequest},
		{"/block?hash=zz", http.StatusBadRequest},
		{"/block?hash=" + zero, http.StatusNotFound},
		{"/block?height=-1", http.StatusBadRequest},
		{"/block?height=x", http.StatusBadRequest},
		{"/block?height=999999", http.StatusNotFound},
		{"/block?height=9223372036854775807", http.StatusNotFound},
		{byHash + "&offset=-1", http.StatusBadRequest},
		{byHash + "&limit=0", http.StatusBadRequest},
		{byHash + "&limit=201", http.StatusBadRequest},
		{byHash + "&txid=zz", http.StatusBadRequest},
		{byHash + "&txid=" + zero, http.StatusNotFound},
		{byHash + "&txid=" + want + "&limit=5", http.StatusBadRequest},
	} {
		if rec := get(c.url); rec.Code != c.code {
			t.Errorf("%s = %d, beklenen %d", c.url, rec.Code, c.code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", byHash, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /block = %d", rec.Code)
	}
}

// /latestblock uçtaki bloğu /block biçiminde, sayfalı döndürür; hash/height kabul etmez.
func TestLatestBlockEndpoint(t *testing.T) {
	bc := newChain(t)
	from := fundKey(t, bc)
	ps := NewP2PServer(":0", bc)
	var sent []core.Transaction
	for i := 0; i < 3; i++ {
		tx := policyTx(t, ps.Mempool, from, int64(i+1))
		if _, err := ps.Mempool.Add(tx); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, tx)
	}
	txs, err := ps.Mempool.Select(core.MaxTxPerBlock)
	if err != nil || len(txs) != 3 {
		t.Fatalf("havuz seçimi: %d %v", len(txs), err)
	}
	blk, err := bc.AddBlock(txs, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}

	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec
	}
	page := func(url string) blockPage {
		t.Helper()
		var p blockPage
		rec := get(url)
		if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&p) != nil {
			t.Fatalf("%s = %d %s", url, rec.Code, rec.Body.String())
		}
		return p
	}

	p := page("/latestblock")
	if p.Header.Hash != blk.Hash || p.Header.Index != blk.Index || p.Confirmations != 1 || p.TxCount != 4 ||
		p.Offset != 0 || len(p.Transactions) != 4 || p.Transactions[0].TxID != blk.Transactions[0].ID() {
		t.Fatalf("son blok sayfası %+v", p)
	}
	if q := page("/latestblock?offset=1&limit=2"); q.Header.Hash != blk.Hash || q.Offset != 1 ||
		len(q.Transactions) != 2 || q.Transactions[0].Index != 1 || q.Transactions[1].Index != 2 {
		t.Fatalf("sayfa %+v", q)
	}
	want := sent[2].ID()
	if q := page("/latestblock?txid=" + want); len(q.Transactions) != 1 || q.Transactions[0].TxID != want ||
		q.Transactions[0].Index != 3 {
		t.Fatalf("txid sorgusu %+v", q)
	}
	for _, url := range []string{
		"/latestblock?height=1",
		"/latestblock?hash=" + blk.Hash,
		"/latestblock?limit=0",
		"/latestblock?limit=201",
		"/latestblock?offset=-1",
	} {
		if rec := get(url); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, beklenen 400", url, rec.Code)
		}
	}
	mineN(t, bc, 1) // uç ilerleyince yeni uç döner
	if q := page("/latestblock"); q.Header.Index != blk.Index+1 || q.Confirmations != 1 || q.TxCount != 1 {
		t.Fatalf("yeni uç %+v", q.Header)
	}
}
