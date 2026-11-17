package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// Yayılım ölçümü (docs/YAYILIM-OLCUM.md). Blok süresi kararının (60 ya da 30 sn) girdisidir;
// düğüm davranışına ve uzlaşmaya dokunmaz, yalnızca -bench-propagation ile çalışır. İki katmanı
// vardır ve güvenilirlikleri farklıdır:
//
//   - Blok ölçümü (BenchBlock): dolu bloğun gerçek kablo boyutu, çözümleme süresi ve taze bir
//     zincirde tam doğrulama (AcceptBlock) süresi. Bu makinede gerçek bileşenlerle ölçülür.
//   - Yayılım benzetimi (BenchPropagation): aynı süreçte birden çok düğüm, bant genişliği ve
//     gecikmesi seçilen profile kısıtlanmış bağlantılarla (throttle proxy) bağlanır; bloğun
//     kazıcının kabulünden her düğümün kabulüne kadar geçen süre ölçülür. Ağ değerleri seçilen
//     profilin sonucudur, gerçek ağ ölçümü değildir.
//
// Benzetimde blok yalnızca itmeyle (push.go) yayılır: alıcılar kaynaklarını yapılandırmaz (uç
// beklemesi ve senkronizasyon kaynaktan çekmez), senkronizasyon aralığı uzundur, keşif kapalıdır.
// Her düğüm yalnızca aşağısındaki eşleri yoklar; yanıtlar da yanıtlayanın yukarı hattından çıkar,
// böylece kazara bir çekme yolu bant sınırını atlayamaz.
// Bütün düğümler 127.0.0.1'dedir: kotalar onları tek adres grubu sayar.

// PropConfig yayılım benzetiminin ayarlarıdır.
type PropConfig struct {
	Txs      int           // bloktaki işlem sayısı (coinbase hariç; en çok core.MaxTxPerBlock)
	Hops     int           // "line": kazıcıdan sonraki düğüm sayısı; "star-*": yaprak sayısı
	Topology string        // "line", "star-leaf" (kazıcı yaprakta) ya da "star-hub" (kazıcı merkezde)
	Mbit     float64       // düğüm başına yukarı bant genişliği (Mbit/sn); düğümün bütün kopyaları paylaşır
	Latency  time.Duration // bağlantı başına tek yön gecikme
	Timeout  time.Duration // bir ölçümde bloğun bütün düğümlere ulaşması için en çok süre
	Dir      string        // geçici zincir dizinlerinin kökü
}

// Topolojiler.
const (
	TopologyLine     = "line"
	TopologyStarLeaf = "star-leaf"
	TopologyStarHub  = "star-hub"
)

var errPropConfig = errors.New("yayılım ölçümü ayarı geçersiz")

// Validate ayarları denetler.
func (c PropConfig) Validate() error {
	switch {
	case c.Txs < 0 || c.Txs > core.MaxTxPerBlock:
		return fmt.Errorf("%w: işlem sayısı %d (0..%d)", errPropConfig, c.Txs, core.MaxTxPerBlock)
	case c.Hops < 1 || c.Hops > 16:
		return fmt.Errorf("%w: atlama sayısı %d (1..16)", errPropConfig, c.Hops)
	case c.Topology != TopologyLine && c.Topology != TopologyStarLeaf && c.Topology != TopologyStarHub:
		return fmt.Errorf("%w: topoloji %q", errPropConfig, c.Topology)
	case c.Topology == TopologyStarLeaf && c.Hops < 2:
		return fmt.Errorf("%w: star-leaf en az 2 yaprak ister", errPropConfig)
	case !(c.Mbit > 0) || c.Mbit > maxPropMbit:
		return fmt.Errorf("%w: bant genişliği %v Mbit/sn (0..%d]", errPropConfig, c.Mbit, maxPropMbit)
	case c.Latency < 0 || c.Latency > maxPropLatency || c.Timeout <= 0:
		return fmt.Errorf("%w: gecikme %v, zaman aşımı %v", errPropConfig, c.Latency, c.Timeout)
	case c.Dir == "":
		return fmt.Errorf("%w: dizin yok", errPropConfig)
	}
	return nil
}

// PropFixture ölçülecek dolu blok ile onu kabul ettirecek önek zinciridir (fonlu gönderen).
type PropFixture struct {
	Prefix    []*core.Block // genesis'ten sonraki önek blokları
	Block     *core.Block   // ölçülen blok (önekin ucuna bağlanır)
	WireBytes int           // bloğun ağdaki mesaj boyutu (stamped JSON, satır sonu dahil)
	wire      []byte
}

// propLogger benzetimdeki düğümlerin loglarıdır: yalnızca uyarılar (ör. itme hatası) görünür.
var propLogger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

// NewPropFixture regtest'te tek göndericiyi fonlayan bir önek kazar ve txs işlemli bloğu kurar.
// Gönderenin bakiyesi bütün işlemleri karşılamadan imza atılmaz.
func NewPropFixture(dir string, txs int) (*PropFixture, error) {
	if txs < 0 || txs > core.MaxTxPerBlock {
		return nil, fmt.Errorf("%w: işlem sayısı %d", errPropConfig, txs)
	}
	bc, err := core.NewBlockchain(filepath.Join(dir, "fixture"), core.RegtestParams)
	if err != nil {
		return nil, err
	}
	defer bc.Close()
	sender, err := crypto.GenerateQuantumKeys()
	if err != nil {
		return nil, err
	}
	recv, err := crypto.GenerateQuantumKeys()
	if err != nil {
		return nil, err
	}
	const amount, fee = 1, 0 // uzlaşma ücret istemez; boyut imza ve açık anahtardan gelir
	need := int64(txs) * (amount + fee)
	maxPrefix := core.RegtestParams.CoinbaseMaturity + 200
	var prefix []*core.Block
	for {
		bal, err := bc.GetBalance(sender.Address)
		if err != nil {
			return nil, err
		}
		if bal >= need && int64(len(prefix)) > core.RegtestParams.CoinbaseMaturity {
			break
		}
		if int64(len(prefix)) >= maxPrefix {
			return nil, fmt.Errorf("önek %d blokta %d birim fonlayamadı (bakiye %d)", len(prefix), need, bal)
		}
		b, err := bc.AddBlock(nil, sender.Address)
		if err != nil {
			return nil, err
		}
		prefix = append(prefix, b)
	}
	nonce, err := bc.GetNonce(sender.Address)
	if err != nil {
		return nil, err
	}
	expiry := int64(len(prefix)) + 1 + core.RegtestParams.DefaultTxLifetime()
	list := make([]core.Transaction, txs)
	for i := range list {
		list[i] = core.Transaction{Receiver: recv.Address, Amount: amount, Fee: fee, ExpiryHeight: expiry,
			Nonce: nonce + uint64(i) + 1}
		if err := list[i].Sign(sender, core.RegtestParams); err != nil {
			return nil, err
		}
	}
	b, err := bc.AddBlock(list, sender.Address)
	if err != nil {
		return nil, err
	}
	if got := len(b.Transactions) - 1; got != txs {
		return nil, fmt.Errorf("blok %d işlem aldı, beklenen %d", got, txs)
	}
	_, genesis := bc.Network()
	wire, err := json.Marshal(stamped(message{Type: msgBlock, Block: b, Network: genesis}))
	if err != nil {
		return nil, err
	}
	wire = append(wire, '\n')
	return &PropFixture{Prefix: prefix, Block: b, WireBytes: len(wire), wire: wire}, nil
}

// prefixChain önek blokları kabul edilmiş taze bir regtest zinciri açar.
func (f *PropFixture) prefixChain(dir string) (*core.Blockchain, error) {
	bc, err := core.NewBlockchain(dir, core.RegtestParams)
	if err != nil {
		return nil, err
	}
	for _, b := range f.Prefix {
		if err := bc.AcceptBlock(b); err != nil {
			bc.Close()
			return nil, fmt.Errorf("önek bloğu %d: %w", b.Index, err)
		}
	}
	return bc, nil
}

// BlockTiming tek blok ölçümünün sonucudur.
type BlockTiming struct {
	Decode time.Duration // ağ mesajının JSON çözümlemesi
	Accept time.Duration // taze zincirde AcceptBlock (PoW, Merkle, imzalar, durum, yazma)
}

// BenchBlock bloğu ağdan gelmiş gibi çözümler ve önekli taze bir zincirde kabul ettirir.
func (f *PropFixture) BenchBlock(dir string) (BlockTiming, error) {
	bc, err := f.prefixChain(dir)
	if err != nil {
		return BlockTiming{}, err
	}
	defer bc.Close()
	start := time.Now()
	var m message
	if err := json.Unmarshal(f.wire, &m); err != nil || m.Block == nil {
		return BlockTiming{}, fmt.Errorf("blok mesajı çözümlenemedi: %v", err)
	}
	decoded := time.Now()
	if err := bc.AcceptBlock(m.Block); err != nil {
		return BlockTiming{}, fmt.Errorf("blok kabul edilmedi: %w", err)
	}
	return BlockTiming{Decode: decoded.Sub(start), Accept: time.Since(decoded)}, nil
}

// PropRun tek yayılım ölçümünün sonucudur. Arrival[i] i. düğümün (kazıcı hariç, topoloji
// sırasıyla) bloğu kazıcının kabulünden sonra kabul ettiği süredir; ulaşmadıysa -1.
type PropRun struct {
	Arrival []time.Duration
	// Takeovers bütün düğümlerdeki itme yuvası devralmalarının toplamıdır (push.go).
	Takeovers PushTakeovers
}

// BenchPropagation tek bir yayılım ölçümü yapar: düğümleri kurar, kazıcıda bloğu kabul ettirip
// yayar ve bütün düğümlerin ucunu izler.
func (f *PropFixture) BenchPropagation(c PropConfig) (PropRun, error) {
	if err := c.Validate(); err != nil {
		return PropRun{}, err
	}
	n := c.Hops + 1
	nodes := make([]*P2PServer, n)
	addrs := make([]string, n)
	lns := make([]net.Listener, n)
	serveDone := make([]chan struct{}, n)
	serving := false // Serve'ler başladı mı (erken hata dönüşünde beklenecek bir şey yok)
	var closers []io.Closer
	defer func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i].Close()
		}
	}()
	for i := range nodes {
		bc, err := f.prefixChain(filepath.Join(c.Dir, fmt.Sprintf("node%d", i)))
		if err != nil {
			return PropRun{}, err
		}
		closers = append(closers, bc)
		ps := NewP2PServer("127.0.0.1:0", bc)
		ps.log = propLogger
		ps.syncEvery = time.Hour // blok yalnızca itmeyle gelsin
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return PropRun{}, err
		}
		// Zincir, Serve dönmeden kapanmasın (açık bağlantılar ve döngüler zinciri okur).
		served := make(chan struct{})
		closers = append(closers, closerFunc(func() error {
			err := ln.Close()
			if serving {
				<-served
			}
			return err
		}))
		nodes[i], addrs[i], lns[i], serveDone[i] = ps, ln.Addr().String(), ln, served
	}
	// Yukarı bağlantılar: düğümün bütün giden bağlantıları aynı hattı paylaşır.
	limits := make([]*linkLimiter, n)
	for i := range limits {
		limits[i] = newLinkLimiter(c.Mbit)
	}
	link := func(from, to int) error {
		p, err := newThrottleProxy(addrs[to], limits[from], limits[to], c.Latency)
		if err != nil {
			return err
		}
		closers = append(closers, p)
		nodes[from].AddPeer(p.Addr())
		return nil
	}
	miner := 0
	switch c.Topology {
	case TopologyLine:
		for i := 0; i+1 < n; i++ {
			if err := link(i, i+1); err != nil {
				return PropRun{}, err
			}
		}
	case TopologyStarLeaf, TopologyStarHub:
		// 0 merkezdir ve yapraklara iter. star-leaf'te kazıcı yaprak (1) yalnızca merkeze iter;
		// merkez onu eş olarak almaz, yoksa ucunu yoklayıp bloğu ondan çekerdi (yalnızca itme).
		first := 1
		if c.Topology == TopologyStarLeaf {
			miner, first = 1, 2
			if err := link(1, 0); err != nil {
				return PropRun{}, err
			}
		}
		for i := first; i < n; i++ {
			if err := link(0, i); err != nil {
				return PropRun{}, err
			}
		}
	}
	serving = true
	for i, ps := range nodes {
		go func(ps *P2PServer, ln net.Listener, done chan struct{}) {
			defer close(done)
			ps.Serve(ln)
		}(ps, lns[i], serveDone[i])
	}
	// Ölçüm kazıcının kabulünden başlar: kazıcı kendi bloğunu doğrulamış sayılır.
	if err := nodes[miner].Blockchain.AcceptBlock(f.Block); err != nil {
		return PropRun{}, fmt.Errorf("kazıcı bloğu kabul etmedi: %w", err)
	}
	t0 := time.Now()
	nodes[miner].BroadcastBlock(f.Block)

	var order []int
	for i := range nodes {
		if i != miner {
			order = append(order, i)
		}
	}
	run := PropRun{Arrival: make([]time.Duration, len(order))}
	var wg sync.WaitGroup
	for k, i := range order {
		wg.Add(1)
		go func(k int, bc *core.Blockchain) {
			defer wg.Done()
			run.Arrival[k] = -1
			deadline := t0.Add(c.Timeout)
			for time.Now().Before(deadline) {
				if bc.TipHash() == f.Block.Hash {
					run.Arrival[k] = time.Since(t0)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}(k, nodes[i].Blockchain)
	}
	wg.Wait()
	for _, ps := range nodes {
		run.Takeovers = run.Takeovers.Add(ps.pushSlots.takeovers())
	}
	return run, nil
}

// linkLimiter bir düğümün yukarı hattıdır: baytlar sırayla, hattın hızında çıkar.
type linkLimiter struct {
	mu   sync.Mutex
	bps  float64   // bayt/sn
	next time.Time // hattın boşalacağı an
}

func newLinkLimiter(mbit float64) *linkLimiter {
	return &linkLimiter{bps: mbit * 1e6 / 8}
}

// waitBacklog hattın birikmesi ahead'e inene kadar bekler. Hat bütün bağlantılarca
// paylaşıldığından bekleme her uyanışta yeniden hesaplanır.
func (l *linkLimiter) waitBacklog(ahead time.Duration) {
	for {
		l.mu.Lock()
		// next.Add(-ahead) önce: hiç kullanılmamış hatta next sıfır zamandır, Until(next) en
		// küçük süreye doyar ve ahead çıkarılınca taşıp dev bir beklemeye dönerdi.
		wait := time.Until(l.next.Add(-ahead))
		l.mu.Unlock()
		if wait <= 0 {
			return
		}
		time.Sleep(wait)
	}
}

// readAhead vekilin hat boşalmadan ne kadar önce okuyabileceğidir: bir parçanın hat süresi,
// en az proxyReadAhead (zamanlama gecikmesi hattı boşta bırakmasın).
func (l *linkLimiter) readAhead() time.Duration {
	return max(proxyReadAhead, time.Duration(float64(proxyChunk)/l.bps*float64(time.Second)))
}

// reserve now'da gelen n baytın hattan son çıkış anını döndürür.
func (l *linkLimiter) reserve(n int, now time.Time) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	start := now
	if l.next.After(start) {
		start = l.next
	}
	l.next = start.Add(time.Duration(float64(n) / l.bps * float64(time.Second)))
	return l.next
}

// Proxy parçası ve kuyruk derinliği: kanal hız·gecikme kadar baytı (gecikme hattı) ve bir pay
// taşır (pipeDepth). Hız sınırlı yönde okumayı waitBacklog durdurur, kanal payı pratikte boş
// kalır; sınırsız yönde kanal dolunca okuma durur ve gönderen TCP geri basıncı görür.
//
// Vekil göndericiden yalnızca hat boşalmak üzereyken okur (readAhead; feat/faithful-proxy) ve
// dinleyicinin alma tamponu küçüktür (proxyRcvBuf, Linux): gerçek ağda hattı bekleyen bayt
// göndericinin kuyruğunda onaylanmamış durur, vekil önden okusaydı çekirdeği baytları hattan
// geçmeden onaylardı ve göndericinin onaylanmamış kuyruğu (push.go teslim ilerlemesi) erken
// boşalırdı. Kalan sapma: gecikme hattındaki bayt (hız·gecikme), onayın dönüş gecikmesi, alma
// tamponu, okuma payı ve hattı paylaşan itme başına bir parça göndericide yine onaylanmış görünür
// (docs/YAYILIM-OLCUM.md); hedefe açılan dönüş bağlantısında alma tamponu ayarlanmaz.
const (
	proxyChunk     = 16 << 10
	proxyQueue     = 16
	proxyReadAhead = 5 * time.Millisecond
	proxyRcvBuf    = 16 << 10
)

// Benzetim sınırları: hız·gecikme kadar bayt proxy kuyruğunda bellekte durur (en çok
// 10 Gbit/sn · 1 sn ≈ 1,25 GB).
const (
	maxPropMbit    = 10_000
	maxPropLatency = time.Second
)

// pipeDepth gidiş yönü kuyruğunun parça sayısıdır. Gecikme hattı hız·gecikme kadar bayt
// taşımalı; sabit derinlik yüksek hız·gecikmede hattı hızdan yavaş yapardı (16 parça /
// gecikme), bu da benzetimin değil proxy'nin sınırı olurdu.
func pipeDepth(lim *linkLimiter, latency time.Duration) int {
	if lim == nil {
		return proxyQueue
	}
	return proxyQueue + int(lim.bps*latency.Seconds()/proxyChunk) + 1
}

// closerFunc bir işlevi io.Closer yapar.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// throttleProxy hedefe giden bağlantıları kısıtlar: gidiş yönü lim (bağlananın yukarı hattı),
// dönüş yönü back (hedefin yukarı hattı; nil ise sınırsız) hızında, ikisi de latency gecikmeyle.
type throttleProxy struct {
	ln      net.Listener
	target  string
	lim     *linkLimiter
	back    *linkLimiter
	latency time.Duration
	wg      sync.WaitGroup
	mu      sync.Mutex
	conns   map[net.Conn]bool
	closed  bool
}

func newThrottleProxy(target string, lim, back *linkLimiter, latency time.Duration) (*throttleProxy, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return setRcvBuf(c, proxyRcvBuf)
	}}
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &throttleProxy{ln: ln, target: target, lim: lim, back: back, latency: latency, conns: map[net.Conn]bool{}}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

// Addr proxy'nin dinlediği adrestir.
func (p *throttleProxy) Addr() string { return p.ln.Addr().String() }

// Close dinlemeyi ve açık bağlantıları kapatır, iletimlerin bitmesini bekler.
func (p *throttleProxy) Close() error {
	err := p.ln.Close()
	p.mu.Lock()
	p.closed = true
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return err
}

func (p *throttleProxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = true
	return true
}

func (p *throttleProxy) untrack(c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, c)
}

func (p *throttleProxy) accept() {
	defer p.wg.Done()
	for {
		src, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go p.serve(src)
	}
}

func (p *throttleProxy) serve(src net.Conn) {
	defer p.wg.Done()
	// TCP el sıkışması (SYN, SYN-ACK) bir gidiş-dönüştür: istemci veriyi ancak 2·latency sonra
	// gönderebilir. İtme her blokta yeni bağlantı açar (peersend.go sendTo), bu yüzden her atlamada
	// ödenir. Bu arada istemcinin yazdıkları çekirdek tamponunda bekler.
	time.Sleep(2 * p.latency)
	dst, err := net.Dial("tcp", p.target)
	if err != nil {
		src.Close()
		return
	}
	if !p.track(src) || !p.track(dst) {
		src.Close()
		dst.Close()
		return
	}
	defer func() {
		src.Close()
		dst.Close()
		p.untrack(src)
		p.untrack(dst)
	}()
	var both sync.WaitGroup
	both.Add(2)
	go func() { defer both.Done(); p.pipe(dst, src, p.lim) }()
	go func() { defer both.Done(); p.pipe(src, dst, p.back) }()
	both.Wait()
}

// pipe src'den okunanı dst'ye, lim hızında (nil ise sınırsız) ve gecikmeyle yazar. Okuma bitince
// kuyruktakiler yazılıp dst'nin yazma yönü kapatılır; yazma hatası iki yönü de keser.
func (p *throttleProxy) pipe(dst, src net.Conn, lim *linkLimiter) {
	type chunk struct {
		data []byte
		at   time.Time
	}
	ch := make(chan chunk, pipeDepth(lim, p.latency))
	go func() {
		defer close(ch)
		for {
			if lim != nil {
				lim.waitBacklog(lim.readAhead())
			}
			buf := make([]byte, proxyChunk)
			n, err := src.Read(buf)
			if n > 0 {
				out := time.Now()
				if lim != nil {
					out = lim.reserve(n, out)
				}
				ch <- chunk{data: buf[:n], at: out.Add(p.latency)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range ch {
		if d := time.Until(c.at); d > 0 {
			time.Sleep(d)
		}
		if _, err := dst.Write(c.data); err != nil {
			src.Close()
			dst.Close()
			for range ch {
			}
			return
		}
	}
	if tc, ok := dst.(*net.TCPConn); ok {
		tc.CloseWrite()
	}
}
