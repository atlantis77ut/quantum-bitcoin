package core

import (
	"errors"
	"testing"

	"quantum-bitcoin/crypto"
)

func signedTxExp(t *testing.T, from *crypto.KeyPair, to string, amount int64, nonce uint64, expiry int64) Transaction {
	t.Helper()
	tx := Transaction{Receiver: to, Amount: amount, ExpiryHeight: expiry, Nonce: nonce}
	if err := tx.Sign(from, testParams); err != nil {
		t.Fatal(err)
	}
	return tx
}

func tipBlock(t *testing.T, bc *Blockchain) *Block {
	t.Helper()
	b, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Kural kapsayıcıdır: ExpiryHeight yüksekliğindeki blokta geçerli, bir sonrakinde değil.
// Süresi dolmuş işlem içeren blok kalıcı olarak reddedilir (içeriğe bağlı).
func TestExpiryBoundary(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	h := tipBlock(t, bc).Index + 1
	mustAccept(t, bc, blockOn(t, bc, tipBlock(t, bc), miner.Address, signedTxExp(t, alice, bob.Address, Coin, 1, h)))

	late := blockOn(t, bc, tipBlock(t, bc), miner.Address, signedTxExp(t, alice, bob.Address, Coin, 2, h))
	if err := bc.AcceptBlock(late); !errors.Is(err, ErrTxExpired) {
		t.Fatalf("süresi dolmuş işlem: %v, beklenen ErrTxExpired", err)
	}
	if !bc.rejected.has(late.Hash) {
		t.Fatal("süresi dolmuş işlemli blok kalıcı ret önbelleğinde değil")
	}
	if got := mustBalance(t, bc, bob.Address); got != Coin {
		t.Fatalf("bob bakiyesi %d", got)
	}
}

// "Süresiz" değer yoktur: alanı eksik (0) ya da negatif işlem imzası doğru olsa da geçersizdir.
func TestExpiryMustBePositive(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	for _, e := range []int64{0, -1, -1 << 63} {
		tx := signedTxExp(t, alice, bob.Address, Coin, 1, e)
		if err := tx.VerifyStateless(testParams); !errors.Is(err, ErrBadExpiry) {
			t.Fatalf("E=%d: %v, beklenen ErrBadExpiry", e, err)
		}
		b := blockOn(t, bc, tipBlock(t, bc), miner.Address, tx)
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadExpiry) {
			t.Fatalf("E=%d blokta: %v", e, err)
		}
	}
}

// Son kullanma yüksekliği imzalıdır: imzadan sonra uzatılamaz.
func TestExpiryIsSigned(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	tx := signedTxExp(t, alice, bob.Address, Coin, 1, 10)
	tx.ExpiryHeight = 1 << 40
	if err := tx.VerifyStateless(testParams); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("uzatılmış süre: %v, beklenen ErrBadSignature", err)
	}
}

// Ucuz kontroller bütün işlemler için imzalardan önce yapılır: süresi dolmuş işlem, kendinden
// önceki işlemin imzası bozuk olsa bile önce yakalanır (imza doğrulaması yaptırılamaz).
func TestExpiryCheckedBeforeAnySignature(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	h := tipBlock(t, bc).Index + 1

	badSig := signedTxExp(t, alice, bob.Address, Coin, 1, testExpiry)
	badSig.Signature = flipFirst(badSig.Signature)
	expired := signedTxExp(t, alice, bob.Address, Coin, 2, h-1)
	b := blockOn(t, bc, tipBlock(t, bc), miner.Address, badSig, expired)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTxExpired) {
		t.Fatalf("hata %v, beklenen ErrTxExpired (imza geçişinden önce)", err)
	}
}

// Coinbase'in son kullanma alanı kanoniktir (0): madencinin elinde serbest alan kalmaz.
func TestCoinbaseExpiryMustBeZero(t *testing.T) {
	bc := newTestChain(t)
	parent := tipBlock(t, bc)
	h := parent.Index + 1
	cb := Transaction{Sender: CoinbaseSender, Receiver: newKey(t).Address, Amount: BlockSubsidy(h, bc.params),
		ExpiryHeight: h, IsCoinbase: true}
	b := &Block{Index: h, Timestamp: bc.now(), Transactions: []Transaction{cb}, PrevHash: parent.Hash}
	mineAt(t, bc, b)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadCoinbase) {
		t.Fatalf("coinbase E=%d: %v, beklenen ErrBadCoinbase", h, err)
	}
}

// Blok üretimi süresi dolmuş işlemi atlar; blok geri kalanıyla üretilir ve geçerlidir.
func TestAddBlockSkipsExpired(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	h := tipBlock(t, bc).Index + 1

	expired := signedTxExp(t, alice, bob.Address, 5*Coin, 1, h-1)
	valid := signedTxExp(t, alice, bob.Address, Coin, 1, h)
	b, err := bc.AddBlock([]Transaction{expired, valid}, miner.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Transactions) != 2 || b.Transactions[1].ID() != valid.ID() {
		t.Fatalf("blok %d işlem içeriyor, beklenen coinbase + geçerli işlem", len(b.Transactions))
	}
	if got := mustBalance(t, bc, bob.Address); got != Coin {
		t.Fatalf("bob bakiyesi %d", got)
	}
}
