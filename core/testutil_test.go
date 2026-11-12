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

// testParams testlerin ağıdır: en kolay hedef, sabit genesis.
var testParams = RegtestParams

// testExpiry testlerde süre kuralına takılmayacak kadar uzak bir son kullanma yüksekliğidir.
const testExpiry int64 = 1 << 40

// testReward regtest'in ilk dönem ödülüdür. Bunu kullanan testler yükseklik
// HalvingInterval'a (150) ulaşmadan biter; craftBlock ve fund bunu denetler.
var testReward = RegtestParams.InitialSubsidy

// newTestChain geçici dizinde, sabit saatli ve düşük zorluklu bir zincir açar.
func newTestChain(t *testing.T) *Blockchain {
	t.Helper()
	bc, err := NewBlockchain(t.TempDir(), testParams)
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

// fund madenciye n blok kazdırarak n*testReward verir (ilk dönemde); ardından ödüller
// harcanabilir olsun diye olgunluk süresince başka bir adrese blok kazar.
func fund(t *testing.T, bc *Blockchain, kp *crypto.KeyPair, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		requireFirstEpoch(t, bc)
		if _, err := bc.AddBlock(nil, kp.Address); err != nil {
			t.Fatal(err)
		}
	}
	mature(t, bc)
}

// mature olgunluk süresince (CoinbaseMaturity blok) yeni bir adrese boş blok kazar: o ana
// kadarki bütün coinbase'ler harcanabilir olur.
func mature(t *testing.T, bc *Blockchain) {
	t.Helper()
	burn := newKey(t).Address
	for i := int64(0); i < bc.params.CoinbaseMaturity; i++ {
		requireFirstEpoch(t, bc)
		if _, err := bc.AddBlock(nil, burn); err != nil {
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
	tx := Transaction{Receiver: to, Amount: amount, Fee: fee, ExpiryHeight: testExpiry, Nonce: nonce}
	if err := tx.Sign(from, testParams); err != nil {
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
	requireFirstEpoch(t, bc)
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

// mineAt coinbase nonce'u boşsa bloğun yüksekliğine ayarlar ve bloğu ebeveynine göre
// beklenen hedefle (ebeveyn bilinmiyorsa ucun hedefiyle) kazar.
func mineAt(t *testing.T, bc *Blockchain, b *Block) {
	t.Helper()
	if len(b.Transactions) > 0 && b.Transactions[0].IsCoinbase && b.Transactions[0].Nonce == 0 {
		b.Transactions[0].Nonce = uint64(b.Index)
	}
	bits, err := bc.BitsAfter(b.PrevHash)
	if err != nil {
		bits = bc.Bits()
	}
	if err := b.MineBlock(bits); err != nil {
		t.Fatal(err)
	}
}

// requireFirstEpoch, testReward varsayan yardımcıların ilk yarılanmayı geçmesini engeller.
func requireFirstEpoch(t *testing.T, bc *Blockchain) {
	t.Helper()
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	if bc.params == RegtestParams && tip.Index+1 >= RegtestParams.HalvingInterval {
		t.Fatal("testReward ilk dönem içindir; bu test BlockSubsidy kullanmalı")
	}
}

func coinbaseTo(addr string) Transaction {
	return Transaction{Sender: CoinbaseSender, Receiver: addr, Amount: testReward, IsCoinbase: true}
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
