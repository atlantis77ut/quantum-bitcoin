package core

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
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

type Block struct {
	Index        int64         `json:"index"`
	Timestamp    int64         `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
	PrevHash     string        `json:"prev_hash"`
	Hash         string        `json:"hash"`
	Nonce        int64         `json:"nonce"`
	Difficulty   int           `json:"difficulty"`
}

// CalculateHash blok başlığının çift SHA-256 özetini döndürür. Alanlar "|" ile
// ayrılır; böylece (Index=1, Timestamp=23) ile (Index=12, Timestamp=3) aynı veriyi üretmez.
func (b *Block) CalculateHash() string {
	data, err := json.Marshal(b.Transactions)
	if err != nil {
		// Bu tiplerle Marshal hata vermez; yine de boş özet hiçbir doğrulamadan geçmez.
		return ""
	}
	record := fmt.Sprintf("%d|%d|%s|%s|%d|%d", b.Index, b.Timestamp, data, b.PrevHash, b.Nonce, b.Difficulty)

	firstHash := sha256.Sum256([]byte(record))
	secondHash := sha256.Sum256(firstHash[:])

	return fmt.Sprintf("%x", secondHash)
}

// MeetsTarget özetin zorluk hedefini karşılayıp karşılamadığını kontrol eder.
func MeetsTarget(hash string, difficulty int) bool {
	return len(hash) == 64 && strings.HasPrefix(hash, strings.Repeat("0", difficulty))
}

func (b *Block) MineBlock(difficulty int) {
	b.Difficulty = difficulty

	fmt.Printf("[...] Blok #%d kazılıyor (Zorluk: %d)... \n", b.Index, difficulty)
	startTime := time.Now()

	for {
		b.Hash = b.CalculateHash()
		if MeetsTarget(b.Hash, difficulty) {
			break
		}
		b.Nonce++
	}

	elapsed := time.Since(startTime)
	fmt.Printf("[✓] Blok Kazıldı! Nonce: %d | Süre: %s | Hash: %s\n", b.Nonce, elapsed, b.Hash)
}

// NewGenesisBlock ilk bloğu oluşturur.
// DİKKAT: Zaman damgası time.Now() olduğu için her düğüm farklı bir genesis üretir;
// ağdaki düğümler ortak zincire ancak sabit bir genesis ile anlaşabilir (kullanıcı kararı bekliyor).
func NewGenesisBlock() *Block {
	genesisTx := Transaction{
		Sender:     "SYSTEM",
		Receiver:   "QBTC_SATOSHI_QUANTUM",
		Amount:     21_000_000 * Coin, // İlk can suyu arzı; premine genesis PR'ında kaldırılacak (kullanıcı kararı)
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
