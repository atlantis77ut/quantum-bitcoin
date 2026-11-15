package network

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

func decodeHash32(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("hex %q", s)
	}
	var h [32]byte
	copy(h[:], b)
	return h
}

// Üç işlemli (dengesiz ağaç) bloktaki her işlemin kanıtı, kökü /header'dan alınarak doğrulanır.
func TestMerkleProofEndpoint(t *testing.T) {
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
	var txs []core.Transaction
	for nonce := uint64(1); nonce <= 2; nonce++ {
		tx := core.Transaction{Receiver: bob.Address, Amount: core.Coin, ExpiryHeight: 1_000_000, Nonce: nonce}
		if err := tx.Sign(alice, core.RegtestParams); err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	blk, err := bc.AddBlock(txs, bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(blk.Transactions) != 3 {
		t.Fatalf("blokta %d işlem", len(blk.Transactions))
	}
	h := NewAPIServer(":0", bc).Handler()
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec
	}

	rec := get("/header?hash=" + blk.Hash)
	var hdr struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&hdr); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("başlık %d, %v", rec.Code, err)
	}
	root := decodeHash32(t, hdr.MerkleRoot)

	for i, tx := range blk.Transactions {
		rec := get("/merkleproof?id=" + tx.ID())
		var body struct {
			TxID          string   `json:"txid"`
			BlockHash     string   `json:"block_hash"`
			Height        int64    `json:"height"`
			Index         int      `json:"index"`
			TxCount       int      `json:"tx_count"`
			MerkleRoot    string   `json:"merkle_root"`
			Proof         []string `json:"proof"`
			Confirmations int64    `json:"confirmations"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("sıra %d: %d, %v", i, rec.Code, err)
		}
		if body.BlockHash != blk.Hash || body.Height != blk.Index || body.Index != i || body.TxCount != 3 ||
			body.Confirmations != 1 || body.MerkleRoot != hdr.MerkleRoot {
			t.Fatalf("sıra %d: yanıt %+v", i, body)
		}
		proof := make([][32]byte, len(body.Proof))
		for j, s := range body.Proof {
			proof[j] = decodeHash32(t, s)
		}
		if !core.VerifyMerkleProof(decodeHash32(t, body.TxID), body.Index, body.TxCount, proof, root) {
			t.Fatalf("sıra %d: kanıt başlığın köküne çıkmadı", i)
		}
	}

	zero := hex.EncodeToString(make([]byte, 32))
	for url, want := range map[string]int{
		"/merkleproof":            http.StatusBadRequest,
		"/merkleproof?id=zz":      http.StatusBadRequest,
		"/merkleproof?id=" + zero: http.StatusNotFound,
	} {
		if rec := get(url); rec.Code != want {
			t.Fatalf("%s = %d, beklenen %d", url, rec.Code, want)
		}
	}
}
