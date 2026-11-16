package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

// Hafif istemci yolu bağımsız vektörleri (txid_test.go) dışa açık fonksiyonlarla üretir.
func TestTxBodyVectors(t *testing.T) {
	tx := Transaction{
		Sender: "QBTC" + hexRepeat("11", 20), Receiver: "QBTC" + hexRepeat("22", 20),
		Amount: 100_000_000, Fee: 1000, ExpiryHeight: 500, Nonce: 7,
		PublicKey: bytes.Repeat([]byte{1}, 1952), Signature: bytes.Repeat([]byte{2}, 3309),
	}
	bh := sha256.Sum256(tx.BodyBytes())
	wh := tx.WitnessHash()
	if got := hex.EncodeToString(bh[:]); got != "25baefa080bc98071d6c2c37ab8ea6f262109b933b65cd53d055728335e9b1b4" {
		t.Fatalf("gövde özeti %s", got)
	}
	if got := hex.EncodeToString(wh[:]); got != "32622eed00a269bafcaba302f679c97f8b1c25629b834218eca8f28a2114374a" {
		t.Fatalf("tanık özeti %s", got)
	}
	id := TxIDFromParts(tx.BodyBytes(), wh)
	if got := hex.EncodeToString(id[:]); got != "576c82e0f458fa7d01b77961a42c11d0c327d4ceb1ca75d32b01e84c2d3ee4b0" {
		t.Fatalf("kimlik %s", got)
	}
}

// Gövde çözülüp yeniden kodlandığında aynı baytlar ve aynı kimlik çıkar: imzalı işlem, taahhütlü
// coinbase ve regtest genesis coinbase'i.
func TestDecodeTxBodyRoundTrip(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	signed := signedTx(t, alice, bob.Address, Coin, 1)
	commit := make([]byte, CoinbaseCommitmentSize)
	for i := range commit {
		commit[i] = byte(i)
	}
	cb := Transaction{Sender: CoinbaseSender, Receiver: "QBTC" + hexRepeat("33", 20), Amount: 500_000_000,
		Nonce: 12, IsCoinbase: true, Commitment: commit}
	g, err := NewGenesisBlock(RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	for name, tx := range map[string]Transaction{"imzalı": signed, "coinbase": cb, "genesis": g.Transactions[0]} {
		body := tx.BodyBytes()
		if id := TxIDFromParts(body, tx.WitnessHash()); id != tx.IDBytes() {
			t.Fatalf("%s: parçalardan kimlik farklı", name)
		}
		dec, err := DecodeTxBody(body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if dec.Sender != tx.Sender || dec.Receiver != tx.Receiver || dec.Amount != tx.Amount || dec.Fee != tx.Fee ||
			dec.ExpiryHeight != tx.ExpiryHeight || dec.Nonce != tx.Nonce || dec.IsCoinbase != tx.IsCoinbase ||
			!bytes.Equal(dec.Commitment, tx.Commitment) || dec.PublicKey != nil || dec.Signature != nil {
			t.Fatalf("%s: çözülen %+v, beklenen %+v", name, dec, tx)
		}
		if !bytes.Equal(dec.BodyBytes(), body) {
			t.Fatalf("%s: yeniden kodlama farklı", name)
		}
	}
}

// Kanonik olmayan ya da kesik gövdeler reddedilir.
func TestDecodeTxBodyRejects(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	good := tx.BodyBytes()
	field := func(b []byte) []byte {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(b)))
		return append(l[:], b...)
	}
	build := func(fs ...[]byte) []byte {
		var out []byte
		for _, f := range fs {
			out = append(out, field(f)...)
		}
		return out
	}
	u := func(v uint64) []byte {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], v)
		return n[:]
	}
	s, r := []byte(tx.Sender), []byte(tx.Receiver)
	huge := append([]byte(nil), good...)
	binary.BigEndian.PutUint32(huge[:4], 0xffffffff)
	cases := map[string][]byte{
		"boş":           nil,
		"kesik":         good[:len(good)-1],
		"kesik önek":    good[:2],
		"artık bayt":    append(append([]byte(nil), good...), 0),
		"fazla alan":    append(append([]byte(nil), good...), field(nil)...),
		"eksik alan":    build(s, r, u(1), u(0), u(5), u(1), []byte{0}),
		"kısa tamsayı":  build(s, r, []byte{1}, u(0), u(5), u(1), []byte{0}, nil),
		"uzun tamsayı":  build(s, r, append(u(1), 0), u(0), u(5), u(1), []byte{0}, nil),
		"işaret 2":      build(s, r, u(1), u(0), u(5), u(1), []byte{2}, nil),
		"işaret 2 bayt": build(s, r, u(1), u(0), u(5), u(1), []byte{0, 0}, nil),
		"aşırı uzunluk": huge,
	}
	for name, b := range cases {
		if _, err := DecodeTxBody(b); !errors.Is(err, ErrBadTxBody) {
			t.Fatalf("%s: hata %v, beklenen ErrBadTxBody", name, err)
		}
	}
	if _, err := DecodeTxBody(build(s, r, u(1), u(0), u(5), u(1), []byte{0}, nil)); err != nil {
		t.Fatalf("elle kurulan geçerli gövde: %v", err)
	}
}
