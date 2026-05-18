package main

import (
	"flag"
	"fmt"
	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
	"quantum-bitcoin/network"
	"time"
)

func main() {
	fmt.Println("=== QUANTUM BITCOIN (Q-BTC) PRODUCTION NODE ===")

	// Terminal parametreleri
	p2pPort := flag.String("port", ":8001", "P2P Dinleme Portu")
	apiPort := flag.String("api", ":9001", "Borsa API Portu")
	dbPath := flag.String("db", "qbtc_production_db", "LevelDB Klasör Yolu")
	flag.Parse()

	// 1. Blok Zincirini ve Veritabanını Başlat
	qbtcChain := core.NewBlockchain(*dbPath, 4)
	defer qbtcChain.Close()

	// 2. Kuantum Cüzdanları Oluştur
	minerWallet, _ := crypto.GenerateQuantumKeys()
	userWallet, _ := crypto.GenerateQuantumKeys()
	fmt.Printf("[SİSTEM] Madenci Adresi: %s\n", minerWallet.Address)
	fmt.Printf("[SİSTEM] Kullanıcı Adresi: %s\n", userWallet.Address)

	// 3. Borsa API Sunucusunu Asenkron Başlat
	apiServer := network.NewAPIServer(*apiPort, qbtcChain)
	go apiServer.Start()

	// 4. Arka Planda P2P Sunucusunu Başlat
	p2pServer := network.NewP2PServer(*p2pPort, qbtcChain)
	go func() {
		_ = p2pServer.Start()
	}()

	// Sistemlerin ayağa kalkması için kısa bir es
	time.Sleep(2 * time.Second)

	// 5. Simülasyon: İlk Bloğu Kazalım ki API'den sorgulayacak verimiz olsun
	fmt.Println("\n[SİSTEM] İlk blok kazım işlemi otomatik başlatılıyor...")
	txPool := []core.Transaction{
		{Sender: minerWallet.Address, Receiver: userWallet.Address, Amount: 15, Timestamp: time.Now().Unix(), IsCoinbase: false},
	}
	_, _ = qbtcChain.AddBlock(txPool, minerWallet.Address)

	fmt.Println("[SİSTEM] Düğüm tamamen hazır. Dış bağlantılar bekleniyor...")
	
	// Sunucunun açık kalması için sonsuz döngü
	select {}
}
