package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/network"
)

func main() {
	fmt.Println("=== QUANTUM BITCOIN (Q-BTC) PRODUCTION NODE ===")

	// 1. Komut satırı parametreleri
	port := flag.String("port", ":8001", "P2P dinleme portu")
	apiPort := flag.String("api", ":9001", "Borsa API portu")
	dbPath := flag.String("db", "qbtc_production_db", "LevelDB klasör yolu")
	peerAddr := flag.String("peer", "", "Bağlanılacak diğer düğümlerin adresleri (virgülle ayrılmış)")
	tlsCert := flag.String("tls-cert", "", "API için TLS sertifika dosyası (boşsa HTTP)")
	tlsKey := flag.String("tls-key", "", "API için TLS anahtar dosyası")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if (*tlsCert == "") != (*tlsKey == "") {
		slog.Error("tls-cert ve tls-key birlikte verilmeli")
		os.Exit(2)
	}

	// 2. Blockchain
	bc, err := core.NewBlockchain(*dbPath, 4)
	if err != nil {
		slog.Error("zincir başlatılamadı", "err", err)
		os.Exit(1)
	}
	defer bc.Close()

	// 3. P2P sunucusu ve eşler
	p2p := network.NewP2PServer(*port, bc)
	for _, p := range strings.Split(*peerAddr, ",") {
		if p = strings.TrimSpace(p); p != "" {
			p2p.AddPeer(p)
			slog.Info("eş eklendi", "peer", p)
		}
	}

	// 4. Borsa API sunucusu
	apiServer := network.NewAPIServer(*apiPort, bc)
	apiServer.TLSCert, apiServer.TLSKey = *tlsCert, *tlsKey

	errCh := make(chan error, 2)
	go func() { errCh <- fmt.Errorf("p2p: %w", p2p.Start()) }()
	go func() { errCh <- fmt.Errorf("api: %w", apiServer.Start()) }()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		slog.Error("sunucu durdu", "err", err)
	case s := <-sig:
		slog.Info("kapatma sinyali alındı", "signal", s.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := apiServer.Shutdown(ctx); err != nil {
		slog.Error("API düzgün kapatılamadı", "err", err)
	}
}
