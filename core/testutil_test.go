package core

import (
	"math/big"
	"testing"

	"quantum-bitcoin/crypto"
)

// easierThanLimitBits powLimit'ten kolay (kural dışı) bir hedeftir: 2^255.
func easierThanLimitBits() uint32 {
	return bigToCompact(new(big.Int).Lsh(big.NewInt(1), 255))
}

const testDifficulty = 1

// newTestChain geçici dizinde, sabit saatli ve düşük zorluklu bir zincir açar.
func newTestChain(t *testing.T) *Blockchain {
	t.Helper()
	bc, err := NewBlockchain(t.TempDir(), testDifficulty)
	if err != nil {
		t.Fatal(err)
	}
	clock := int64(1_900_000_000)
	bc.now = func() int64 { clock++; return clock }
	t.Cleanup(func() { bc.Close() })
	return bc
}

func newKey(t *testing.T) *crypto.KeyPair {
	t.Helper()
	kp, err := crypto.GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

// fund madenciye n blok kazdırarak n*BlockReward verir.
func fund(t *testing.T, bc *Blockchain, kp *crypto.KeyPair, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := bc.AddBlock(nil, kp.Address); err != nil {
			t.Fatal(err)
		}
	}
}

func signedTx(t *testing.T, from *crypto.KeyPair, to string, amount int64, nonce uint64) Transaction {
	t.Helper()
	return signedTxFee(t, from, to, amount, 0, nonce)
}

func signedTxFee(t *testing.T, from *crypto.KeyPair, to string, amount, fee int64, nonce uint64) Transaction {
	t.Helper()
	tx := Transaction{Receiver: to, Amount: amount, Fee: fee, Timestamp: 1, Nonce: nonce}
	if err := tx.Sign(from); err != nil {
		t.Fatal(err)
	}
	return tx
}

// craftBlock zincir ucunun üzerine verilen işlemlerle (coinbase dahil) bir blok kazar;
// doğrulamadan geçmeyen blokları denemek için kullanılır.
func craftBlock(t *testing.T, bc *Blockchain, txs []Transaction) *Block {
	t.Helper()
	prev, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		Index:        prev.Index + 1,
		Timestamp:    bc.now(),
		Transactions: txs,
		PrevHash:     prev.Hash,
	}
	mineAt(t, bc, b)
	return b
}

// regrind başlık alanlarını değiştirmeden (MineBlock'un aksine Version/Bits/MerkleRoot'a
// dokunmadan) yalnızca nonce'u, özet bloğun Bits hedefini karşılayana kadar artırır.
func regrind(b *Block) {
	for b.Nonce = 0; ; b.Nonce++ {
		b.Hash = b.CalculateHash()
		if MeetsTarget(b.Hash, b.Bits) {
			return
		}
	}
}

// mineAt coinbase nonce'u boşsa bloğun yüksekliğine ayarlar ve bloğu zincir hedefiyle kazar.
func mineAt(t *testing.T, bc *Blockchain, b *Block) {
	t.Helper()
	if len(b.Transactions) > 0 && b.Transactions[0].IsCoinbase && b.Transactions[0].Nonce == 0 {
		b.Transactions[0].Nonce = uint64(b.Index)
	}
	if err := b.MineBlock(bc.Bits()); err != nil {
		t.Fatal(err)
	}
}

func coinbaseTo(addr string) Transaction {
	return Transaction{Sender: CoinbaseSender, Receiver: addr, Amount: BlockReward, IsCoinbase: true}
}

// coinbaseWithFees ödül + verilen işlemlerin ücretleri kadar coinbase üretir.
func coinbaseWithFees(addr string, txs ...Transaction) Transaction {
	cb := coinbaseTo(addr)
	for _, tx := range txs {
		cb.Amount += tx.Fee
	}
	return cb
}

func mustBalance(t *testing.T, bc *Blockchain, addr string) int64 {
	t.Helper()
	b, err := bc.GetBalance(addr)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
