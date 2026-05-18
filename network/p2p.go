package network

import (
	"encoding/json"
	"fmt"
	"net"
	"quantum-bitcoin/core"
)

type P2PServer struct {
	ListenAddr string
	Blockchain *core.Blockchain
	Peers      []string
}

func NewP2PServer(listenAddr string, bc *core.Blockchain) *P2PServer {
	return &P2PServer{
		ListenAddr: listenAddr,
		Blockchain: bc,
		Peers:      make([]string, 0),
	}
}

func (ps *P2PServer) Start() error {
	listener, err := net.Listen("tcp", ps.ListenAddr)
	if err != nil {
		return err
	}
	defer listener.Close()

	fmt.Printf("[AĞ] P2P Sunucusu %s adresinde dinlemede...\n", ps.ListenAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			continue
		}
		go ps.handleConnection(conn)
	}
}

func (ps *P2PServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	decoder := json.NewDecoder(conn)

	var incomingBlock core.Block
	if err := decoder.Decode(&incomingBlock); err != nil {
		return
	}

	fmt.Printf("\n[AĞ] Diğer sunucudan yeni bir blok geldi! Blok #%d\n", incomingBlock.Index)
	
	latestBlock := ps.Blockchain.GetLatestBlock()
	if incomingBlock.Index > latestBlock.Index && incomingBlock.PrevHash == latestBlock.Hash {
		if incomingBlock.Hash == incomingBlock.CalculateHash() {
			// DEĞİŞİKLİK: Gelen bloğu artık hafızaya değil, diske mühürlüyoruz!
			ps.Blockchain.SaveBlock(&incomingBlock)
			fmt.Printf("[AĞ] [✓] Gelen blok diske mühürlendi! Yeni En Yüksek Blok: #%d\n", incomingBlock.Index)
		} else {
			fmt.Println("[AĞ] [X] Reddedildi: Hash geçersiz!")
		}
	}
}

func (ps *P2PServer) BroadcastBlock(block *core.Block) {
	for _, peer := range ps.Peers {
		conn, err := net.Dial("tcp", peer)
		if err != nil {
			continue
		}
		encoder := json.NewEncoder(conn)
		encoder.Encode(block)
		conn.Close()
	}
}
