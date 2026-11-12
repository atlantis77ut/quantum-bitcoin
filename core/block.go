package core

// Uzlaşma (consensus) parametreleri. Değiştirmeden önce kullanıcıya sorulmalı.
const (
	Coin           int64 = 100_000_000     // 1 QBTC = 10^8 taban birim; tüm tutarlar taban birim cinsindendir
	CoinbaseSender       = "SYSTEM_REWARD" // Ödül işlemlerinin göndereni
	MaxTxPerBlock        = 1000            // Coinbase hariç blok başına en fazla işlem (DoS sınırı)
	MaxFutureDrift       = 5 * 60          // Saniye (FTL): blok zamanı yerel saatten en fazla bu kadar ileride olabilir
	MedianTimeSpan       = 11              // Blok zamanı son N bloğun medyanından büyük olmalı
)

// MaxFutureDrift blok süresinin katı değil, mutlak bir süredir: dürüst düğümlerin saat
// kayması blok süresiyle küçülmez (kullanıcı kararı, 2026-11-08). LWMA'nın pencere uzunluğu
// N seçildiğinde N·TargetSpacing/20 üst sınırına göre yeniden sınanmalıdır. Zaman yalnızca
// yerel saatten okunur; eşlerden ayarlanmış ağ zamanı kullanılmaz.

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

// genesisPrevHash genesis'in önceki blok özetidir (yok: 32 sıfır bayt).
const genesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// NewGenesisBlock ağın sabit genesis bloğunu parametrelerden deterministik olarak üretir.
// Premine yoktur: tek işlem, tutarı sıfır olan ve alıcısı genesis mesajından türetilen
// bir coinbase'dir. Blok, ağın başlangıç hedefine sabit nonce ile kazılmıştır; özetin
// p.GenesisHash'e eşit olduğu NewBlockchain'de ve testlerde doğrulanır.
func NewGenesisBlock(p *Params) (*Block, error) {
	if p == nil || p.GenesisHash == "" {
		return nil, ErrNoGenesis
	}
	coinbase := Transaction{
		Sender:     CoinbaseSender,
		Receiver:   p.GenesisReceiver(),
		Amount:     0,
		Nonce:      0, // yükseklik
		IsCoinbase: true,
	}
	block := &Block{
		Version:      BlockVersion,
		Index:        0,
		Timestamp:    p.GenesisTimestamp,
		Transactions: []Transaction{coinbase},
		PrevHash:     genesisPrevHash,
		Bits:         p.Bits,
		Nonce:        p.GenesisNonce,
	}
	block.MerkleRoot = block.ComputeMerkleRoot()
	block.Hash = block.CalculateHash()
	return block, nil
}
