package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"quantum-bitcoin/core"
)

// P2P sınırları: güvenilmez eşlerden gelen veri için.
const (
	maxBlockMessageBytes   = 8 << 20 // Bir blok mesajı en fazla 8 MiB (1000 ML-DSA işlemi ~7,2 MiB JSON)
	maxHeadersMessageBytes = 2 << 20 // 2000 başlık ~0,8 MiB JSON
	connReadTimeout        = 10 * time.Second
	connWriteTimeout       = 10 * time.Second
	dialTimeout            = 5 * time.Second
	maxConcurrentConns     = 64
	maxConnsPerIP          = 4 // Tek bir IP tüm bağlantı yuvalarını dolduramasın (eclipse)
	maxConcurrentDecodes   = 4 // Aynı anda çözümlenen büyük mesaj sayısı (bellek tavanı)
	maxConcurrentServes    = 8 // Aynı anda sunulan blok/başlık yanıtı (bant genişliği tavanı)

	syncInterval  = 30 * time.Second // Senkronizasyon hiç bitmez; "senkronuz" kararı zamana bağlı değildir
	relayQueueLen = 16

	// transferTimeout bir istek yanıtının (en çok ~7 MiB blok) toplam aktarım süresidir:
	// ~1 Mbps bağlantıda dolu bir blok aktarılabilmeli. Sunum yuvası en fazla bu kadar tutulur.
	transferTimeout = 60 * time.Second

	// syncPeerBudget bir eşle başlık ve gövde aşamalarının her birinin süre bütçesidir. Süre
	// her istekten önce denetlendiğinden bir aşama en fazla syncPeerBudget+transferTimeout
	// (~3 dk), bir eşle tur ~6 dk sürer (gövde aşaması ucu aşan gerçek PoW gerektirir). Yavaş
	// ya da kötü niyetli tek bir eş senkronizasyonu böylece geciktirebilir ama bloke edemez;
	// kalan iş sonraki tura kalır. Eşlerin paralel senkronizasyonu DoS PR'ındadır.
	syncPeerBudget = 2 * time.Minute

	// maxHeaderRounds bir turda yeni başlık ekleyen en fazla yanıt sayısıdır.
	maxHeaderRounds = core.MaxSyncHeaders/core.MaxHeadersPerMsg + 2
)

// Mesaj türleri. Her bağlantıda tek bir mesaj (ve istekse tek bir yanıt) taşınır.
const (
	msgBlock      = "block"      // blok duyurusu ya da getblock yanıtı
	msgGetHeaders = "getheaders" // Locator → headers
	msgHeaders    = "headers"
	msgGetBlock   = "getblock" // Hash → block
)

type message struct {
	Type    string             `json:"type"`
	Block   *core.Block        `json:"block,omitempty"`
	Locator []string           `json:"locator,omitempty"`
	Headers []core.BlockHeader `json:"headers,omitempty"`
	Hash    string             `json:"hash,omitempty"`
}

var (
	errBadResponse = errors.New("eşten beklenmeyen yanıt")
	errSyncBudget  = errors.New("eşle tur süresi doldu, kalan iş sonraki turda")
)

type P2PServer struct {
	ListenAddr string
	Blockchain *core.Blockchain

	mu    sync.RWMutex
	peers []string

	sem       chan struct{}
	decodeSem chan struct{}
	serveSem  chan struct{}
	ipMu      sync.Mutex
	ipConns   map[string]int
	log       *slog.Logger

	syncEvery time.Duration

	// syncCh senkronizasyonu hemen tetikler; tek yuvalıdır, bekleyen tetikler birleşir.
	syncCh  chan struct{}
	relayCh chan *core.Block

	// syncRounds tamamlanan senkronizasyon turu sayısıdır (testler için).
	syncRounds atomic.Int64
}

func NewP2PServer(listenAddr string, bc *core.Blockchain) *P2PServer {
	return &P2PServer{
		ListenAddr: listenAddr,
		Blockchain: bc,
		sem:        make(chan struct{}, maxConcurrentConns),
		decodeSem:  make(chan struct{}, maxConcurrentDecodes),
		serveSem:   make(chan struct{}, maxConcurrentServes),
		ipConns:    map[string]int{},
		log:        slog.Default().With("component", "p2p"),
		syncEvery:  syncInterval,
		syncCh:     make(chan struct{}, 1),
		relayCh:    make(chan *core.Block, relayQueueLen),
	}
}

// AddPeer yayın ve senkronizasyon listesine bir eş ekler.
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

// Serve verilen dinleyici üzerinden bağlantıları kabul eder; senkronizasyon ve yayın
// döngülerini de çalıştırır (dinleyici kapanınca durur).
func (ps *P2PServer) Serve(listener net.Listener) error {
	defer listener.Close()
	ps.log.Info("P2P sunucusu dinlemede", "addr", listener.Addr().String())

	stop := make(chan struct{})
	defer close(stop)
	ps.TriggerSync() // açılışta hemen senkronize ol
	go ps.syncLoop(stop)
	go ps.relayLoop(stop)

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
	var msg message
	err := json.NewDecoder(io.LimitReader(conn, maxBlockMessageBytes)).Decode(&msg)
	<-ps.decodeSem
	if err != nil {
		ps.log.Warn("eş mesajı okunamadı", "remote", remote, "err", err)
		return
	}

	switch msg.Type {
	case msgBlock:
		if msg.Block != nil {
			ps.handleBlock(remote, msg.Block)
		}
	case msgGetHeaders:
		ps.serveHeaders(conn, remote, msg.Locator)
	case msgGetBlock:
		ps.serveBlock(conn, remote, msg.Hash)
	default:
		ps.log.Debug("bilinmeyen mesaj türü", "remote", remote, "type", msg.Type)
	}
}

// handleBlock duyurulan bloğu doğrular; uç olduysa eşlere yayar. Ebeveyni bilinmiyorsa
// senkronizasyonu tetikler: gelen bağlantının eşine geri bağlanılamaz, eksik atalar
// yapılandırılmış eşlerden çekilir.
func (ps *P2PServer) handleBlock(remote string, b *core.Block) {
	// Tüm uzlaşma kuralları (PoW, coinbase, imzalar, bakiye, nonce, zaman) burada uygulanır.
	err := ps.Blockchain.AcceptBlock(b)
	switch {
	case err == nil:
		tip := ps.Blockchain.TipHash()
		ps.log.Info("eşten gelen blok kabul edildi", "remote", remote, "index", b.Index, "uc", tip)
		if tip == b.Hash {
			ps.relay(b)
		}
	case errors.Is(err, core.ErrBadPrevHash):
		ps.log.Debug("ebeveyni bilinmeyen blok, senkronizasyon tetiklendi", "remote", remote, "index", b.Index)
		ps.TriggerSync()
	case errors.Is(err, core.ErrKnownBlock):
	case errors.Is(err, core.ErrTimestampFuture), errors.Is(err, core.ErrSideChainLimit),
		errors.Is(err, core.ErrKnownInvalid):
		// Önbelleğe alınmayan ya da ucuz ret yolları: aynı blokla tekrar tekrar tetiklenebilir,
		// log taşkını olmasın diye Debug.
		ps.log.Debug("eşten gelen blok reddedildi", "remote", remote, "index", b.Index, "err", err)
	default:
		ps.log.Warn("eşten gelen blok reddedildi", "remote", remote, "index", b.Index, "err", err)
	}
}

func (ps *P2PServer) reply(conn net.Conn, remote string, m message) {
	if err := conn.SetWriteDeadline(time.Now().Add(transferTimeout)); err != nil {
		return
	}
	if err := json.NewEncoder(conn).Encode(m); err != nil {
		ps.log.Debug("yanıt gönderilemedi", "remote", remote, "err", err)
	}
}

// serveHeaders konum belirleyiciye göre başlık sunar. Ağdan gelen her özet biçimce
// denetlenir; başlıklar indeksten okunur (disk okuması yok).
func (ps *P2PServer) serveHeaders(conn net.Conn, remote string, locator []string) {
	if len(locator) > core.MaxLocatorHashes {
		ps.log.Debug("konum belirleyici çok uzun", "remote", remote, "adet", len(locator))
		return
	}
	for _, h := range locator {
		if !core.IsHashHex(h) {
			ps.log.Debug("geçersiz özet", "remote", remote)
			return
		}
	}
	if !ps.acquireServe() {
		return
	}
	defer func() { <-ps.serveSem }()
	ps.reply(conn, remote, message{Type: msgHeaders, Headers: ps.Blockchain.HeadersAfter(locator, core.MaxHeadersPerMsg)})
}

// serveBlock indeksteki bir bloğu sunar; eşzamanlı sunum serveSem ile sınırlıdır.
func (ps *P2PServer) serveBlock(conn net.Conn, remote, hash string) {
	if !core.IsHashHex(hash) {
		ps.log.Debug("geçersiz özet", "remote", remote)
		return
	}
	if !ps.acquireServe() {
		return
	}
	defer func() { <-ps.serveSem }()
	b, err := ps.Blockchain.BlockByHash(hash)
	if err != nil {
		ps.reply(conn, remote, message{Type: msgBlock})
		return
	}
	ps.reply(conn, remote, message{Type: msgBlock, Block: b})
}

// acquireServe bir sunum yuvası alır; yuva kısa sürede boşalmazsa istek yanıtsız kapanır.
// Kimlik doğrulamasız eşlere karşı eş başına kota DoS PR'ındadır (CLAUDE.md madde 5).
func (ps *P2PServer) acquireServe() bool {
	select {
	case ps.serveSem <- struct{}{}:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// request bir eşe tek bir istek gönderip tek bir yanıt okur.
func (ps *P2PServer) request(peer string, req message, limit int64) (*message, error) {
	conn, err := net.DialTimeout("tcp", peer, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(transferTimeout)); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp message
	if err := json.NewDecoder(io.LimitReader(conn, limit)).Decode(&resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TriggerSync bir sonraki senkronizasyon turunu hemen başlatır; bekleyen bir tetik varsa
// yenisi onunla birleşir (yetim blok yağmuru senkronizasyon fırtınası yaratamaz).
func (ps *P2PServer) TriggerSync() {
	select {
	case ps.syncCh <- struct{}{}:
	default:
	}
}

func (ps *P2PServer) syncLoop(stop <-chan struct{}) {
	t := time.NewTicker(ps.syncEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		case <-ps.syncCh:
		}
		for _, peer := range ps.Peers() {
			if err := ps.syncFrom(peer, stop); err != nil {
				ps.log.Warn("eşle senkronizasyon başarısız", "peer", peer, "err", err)
			}
		}
		ps.syncRounds.Add(1)
	}
}

// syncFrom başlık öncelikli senkronizasyon: eşten başlıkları alır ve doğrular; başlık
// zincirinin işi ucunkini geçerse eksik gövdeleri sırayla indirip doğrular. Zincir kilidi
// ağ G/Ç'si sırasında tutulmaz.
//
// İlerleme kaybolmaz: başlık aşaması hangi nedenle biterse bitsin (süre, tur sınırı, oturum
// sınırı, eşin hatası ya da reorg'u), o ana kadar doğrulanmış başlıkların işi ucunkini
// geçiyorsa gövdeler indirilir. Her başlık PoW dahil ayrı ayrı doğrulandığından geçerli
// önek güvenle kullanılır; gövdeler ayrıca AcceptBlock'ta tam doğrulanır.
func (ps *P2PServer) syncFrom(peer string, stop <-chan struct{}) error {
	bc := ps.Blockchain
	stopped := func() bool {
		select {
		case <-stop:
			return true
		default:
			return false
		}
	}
	s := bc.NewHeaderSync()
	herr := ps.syncHeaders(peer, s, stopped)
	if !s.MoreWork() {
		return herr
	}

	before := bc.TipHash()
	defer func() { // kısmi ilerleme de duyurulur
		if tip := bc.TipHash(); tip != before {
			ps.log.Info("eşle senkronize olundu", "peer", peer, "uc", tip)
			if b, err := bc.BlockByHash(tip); err == nil {
				ps.relay(b)
			}
		}
	}()
	deadline := time.Now().Add(syncPeerBudget) // gövdelerin kendi süre bütçesi var
	for _, h := range s.Missing() {
		if stopped() || time.Now().After(deadline) {
			return errSyncBudget
		}
		resp, err := ps.request(peer, message{Type: msgGetBlock, Hash: h}, maxBlockMessageBytes)
		if err != nil {
			return err
		}
		if resp.Type != msgBlock || resp.Block == nil || resp.Block.Hash != h {
			return fmt.Errorf("%w: blok %s", errBadResponse, h)
		}
		if err := bc.AcceptBlock(resp.Block); err != nil && !errors.Is(err, core.ErrKnownBlock) {
			return fmt.Errorf("blok %d: %w", resp.Block.Index, err)
		}
	}
	return herr
}

// syncHeaders eşten başlıkları oturuma toplar. Dönen hata bilgi amaçlıdır: oturumdaki
// başlıklar her durumda geçerlidir. Geçici durumlar (gelecek tarihli başlık, oturum sınırı)
// hata sayılmaz; kalan başlıklar sonraki turda gelir.
//
// Tur sınırı yalnızca yeni başlık ekleyen yanıtları sayar: zaten indekste olan başlıklar
// (ör. yarım kalmış derin bir reorg'un indirilmiş kısmı) indekste kesin artan yükseklikte
// ilerlediğinden sayıları indeksle sınırlıdır ve süre bütçesine tabidir.
func (ps *P2PServer) syncHeaders(peer string, s *core.HeaderSync, stopped func() bool) error {
	bc := ps.Blockchain
	deadline := time.Now().Add(syncPeerBudget)
	loc := bc.Locator()
	for rounds := 0; rounds < maxHeaderRounds; {
		if stopped() || time.Now().After(deadline) {
			return errSyncBudget
		}
		resp, err := ps.request(peer, message{Type: msgGetHeaders, Locator: loc}, maxHeadersMessageBytes)
		if err != nil {
			return err
		}
		if resp.Type != msgHeaders {
			return errBadResponse
		}
		if len(resp.Headers) == 0 {
			return nil
		}
		n := s.Len()
		if _, err := s.Add(resp.Headers); err != nil {
			if errors.Is(err, core.ErrTimestampFuture) || errors.Is(err, core.ErrSyncLimit) {
				return nil
			}
			return err
		}
		if s.Len() > n {
			rounds++
		}
		if len(resp.Headers) < core.MaxHeadersPerMsg {
			return nil
		}
		loc = append([]string{s.Last()}, bc.Locator()...)
		if len(loc) > core.MaxLocatorHashes {
			loc = loc[:core.MaxLocatorHashes]
		}
	}
	return errSyncBudget
}

// relay bloğu yayın kuyruğuna koyar; kuyruk doluysa atar (eşler onu senkronizasyonla alır).
func (ps *P2PServer) relay(b *core.Block) {
	select {
	case ps.relayCh <- b:
	default:
		ps.log.Debug("yayın kuyruğu dolu, blok yayınlanmadı", "hash", b.Hash)
	}
}

// relayLoop tek bir işçiyle yayın yapar: yavaş eşler gelen bağlantı yuvalarını tutmaz.
func (ps *P2PServer) relayLoop(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case b := <-ps.relayCh:
			ps.BroadcastBlock(b)
		}
	}
}

// BroadcastBlock bloğu bilinen tüm eşlere duyurur. Yayın döngüsü kendiliğinden biter: bloğu
// zaten bilen eş ErrKnownBlock alır ve yeniden yaymaz.
func (ps *P2PServer) BroadcastBlock(block *core.Block) {
	for _, peer := range ps.Peers() {
		conn, err := net.DialTimeout("tcp", peer, dialTimeout)
		if err != nil {
			ps.log.Warn("eşe bağlanılamadı", "peer", peer, "err", err)
			continue
		}
		if err := conn.SetWriteDeadline(time.Now().Add(connWriteTimeout)); err == nil {
			if err := json.NewEncoder(conn).Encode(message{Type: msgBlock, Block: block}); err != nil {
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
