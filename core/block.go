package core

import (
	"time"
)

// Uzlaşma (consensus) parametreleri. Değiştirmeden önce kullanıcıya sorulmalı.
const (
	Coin           int64 = 100_000_000     // 1 QBTC = 10^8 taban birim; tüm tutarlar taban birim cinsindendir
	BlockReward    int64 = 50 * Coin       // Blok ödülü (emisyon takvimi ayrı bir uzlaşma kararıdır)
	CoinbaseSender       = "SYSTEM_REWARD" // Ödül işlemlerinin göndereni
	MaxTxPerBlock        = 1000            // Coinbase hariç blok başına en fazla işlem (DoS sınırı)
	MaxFutureDrift       = 2 * 60 * 60     // Saniye: blok zamanı yerel saatten en fazla bu kadar ileride olabilir
	MedianTimeSpan       = 11              // Blok zamanı son N bloğun medyanından büyük olmalı
)

// Block başlık alanları (Version..Nonce) ve işlemlerden oluşur. Hash yalnızca sabit
// boyutlu başlığın özetidir (bkz. header.go); işlemler MerkleRoot ile bağlanır.
type Block struct {
	Version      uint32        `json:"version"`
	Index        int64         `json:"index"`
	Timestamp    int64         `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
	PrevHash     string        `json:"prev_hash"`
	MerkleRoot   string        `json:"merkle_root"`
	Bits         uint32        `json:"bits"`
	Nonce        uint64        `json:"nonce"`
	Hash         string        `json:"hash"`
}

// NewGenesisBlock ilk bloğu oluşturur.
// DİKKAT: Zaman damgası time.Now() olduğu için her düğüm farklı bir genesis üretir;
// ağdaki düğümler ortak zincire ancak sabit bir genesis ile anlaşabilir (genesis PR'ı).
// Genesis PoW doğrulamasından geçmez; yerel ve güvenilir kaynak olarak yazılır.
func NewGenesisBlock() *Block {
	genesisTx := Transaction{
		Sender:     "SYSTEM",
		Receiver:   "QBTC_SATOSHI_QUANTUM",
		Amount:     21_000_000 * Coin, // İlk can suyu arzı; premine genesis PR'ında kaldırılacak (kullanıcı kararı)
		Timestamp:  time.Now().Unix(),
		IsCoinbase: true,
	}

	block := &Block{
		Version:      BlockVersion,
		Index:        0,
		Timestamp:    time.Now().Unix(),
		Transactions: []Transaction{genesisTx},
		PrevHash:     "0000000000000000000000000000000000000000000000000000000000000000",
		Bits:         bigToCompact(powLimit),
	}
	block.MerkleRoot = block.ComputeMerkleRoot()
	block.Hash = block.CalculateHash()
	return block
}
