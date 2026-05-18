package network

import (
	"encoding/json"
	"fmt"
	"net/http"
	"quantum-bitcoin/core"
)

type APIServer struct {
	Blockchain *core.Blockchain
	ListenAddr string
}

func NewAPIServer(listenAddr string, bc *core.Blockchain) *APIServer {
	return &APIServer{
		Blockchain: bc,
		ListenAddr: listenAddr,
	}
}

// Start API sunucusunu HTTP protokolü üzerinden ateşler
func (api *APIServer) Start() {
	// 1. Rota: Bakiye Sorgulama (Borsalar kullanıcı bakiyelerini bununla doğrular)
	// Kullanım: http://localhost:9001/balance?address=QBTC...
	http.HandleFunc("/balance", api.handleGetBalance)

	// 2. Rota: Son Bloğu Getir (Borsalar ağı senkronize etmek için sürekli bu ucu tetikler)
	// Kullanım: http://localhost:9001/latestblock
	http.HandleFunc("/latestblock", api.handleGetLatestBlock)

	fmt.Printf("[API] Borsa Entegrasyon HTTP Sunucusu %s adresinde başlatıldı...\n", api.ListenAddr)
	if err := http.ListenAndServe(api.ListenAddr, nil); err != nil {
		fmt.Printf("[API HATA] HTTP Sunucu başlatılamadı: %v\n", err)
	}
}

func (api *APIServer) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	address := r.URL.Query().Get("address")
	if address == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "address parametresi eksik"})
		return
	}

	balance := api.Blockchain.GetBalance(address)
	response := map[string]interface{}{
		"address": address,
		"balance": balance,
		"unit":    "QBTC",
	}
	json.NewEncoder(w).Encode(response)
}

func (api *APIServer) handleGetLatestBlock(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	latestBlock := api.Blockchain.GetLatestBlock()
	json.NewEncoder(w).Encode(latestBlock)
}
