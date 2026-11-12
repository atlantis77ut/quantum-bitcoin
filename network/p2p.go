package network

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"quantum-bitcoin/core"
)

// P2P sınırları: güvenilmez eşlerden gelen veri için.
const (
	maxBlockMessageBytes = 8 << 20 // Bir blok mesajı en fazla 8 MiB (1000 ML-DSA işlemi ~7,2 MiB JSON)
	connReadTimeout      = 10 * time.Second
	connWriteTimeout     = 10 * time.Second
	dialTimeout          = 5 * time.Second
	maxConcurrentConns   = 64
	maxConnsPerIP        = 4 // Tek bir IP tüm bağlantı yuvalarını dolduramasın (eclipse)
	maxConcurrentDecodes = 4 // Aynı anda çözümlenen büyük mesaj sayısı (bellek tavanı)
)

type P2PServer struct {
	ListenAddr string
	Blockchain *core.Blockchain

	mu    sync.RWMutex
	peers []string

	sem       chan struct{}
	decodeSem chan struct{}
	ipMu      sync.Mutex
	ipConns   map[string]int
	log       *slog.Logger
}

func NewP2PServer(listenAddr string, bc *core.Blockchain) *P2PServer {
	return &P2PServer{
		ListenAddr: listenAddr,
		Blockchain: bc,
		sem:        make(chan struct{}, maxConcurrentConns),
		decodeSem:  make(chan struct{}, maxConcurrentDecodes),
		ipConns:    map[string]int{},
		log:        slog.Default().With("component", "p2p"),
	}
}

// AddPeer yayın listesine bir eş ekler.
func (ps *P2PServer) AddPeer(addr string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range ps.peers {
		if p == addr {
			return
		}
	}
	ps.peers = append(ps.peers, addr)
}

// Peers eş listesinin bir kopyasını döndürür.
func (ps *P2PServer) Peers() []string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return append([]string(nil), ps.peers...)
}

// Start dinleyiciyi açar ve bağlantıları kabul eder; dinleyici kapanana kadar bloklar.
func (ps *P2PServer) Start() error {
	listener, err := net.Listen("tcp", ps.ListenAddr)
	if err != nil {
		return err
	}
	return ps.Serve(listener)
}

// Serve verilen dinleyici üzerinden bağlantıları kabul eder (testlerde kullanılır).
func (ps *P2PServer) Serve(listener net.Listener) error {
	defer listener.Close()
	ps.log.Info("P2P sunucusu dinlemede", "addr", listener.Addr().String())

	stop := make(chan struct{})
	defer close(stop)
	go ps.retryPendingLoop(stop)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		ip := hostOf(conn.RemoteAddr())
		if !ps.acquireIP(ip) {
			ps.log.Warn("IP başına bağlantı sınırı aşıldı", "remote", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		select {
		case ps.sem <- struct{}{}:
			go func() {
				defer func() { <-ps.sem; ps.releaseIP(ip) }()
				ps.handleConnection(conn)
			}()
		default:
			ps.releaseIP(ip)
			ps.log.Warn("bağlantı sınırı dolu, bağlantı reddedildi", "remote", conn.RemoteAddr().String())
			conn.Close()
		}
	}
}

func (ps *P2PServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	if err := conn.SetReadDeadline(time.Now().Add(connReadTimeout)); err != nil {
		return
	}

	// Bellek tavanı: aynı anda en fazla maxConcurrentDecodes mesaj çözümlenir.
	select {
	case ps.decodeSem <- struct{}{}:
	case <-time.After(connReadTimeout):
		return
	}
	var incomingBlock core.Block
	err := json.NewDecoder(io.LimitReader(conn, maxBlockMessageBytes)).Decode(&incomingBlock)
	<-ps.decodeSem
	if err != nil {
		ps.log.Warn("blok mesajı okunamadı", "remote", remote, "err", err)
		return
	}

	// Tüm uzlaşma kuralları (PoW, coinbase, imzalar, bakiye, nonce, zaman) burada uygulanır.
	if err := ps.Blockchain.AcceptBlock(&incomingBlock); err != nil {
		ps.log.Warn("eşten gelen blok reddedildi", "remote", remote, "index", incomingBlock.Index, "err", err)
		return
	}
	// nil: blok indekse alındı; uç değişmemiş olabilir (eşit ya da az işli yan blok).
	ps.log.Info("eşten gelen blok kabul edildi", "remote", remote, "index", incomingBlock.Index,
		"uc", ps.Blockchain.TipHash())
}

// pendingRetryInterval gelecek tarihli bekleyen blokların yeniden denenme aralığıdır.
const pendingRetryInterval = 5 * time.Second

// retryPendingLoop saat yetiştikçe bekleyen blokları zincire eklemeyi dener.
func (ps *P2PServer) retryPendingLoop(stop <-chan struct{}) {
	t := time.NewTicker(pendingRetryInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if n := ps.Blockchain.ProcessPending(); n > 0 {
				ps.log.Info("bekleyen bloklar zincire eklendi", "adet", n)
			}
		}
	}
}

// BroadcastBlock bloğu bilinen tüm eşlere gönderir.
func (ps *P2PServer) BroadcastBlock(block *core.Block) {
	for _, peer := range ps.Peers() {
		conn, err := net.DialTimeout("tcp", peer, dialTimeout)
		if err != nil {
			ps.log.Warn("eşe bağlanılamadı", "peer", peer, "err", err)
			continue
		}
		if err := conn.SetWriteDeadline(time.Now().Add(connWriteTimeout)); err == nil {
			if err := json.NewEncoder(conn).Encode(block); err != nil {
				ps.log.Warn("blok eşe gönderilemedi", "peer", peer, "err", err)
			}
		}
		conn.Close()
	}
}

func (ps *P2PServer) acquireIP(ip string) bool {
	ps.ipMu.Lock()
	defer ps.ipMu.Unlock()
	if ps.ipConns[ip] >= maxConnsPerIP {
		return false
	}
	ps.ipConns[ip]++
	return true
}

func (ps *P2PServer) releaseIP(ip string) {
	ps.ipMu.Lock()
	defer ps.ipMu.Unlock()
	if ps.ipConns[ip] <= 1 {
		delete(ps.ipConns, ip)
		return
	}
	ps.ipConns[ip]--
}

func hostOf(a net.Addr) string {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return clientKey(host)
}
