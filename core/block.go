package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Transaction struct {
	Sender    string `json:"sender"`
	Receiver  string `json:"receiver"`
	Amount    int64  `json:"amount"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
	IsCoinbase bool   `json:"is_coinbase"` // Sistemin ödül olarak bastığı para mı?
}

type Block struct {
	Index        int64         `json:"index"`
	Timestamp    int64         `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
	PrevHash     string        `json:"prev_hash"`
	Hash         string        `json:"hash"`
	Nonce        int64         `json:"nonce"`
	Difficulty   int           `json:"difficulty"`
}

func (b *Block) CalculateHash() string {
	data, _ := json.Marshal(b.Transactions)
	record := fmt.Sprintf("%d%d%s%s%d%d", b.Index, b.Timestamp, string(data), b.PrevHash, b.Nonce, b.Difficulty)
	
	firstHash := sha256.Sum256([]byte(record))
	secondHash := sha256.Sum256(firstHash[:])
	
	return fmt.Sprintf("%x", secondHash)
}

func (b *Block) MineBlock(difficulty int) {
	b.Difficulty = difficulty
	target := strings.Repeat("0", difficulty)

	fmt.Printf("[...] Blok #%d kazılıyor (Zorluk: %d)... \n", b.Index, difficulty)
	startTime := time.Now()

	for {
		b.Hash = b.CalculateHash()
		if strings.HasPrefix(b.Hash, target) {
			break
		}
		b.Nonce++
	}

	elapsed := time.Since(startTime)
	fmt.Printf("[✓] Blok Kazıldı! Nonce: %d | Süre: %s | Hash: %s\n", b.Nonce, elapsed, b.Hash)
}

// NewGenesisBlock ilk bloğu oluşturur
func NewGenesisBlock() *Block {
	genesisTx := Transaction{
		Sender:     "SYSTEM",
		Receiver:   "QBTC_SATOSHI_QUANTUM",
		Amount:     21000000, // İlk can suyu arzı
		Timestamp:  time.Now().Unix(),
		IsCoinbase: true,
	}
	
	block := &Block{
		Index:        0,
		Timestamp:    time.Now().Unix(),
		Transactions: []Transaction{genesisTx},
		PrevHash:     "0000000000000000000000000000000000000000000000000000000000000000",
		Nonce:        0,
		Difficulty:   0,
	}
	block.Hash = block.CalculateHash()
	return block
}
