package core

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"github.com/syndtr/goleveldb/leveldb"
)

type Blockchain struct {
	DB         *leveldb.DB
	Difficulty int
	LastHash   string
}

func NewBlockchain(dbPath string, difficulty int) *Blockchain {
	db, err := leveldb.OpenFile(dbPath, nil)
	if err != nil {
		log.Fatalf("[DB HATA] Veritabanı açılamadı: %v", err)
	}

	bc := &Blockchain{
		DB:         db,
		Difficulty: difficulty,
	}

	data, err := db.Get([]byte("lh"), nil)
	if err != nil {
		fmt.Println("[DB] Zincir boş, Genesis bloğu üretiliyor...")
		genesis := NewGenesisBlock()
		bc.SaveBlock(genesis)
		bc.LastHash = genesis.Hash
	} else {
		bc.LastHash = string(data)
		fmt.Printf("[DB] Mevcut zincir diskten yüklendi. Son Blok Hash: %s\n", bc.LastHash)
	}

	return bc
}

// GetBalance bir kuantum adresinin diskteki anlık bakiyesini sorgular
func (bc *Blockchain) GetBalance(address string) int64 {
	data, err := bc.DB.Get([]byte("bal_"+address), nil)
	if err != nil {
		return 0 // Adres daha önce hiç işlem görmediyse bakiyesi 0'dır
	}
	balance, _ := strconv.ParseInt(string(data), 10, 64)
	return balance
}

// setBalance adrese ait bakiyeyi diske yazar
func (bc *Blockchain) setBalance(address string, amount int64) {
	bc.DB.Put([]byte("bal_"+address), []byte(strconv.FormatInt(amount, 10)), nil)
}

// SaveBlock bloğu diske kaydederken adres bakiyelerini günceller (State Ledger)
func (bc *Blockchain) SaveBlock(b *Block) {
	// Blok içindeki her bir işlemi deftere işliyoruz
	for _, tx := range b.Transactions {
		if !tx.IsCoinbase {
			// Gönderenin bakiyesini düşür
			senderBalance := bc.GetBalance(tx.Sender)
			bc.setBalance(tx.Sender, senderBalance-tx.Amount)
		}
		// Alıcının bakiyesini artır
		receiverBalance := bc.GetBalance(tx.Receiver)
		bc.setBalance(tx.Receiver, receiverBalance+tx.Amount)
	}

	blockData, _ := json.Marshal(b)
	bc.DB.Put([]byte(b.Hash), blockData, nil)
	bc.DB.Put([]byte("lh"), []byte(b.Hash), nil)
	bc.LastHash = b.Hash
}

func (bc *Blockchain) GetLatestBlock() *Block {
	data, err := bc.DB.Get([]byte(bc.LastHash), nil)
	if err != nil {
		log.Fatalf("[DB KRİTİK] Son blok diskten okunamadı: %v", err)
	}
	var block Block
	json.Unmarshal(data, &block)
	return &block
}

// AddBlock işlemleri doğrular, madenci ödülünü ekler ve bloğu kazar
func (bc *Blockchain) AddBlock(transactions []Transaction, minerAddress string) (*Block, error) {
	var validatedTransactions []Transaction

	// 1. Önce transfer işlemlerini bakiye kontrolünden geçiriyoruz (Çift Harcama Koruması)
	for _, tx := range transactions {
		senderBalance := bc.GetBalance(tx.Sender)
		if senderBalance < tx.Amount {
			fmt.Printf("[X] İŞLEM REDDEDİLDİ: %s adresinde yeterli bakiye yok! (Mevcut: %d, İstenen: %d)\n", tx.Sender, senderBalance, tx.Amount)
			continue // Yetersiz bakiye olan işlemi bloğa dahil etmiyoruz
		}
		validatedTransactions = append(validatedTransactions, tx)
	}

	// 2. Bloğu kazan madenciye 50 QBTC COINBASE ödülü ekliyoruz!
	coinbaseTx := Transaction{
		Sender:     "SYSTEM_REWARD",
		Receiver:   minerAddress,
		Amount:     50, // Whitepaper Blok Ödülü
		Timestamp:  0,
		IsCoinbase: true,
	}
	// Ödül işlemini en başa koyuyoruz (Bitcoin standardı)
	validatedTransactions = append([]Transaction{coinbaseTx}, validatedTransactions...)

	prevBlock := bc.GetLatestBlock()
	newBlock := &Block{
		Index:        prevBlock.Index + 1,
		Timestamp:    prevBlock.Timestamp + 15,
		Transactions: validatedTransactions,
		PrevHash:     prevBlock.Hash,
	}
	
	newBlock.MineBlock(bc.Difficulty)
	bc.SaveBlock(newBlock)
	return newBlock, nil
}

func (bc *Blockchain) Close() {
	bc.DB.Close()
}
