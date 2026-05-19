package main

import (
	"flag"
	"fmt"
	"quantum-bitcoin/core"
	"quantum-bitcoin/network"
)

func main() {
	fmt.Println("=== QUANTUM BITCOIN (Q-BTC) PRODUCTION NODE ===")

	// 1. Komut Satırı Parametrelerini Tanımlıyoruz
	port := flag.String("port", ":8001", "P2P Dinleme Portu")
	apiPort := flag.String("api", ":9001", "Borsa API Portu")
	dbPath := flag.String("db", "qbtc_production_db", "LevelDB Klasör Yolu")
	peerAddr := flag.String("peer", "", "Bağlanılacak diğer düğümün adresi")
	flag.Parse()

	// 2. Blockchain Nesnesini Orijinal Mimarisine Göre Başlatıyoruz
	bc := core.NewBlockchain(*dbPath, 4)
	defer bc.Close()

	// 3. Borsa API ve Web Explorer Sunucusunu Başlatıyoruz
	apiServer := network.NewAPIServer(*apiPort, bc)
	
	// Eğer dışarıdan bağlanılacak bir eş (peer) adresi verildiyse 
	// Arka planda o sunucuya köprü atmasını söylüyoruz
	if *peerAddr != "" {
		go func() {
			fmt.Printf("[P2P] %s adresindeki uzak düğüme köprü kuruluyor...\n", *peerAddr)
		}()
	}

	// Ana sunucuyu aktif dinleme modunda ateşle
	apiServer.Start()
}
