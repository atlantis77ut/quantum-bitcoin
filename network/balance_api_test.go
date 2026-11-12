package network

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quantum-bitcoin/core"
)

type balanceResp struct {
	Balance           int64  `json:"balance"`
	TipHeight         int64  `json:"tip_height"`
	TipHash           string `json:"tip_hash"`
	Immature          int64  `json:"immature"`
	ImmatureQBTC      string `json:"immature_qbtc"`
	ImmatureCoinbases []struct {
		Height          int64  `json:"height"`
		BlockHash       string `json:"block_hash"`
		Amount          int64  `json:"amount"`
		MaturesAt       int64  `json:"matures_at"`
		SpendableInPool bool   `json:"spendable_in_pool"`
	} `json:"immature_coinbases"`
	PendingOutgoing *int64 `json:"pending_outgoing"`
	PendingIncoming *int64 `json:"pending_incoming"`
	Spendable       *int64 `json:"spendable"`
}

func getBalance(t *testing.T, h http.Handler, addr string) balanceResp {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/balance?address="+addr, nil))
	var got balanceResp
	if rec.Code != http.StatusOK || json.NewDecoder(rec.Body).Decode(&got) != nil {
		t.Fatalf("bakiye = %d %s", rec.Code, rec.Body.String())
	}
	return got
}

// /balance olgunlaşmamış coinbase'leri listeler; havuz açıkken bekleyen giden/gelen ve
// harcanabilir tutarı ekler, kapalıyken bu alanları vermez.
func TestBalanceEndpointImmatureAndPending(t *testing.T) {
	bc := newChain(t)
	from := fundKey(t, bc)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	miner := tip.Transactions[0].Receiver

	plain := NewAPIServer(":0", bc).Handler()
	got := getBalance(t, plain, miner)
	if got.PendingOutgoing != nil || got.Spendable != nil {
		t.Fatalf("havuz kapalıyken bekleyen alanlar var: %+v", got)
	}
	// fundKey'in son CoinbaseMaturity bloğunu aynı madenci kazdı: hepsi olgunlaşmamış.
	var sum int64
	for _, c := range got.ImmatureCoinbases {
		sum += c.Amount
	}
	if got.TipHeight != tip.Index || got.TipHash != tip.Hash || got.Immature != sum ||
		int64(len(got.ImmatureCoinbases)) != core.RegtestParams.CoinbaseMaturity ||
		!got.ImmatureCoinbases[0].SpendableInPool {
		t.Fatalf("olgunlaşmamış: %+v", got)
	}
	last := got.ImmatureCoinbases[len(got.ImmatureCoinbases)-1]
	if last.Height != tip.Index || last.BlockHash != tip.Hash || last.MaturesAt != tip.Index+core.RegtestParams.CoinbaseMaturity {
		t.Fatalf("son kayıt %+v", last)
	}

	ps := NewP2PServer(":0", bc)
	api := NewAPIServer(":0", bc)
	api.Mempool = ps.Mempool
	h := api.Handler()
	tx := policyTx(t, ps.Mempool, from, 7)
	if _, err := ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	got = getBalance(t, h, from.Address)
	if got.PendingOutgoing == nil || *got.PendingOutgoing != 7+tx.Fee ||
		got.Spendable == nil || *got.Spendable != got.Balance-7-tx.Fee || got.Immature != 0 {
		t.Fatalf("gönderen: %+v", got)
	}
	got = getBalance(t, h, tx.Receiver)
	if got.PendingIncoming == nil || *got.PendingIncoming != 7 || got.Spendable == nil || *got.Spendable != 0 {
		t.Fatalf("alıcı: %+v", got)
	}
}
