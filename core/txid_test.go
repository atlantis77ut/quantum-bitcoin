package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// Bağımsız test vektörleri (Python hashlib ile transaction.go'daki düzen yorumundan, Go
// kodundan ayrı yazıldı; aynı betik eski v2 genesis'ini de birebir üretti). Tanık yolunu
// (açık anahtar ve imzanın uzunluk önekleri ve sırası), v5 imza mesajını ve coinbase taahhüt
// yuvasının içeriğini sabitler: genesis vektörleri yalnızca boş tanıklı, sıfır taahhütlü
// coinbase'i kapsar.
func TestTxIDVectors(t *testing.T) {
	tx := Transaction{
		Sender: "QBTC" + hexRepeat("11", 20), Receiver: "QBTC" + hexRepeat("22", 20),
		Amount: 100_000_000, Fee: 1000, ExpiryHeight: 500, Nonce: 7,
		PublicKey: bytes.Repeat([]byte{1}, 1952), Signature: bytes.Repeat([]byte{2}, 3309),
	}
	check := func(name, got, want string) {
		t.Helper()
		if got != want {
			t.Fatalf("%s %s, beklenen %s", name, got, want)
		}
	}
	bh, wh := tx.bodyHash(), tx.witnessHash()
	check("gövde özeti", hex.EncodeToString(bh[:]), "25baefa080bc98071d6c2c37ab8ea6f262109b933b65cd53d055728335e9b1b4")
	check("tanık özeti", hex.EncodeToString(wh[:]), "32622eed00a269bafcaba302f679c97f8b1c25629b834218eca8f28a2114374a")
	check("kimlik", tx.ID(), "576c82e0f458fa7d01b77961a42c11d0c327d4ceb1ca75d32b01e84c2d3ee4b0")
	dom, err := TestnetParams.SigDomain()
	if err != nil {
		t.Fatal(err)
	}
	sb := sha256.Sum256(tx.SigningBytes(dom))
	check("imzalanan mesaj", hex.EncodeToString(sb[:]), "079eb646962be955d34c7f60e7958558eef5aca33f0e593a9bdb50fc7ca4c1e5")

	commit := make([]byte, CoinbaseCommitmentSize)
	for i := range commit {
		commit[i] = byte(i)
	}
	cb := Transaction{Sender: CoinbaseSender, Receiver: "QBTC" + hexRepeat("33", 20), Amount: 500_000_000,
		Nonce: 12, IsCoinbase: true, Commitment: commit}
	check("taahhütlü coinbase kimliği", cb.ID(), "3ce39b18c06778e08835b05ed819c4e57174d4cf1cc1bc98e77ca98f5734ab50")
}

func hexRepeat(s string, n int) string {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.WriteString(s)
	}
	return b.String()
}

// Kimlik imzayı ve açık anahtarı kapsar, imzalanan mesaj kapsamaz (tanık ayrı özetlenir).
func TestTxIDCoversWitness(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	dom, err := testParams.SigDomain()
	if err != nil {
		t.Fatal(err)
	}
	id, msg := tx.ID(), string(tx.SigningBytes(dom))
	for _, m := range []struct {
		name string
		mut  func(*Transaction)
	}{
		{"imza", func(c *Transaction) { c.Signature[0] ^= 1 }},
		{"açık anahtar", func(c *Transaction) { c.PublicKey[0] ^= 1 }},
	} {
		name := m.name
		c := tx
		c.Signature = append([]byte(nil), tx.Signature...)
		c.PublicKey = append([]byte(nil), tx.PublicKey...)
		m.mut(&c)
		if c.ID() == id {
			t.Fatalf("%s değişti, kimlik aynı kaldı", name)
		}
		if string(c.SigningBytes(dom)) != msg {
			t.Fatalf("%s imzalanan mesajı değiştirdi", name)
		}
	}
}

// Coinbase taahhüdü tam 32 bayt olmalı, içeriği denetlenmez (ileride yumuşak çatalla bağlanır).
func TestCoinbaseCommitmentRule(t *testing.T) {
	for _, c := range []struct {
		name string
		n    int
		ok   bool
	}{
		{"boş", 0, false},
		{"31 bayt", 31, false},
		{"33 bayt", 33, false},
		{"sıfır olmayan 32 bayt", 32, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			bc := newTestChain(t)
			cb := coinbaseTo(newKey(t).Address)
			cb.Commitment = make([]byte, c.n)
			for i := range cb.Commitment {
				cb.Commitment[i] = byte(0xa0 + i)
			}
			err := bc.AcceptBlock(craftBlock(t, bc, []Transaction{cb}))
			if c.ok && err != nil || !c.ok && !errors.Is(err, ErrBadCoinbase) {
				t.Fatalf("hata %v", err)
			}
		})
	}
}

// Coinbase dışı işlemde taahhüt yuvası boş olmalı: hem durumsuz doğrulama (havuz yolu) hem blok
// doğrulaması reddeder.
func TestNonCoinbaseCommitmentRejected(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	// Biçim kontrolü bakiyeden önce çalışır; gönderenin fonu gerekmez.
	tx := Transaction{Receiver: bob.Address, Amount: Coin, ExpiryHeight: testExpiry, Nonce: 1,
		Commitment: make([]byte, CoinbaseCommitmentSize)}
	if err := tx.Sign(alice, testParams); err != nil {
		t.Fatal(err)
	}
	if err := tx.VerifyStateless(testParams); !errors.Is(err, ErrUnexpectedCommit) {
		t.Fatalf("durumsuz doğrulama: %v", err)
	}
	err := bc.AcceptBlock(craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx}))
	if !errors.Is(err, ErrUnexpectedCommit) {
		t.Fatalf("blok: %v", err)
	}
}
