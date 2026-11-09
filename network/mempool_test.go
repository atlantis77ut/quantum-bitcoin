package network

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// Uçtan uca: cüzdan /txpolicy ile nonce ve süreyi alır, işlemi POST /tx ile gönderir,
// madenci havuzdan şablon kurar ve alıcının bakiyesi değişir.
func TestSubmitTxMinedByMiner(t *testing.T) {
	bc := newChain(t)
	alice, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	bob, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock(nil, alice.Address); err != nil {
		t.Fatal(err)
	}
	mineN(t, bc, int(core.RegtestParams.CoinbaseMaturity)) // ödül olgunlaşsın

	if rec := postTx(NewAPIServer(":0", bc).Handler(), []byte("{}")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("havuzsuz API: %d, beklenen 503", rec.Code)
	}

	ps := NewP2PServer("127.0.0.1:0", bc)
	api := NewAPIServer(":0", bc)
	api.Mempool = ps.Mempool
	h := api.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/txpolicy?address="+alice.Address, nil))
	var pol struct {
		NextNonce uint64 `json:"next_nonce"`
		MinFee    int64  `json:"min_fee"`
		Expiry    int64  `json:"default_expiry_height"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&pol); err != nil || rec.Code != http.StatusOK || pol.NextNonce != 1 {
		t.Fatalf("politika %d %+v, %v", rec.Code, pol, err)
	}

	tx := core.Transaction{Receiver: bob.Address, Amount: core.Coin, Fee: pol.MinFee, ExpiryHeight: pol.Expiry, Nonce: pol.NextNonce}
	if err := tx.Sign(alice, core.RegtestParams); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	rec = postTx(h, body)
	var res struct {
		TxID string `json:"txid"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil || rec.Code != http.StatusOK || res.TxID != tx.ID() {
		t.Fatalf("gönderim %d %+v, %v", rec.Code, res, err)
	}
	for _, c := range []struct {
		name string
		body []byte
		want int
	}{
		{"yinelenen işlem", body, http.StatusConflict},
		{"bilinmeyen alan", []byte(`{"amount":1,"extra":1}`), http.StatusBadRequest},
		{"iki işlem", append(append([]byte{}, body...), body...), http.StatusBadRequest},
		{"bozuk JSON", []byte("{"), http.StatusBadRequest},
	} {
		if rec := postTx(h, c.body); rec.Code != c.want {
			t.Fatalf("%s: %d, beklenen %d", c.name, rec.Code, c.want)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/tx", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /tx: %d", rec.Code)
	}

	m, err := NewMiner(ps, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	runMiner(t, m)
	deadline := time.Now().Add(20 * time.Second)
	for {
		bal, err := bc.GetBalance(bob.Address)
		if err != nil {
			t.Fatal(err)
		}
		if bal == core.Coin {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("işlem kazılmadı: alıcı bakiyesi %d", bal)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postTx(h http.Handler, body []byte) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/tx", bytes.NewReader(body)))
	return rec
}
