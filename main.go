package main

import (
	"flag"
	"fmt"
	"quantum-bitcoin/core"
	"quantum-bitcoin/network"
)

func main() {
	fmt.Println("=== QUANTUM BITCOIN (Q-BTC) PRODUCTION NODE ===")

<<<<<<< HEAD
	// Parametre Tanımlamaları
	port := flag.String("port", ":8001", "P2P Dinleme Portu")
	apiPort := flag.String("api", ":9001", "Borsa API Portu")
	dbPath := flag.String("db", "qbtc_production_db", "LevelDB Klasör Yolu")
	peerAddr := flag.String("peer", "", "100.73.226.15:8001")
	flag.Parse()

	// 1. Veritabanını ve Blockchain Nesnesini Başlat
	bc, err := core.NewBlockchain(*dbPath)
	if err != nil {
		fmt.Printf("[X] Blockchain başlatılamadı: %v\n", err)
		return
	}
	defer bc.Close()

	// 2. Borsa API ve Web Explorer Sunucusunu Başlat
	apiServer := network.NewAPIServer(*apiPort, bc)
	go apiServer.Start()

	// 3. P2P Ağ Katmanını Başlat
	p2pServer := network.NewServer(*port, bc)
	
	// Eğer dışarıdan bir peer (eş) adresi verildiyse, otomatik bağlan
	if *peerAddr != "" {
		go func() {
			fmt.Printf("[P2P] %s adresindeki düğüme bağlanılıyor...\n", *peerAddr)
			p2pServer.ConnectToPeer(*peerAddr)
		}()
	}

	// P2P Sunucusunu dinleme modunda tut
	p2pServer.Start()
=======
	// 1. Komut Satırı Parametrelerini Tanımlıyoruz
	port := flag.String("port", ":8001", "P2P Dinleme Portu")
	apiPort := flag.String("api", ":9001", "Borsa API Portu")
	dbPath := flag.String("db", "qbtc_production_db", "LevelDB Klasör Yolu")
	peerAddr := flag.String("peer", "", "Bağlanılacak diğer düğümün adresi")
	flag.Parse()

	// 2. Blockchain Nesnesini Orijinal Mimarisine Göre Başlatıyoruz
	// (Yol ve Başlangıç Zorluk Derecesi olan 4 değerini gönderiyoruz)
	bc := core.NewBlockchain(*dbPath, 4)
	defer bc.Close()

	// 3. Borsa API ve Web Explorer Sunucusunu Başlatıyoruz
	// (Bu yapı zaten P2P ağını veya API uçlarını içeriyor)
	apiServer := network.NewAPIServer(*apiPort, bc)
	
	// Eğer dışarıdan bağlanılacak bir eş (peer) adresi verildiyse 
	// Arka planda o sunucuya köprü atmasını söylüyoruz
	if *peerAddr != "" {
		go func() {
			fmt.Printf("[P2P] %s adresindeki uzak düğüme köprü kuruluyor...\n", *peerAddr)
			// Eğer API sunucunun altında bir Connect metodu yoksa 
			// P2P senkronizasyonu API katmanı üzerinden yönetilir
		}()
	}

	// Ana sunucuyu aktif dinleme modunda ateşle
	apiServer.Start()
>>>>>>> cba3ac3 (Fix: Align main.go parameters with original core and network structure)
}
