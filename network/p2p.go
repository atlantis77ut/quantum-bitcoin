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
	dialTimeout            = 5 * time.Second
	maxConcurrentConns     = 64
	maxConcurrentDecodes   = 8  // Aynı anda çözümlenen büyük mesaj sayısı (bellek tavanı ~8×8 MiB)
	maxConcurrentServes    = 16 // Aynı anda sunulan blok/başlık yanıtı (bant genişliği tavanı)

	syncInterval  = 30 * time.Second // Senkronizasyon hiç bitmez; "senkronuz" kararı zamana bağlı değildir
	relayQueueLen = 16

	// İşlem yayını: kabul edilen işlemler kuyrukta toplanır ve eşlere en çok maxTxsPerMsg'lik
	// gruplar halinde, en geç txRelayInterval sonra gönderilir (bağlantı başına tek mesaj;
	// işlem başına bağlantı açılmaz). Bir grup ~16 × 7,5 KiB ≈ 120 KiB JSON'dur: karşı düğümde
	// gönderen grubun tek çözümleme yuvasını yavaş bağlantıda da (~2 Mbit/sn) quotaWait'ten çok
	// kısa tutar, ardından gelen blok duyurusu düşmez.
	maxTxsPerMsg    = 16
	txRelayQueueLen = 1024
	txRelayInterval = time.Second

	// transferTimeout bir istek yanıtının (en çok ~7 MiB blok) toplam aktarım süresidir:
	// ~1 Mbps bağlantıda dolu bir blok aktarılabilmeli. Sunum yuvası en fazla bu kadar tutulur.
	transferTimeout = 60 * time.Second

	// syncPeerBudget bir eşle başlık ve gövde aşamalarının her birinin süre bütçesidir. Süre
	// her istekten önce denetlendiğinden bir aşama en fazla syncPeerBudget+transferTimeout
	// (~3 dk), bir eşle tur ~6 dk sürer (gövde aşaması ucu aşan gerçek PoW gerektirir). Yavaş
	// ya da kötü niyetli tek bir eş senkronizasyonu böylece geciktirebilir ama bloke edemez;
	// kalan iş sonraki tura kalır. Eşlerin paralel senkronizasyonu henüz yok.
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
	msgTxs        = "txs"      // işlem duyurusu (en çok maxTxsPerMsg)
)

type message struct {
	Type    string             `json:"type"`
	Block   *core.Block        `json:"block,omitempty"`
	Locator []string           `json:"locator,omitempty"`
	Headers []core.BlockHeader `json:"headers,omitempty"`
	Hash    string             `json:"hash,omitempty"`
	Txs     []core.Transaction `json:"txs,omitempty"`
	TxIDs   []string           `json:"txids,omitempty"`

	// Version gönderenin desteklediği en yüksek protokol sürümü, Services yetenek bitleridir
	// (stamped her gönderimde yazar; bilinmeyen bitler yok sayılır).
	Version  uint32 `json:"version,omitempty"`
	Services uint64 `json:"services,omitempty"`

	// Adres öğrenme (addr.go): Network gönderenin genesis özeti, Addrs "ip:port" listesidir.
	Network string   `json:"network,omitempty"`
	Addrs   []string `json:"addrs,omitempty"`
	// Probe yedek adayı sınamasının belirtecidir (backup.go): kendi sunucumuz görürse aday
	// kendimiziz. Eski düğümler alanı yok sayar.
	Probe string `json:"probe,omitempty"`
}

// Protokol sürümü (düğüm politikası, uzlaşma kuralı değil). Kalıcı oturumlar gibi taşıma
// değişikliklerinin lansmandan sonra pazarlıkla gelebilmesi için her mesaj gönderenin sürümünü
// taşır (docs/ES-KIMLIGI.md §3–4).
//
// Geçiş kuralları: tek yönlü yayınlar (blok, işlem grubu) pazarlık yapamaz, minPeerVersion'daki
// bir düğümün çözebileceği biçimde kalmalıdır. Yeni davranış istek/yanıt yoluyla gelir:
// yanıtlayan isteyenin sürümünü görür ve ikisinin küçüğüyle yanıtlar. Yeni mesaj türleri zaten
// güvenlidir, bilinmeyen tür cezasız bırakılır (handleConnection). Uyumsuz bir değişiklikte
// minPeerVersion yükseltilir; altındaki mesajlar ve yanıtlar cezasız bırakılır (sürüm farkı
// dürüst eşte de olur).
//
// minPeerVersion 0'dır: sürüm alanından önceki (alanı göndermeyen) düğümler de kabul edilir.
// Sürüm 2 uç beklemesini (msgWaitTip/msgTip, tippoll.go), sürüm 3 adres öğrenmeyi
// (msgGetAddr/msgAddr, addr.go) ekledi; ikisi de yeni istek türleri olduğundan eski eşlerle
// uyumludur.
const (
	protocolVersion uint32 = 3
	minPeerVersion  uint32 = 0
	localServices   uint64 = 0 // henüz tanımlı yetenek biti yok
)

var (
	errBadResponse = errors.New("eşten beklenmeyen yanıt")
	errSyncBudget  = errors.New("eşle tur süresi doldu, kalan iş sonraki turda")
	errPeerVersion = errors.New("eşin protokol sürümü desteklenmiyor")
)

// stamped mesaja kendi sürümümüzü ve yeteneklerimizi yazar; her gönderim noktası (reply,
// request, broadcast) kodlamadan hemen önce bundan geçer.
func stamped(m message) message {
	m.Version = protocolVersion
	m.Services = localServices
	return m
}

// versionOK eşin bildirdiği sürümün kabul edilip edilmediğini söyler; daha yeni sürümler
// kabul edilir (bilinmeyen alanlar yok sayılır).
func (ps *P2PServer) versionOK(v uint32) bool {
	return v >= ps.minVersion
}

type P2PServer struct {
	ListenAddr string
	Blockchain *core.Blockchain
	Mempool    *core.Mempool // madenci şablonu ve API işlem kabulü bunu paylaşır

	mu    sync.RWMutex
	peers []string

	// Ortak yuva havuzları; her birinin bir payı korunan gruplara ayrılmıştır (peers.go).
	conns   *slotPool
	decodes *slotPool
	serves  *slotPool
	prot    *protection
	log     *slog.Logger

	// Adres grubu başına kotalar (quota.go).
	connQuota   *groupQuota
	decodeQuota *groupQuota
	serveQuota  *groupQuota

	syncEvery time.Duration

	// minVersion kabul edilen en düşük eş sürümüdür (minPeerVersion; testler yükseltir).
	// Serve'den önce ayarlanır, sonra yalnızca okunur.
	minVersion uint32

	// syncWindow oturum başına bellekteki en fazla yeni başlıktır (core.MaxSyncHeaders;
	// testler küçültür). presyncs yalnızca senkronizasyon döngüsünden erişilir.
	syncWindow int
	presyncs   map[string]*peerPresync

	// presyncBackoff başarısız ön-senkronizasyondan sonra eş başına ertelemedir; yalnızca
	// senkronizasyon döngüsünden erişilir, yapılandırılmış eş sayısıyla sınırlıdır.
	presyncBackoff map[string]*presyncPenalty

	// Havuz eşitlemesi (mempoolsync.go); lastMempoolSync yalnızca senkronizasyon döngüsünden
	// erişilir.
	mempoolSyncEvery time.Duration
	lastMempoolSync  map[string]time.Time

	// syncCh senkronizasyonu hemen tetikler; tek yuvalıdır, bekleyen tetikler birleşir.
	syncCh     chan struct{}
	relayCh    chan relayItem
	txRelayCh  chan core.Transaction
	txOverflow *txOverflow // yayın kuyruğuna sığmayan işlemler (relayoverflow.go)

	// Eş başına gönderim kuyrukları ve devre kesici (peersend.go); senders yapılandırılmış eş
	// sayısıyla sınırlıdır. send Serve'den önce ayarlanır (testler değiştirir), sonra yalnızca
	// okunur.
	sendersMu sync.Mutex
	senders   map[string]*peerSender
	send      func(peer string, data []byte) error

	// İtme taşıması (push.go): pushLim Serve'den önce ayarlanır (testler kısaltır), sonra
	// yalnızca okunur; pushSlots blok itme yuvalarıdır.
	pushLim   pushLimits
	pushSlots *pushSlots

	// Adres öğrenme (addr.go). discover, addrs, seeds ve aralıklar Serve'den önce ayarlanır
	// (EnableDiscovery; testler aralıkları kısaltır), sonra yalnızca okunur. localAddrs
	// (regtest) yönlendirilemeyen adresleri de kabul ettirir. reached istek yapılan eşlere
	// (yapılandırılmış eşler, gömülü liste, yedekler ve sınanan adaylar) son başarılı isteği tutar;
	// keşif açıkken yönetici döngüsü adres kaynakları ve yedekler dışındakileri budar (backup.go).
	// wrongNet `getaddr` yanıtı başka ağ kimliği taşıyan kaynakların yanıtta verilmeyeceği zamanı
	// tutar; adres kaynaklarıyla sınırlıdır.
	discover      bool
	addrs         *AddrBook
	seeds         []string
	addrEvery     time.Duration
	addrScanEvery time.Duration
	localAddrs    bool
	reachedMu     sync.Mutex
	reached       map[string]reachedPeer
	wrongNet      map[string]time.Time

	// Yedek kullanım (backup.go). backups ps.mu altındadır; backupLim Serve'den önce ayarlanır
	// (testler kısaltır), sonra yalnızca okunur. probes bekleyen sınama belirteçleri, selfAddrs
	// kendimiz olduğu görülen adresler (en çok maxSelfAddrs), seedMarks gömülü liste adaylarının
	// işaretleridir; üçü probeMu altındadır.
	backups   []string
	backupLim backupLimits
	probeMu   sync.Mutex
	probes    map[string]bool
	selfAddrs map[string]bool
	seedMarks map[string]seedMark

	// Uç bekleme (tippoll.go). tipCh uç değişince kapatılıp yenilenir, quit Serve dönünce
	// kapanır; ikisi tipMu altındadır. tipWait Serve'den önce ayarlanır (testler kısaltır), sonra
	// yalnızca okunur.
	waits       *slotPool
	waitQuota   *groupQuota
	tipWait     time.Duration
	tipMu       sync.Mutex
	tipCh       chan struct{}
	quit        <-chan struct{}
	lastTipSync atomic.Int64 // uç beklemesinin son senkronizasyon tetiği (tipEpoch'tan beri ns)

	txGate  *txGate      // gelen işlem hız sınırı ve geçersiz işlem cezası (quota.go)
	rejects *rejectCache // eşlerden gelen ve bir daha geçerli olamayacak işlemler (rejectcache.go)
	orphans *orphanPool  // öncülü gelmemiş eş işlemleri (orphans.go)

	// relayGate aynı yuvanın sık yer değiştirme yayınlarını bekletir (relaygate.go).
	relayGate *relayGate

	// restored geri yüklenen, ilk senkronizasyondan sonra bir kez yayılacak işlem kimlikleridir
	// (restorerelay.go).
	restoredMu sync.Mutex
	restored   map[string]struct{}

	// syncRounds tamamlanan senkronizasyon turu sayısıdır (testler için).
	syncRounds atomic.Int64

	// txsSent yayınlanan toplam işlem sayısıdır (relaybudget.go; testler için).
	txsSent atomic.Int64
}

func NewP2PServer(listenAddr string, bc *core.Blockchain) *P2PServer {
	ps := &P2PServer{
		ListenAddr: listenAddr,
		Blockchain: bc,
		Mempool:    core.NewMempool(bc),
		conns:      newSlotPool(maxConcurrentConns, reservedConns),
		decodes:    newSlotPool(maxConcurrentDecodes, reservedDecodes),
		serves:     newSlotPool(maxConcurrentServes, reservedServes),
		prot:       newProtection(),
		log:        slog.Default().With("component", "p2p"),
		syncEvery:  syncInterval,
		minVersion: minPeerVersion,
		syncWindow: core.MaxSyncHeaders,
		presyncs:   map[string]*peerPresync{},
		syncCh:     make(chan struct{}, 1),
		relayCh:    make(chan relayItem, relayQueueLen),
		txRelayCh:  make(chan core.Transaction, txRelayQueueLen),
		txOverflow: newTxOverflow(),
		txGate:     newTxGate(),
		rejects:    newRejectCache(rejectCacheMax),
		orphans:    newOrphanPool(),
		relayGate:  newRelayGate(),

		connQuota:   newGroupQuota(connsPerGroup),
		decodeQuota: newGroupQuota(decodesPerGroup),
		serveQuota:  newGroupQuota(servesPerGroup),

		mempoolSyncEvery: mempoolSyncInterval,
		lastMempoolSync:  map[string]time.Time{},
		presyncBackoff:   map[string]*presyncPenalty{},
		senders:          map[string]*peerSender{},
		pushLim:          defaultPushLimits(),
		pushSlots:        newPushSlots(blockPushSlots),
		addrEvery:        addrInterval,
		addrScanEvery:    addrScanInterval,
		reached:          map[string]reachedPeer{},
		wrongNet:         map[string]time.Time{},
		backupLim:        defaultBackupLimits(),
		probes:           map[string]bool{},
		selfAddrs:        map[string]bool{},
		seedMarks:        map[string]seedMark{},

		waits:     newSlotPool(maxConcurrentWaits, reservedWaits),
		waitQuota: newGroupQuota(waitsPerGroup),
		tipWait:   tipWaitHold,
		tipCh:     make(chan struct{}),
	}
	ps.send = ps.sendTo
	name, _ := bc.Network()
	ps.localAddrs = name == core.RegtestParams.Name
	return ps
}

// AddPeer yayın ve senkronizasyon listesine bir eş ekler; eşin adres grubu korunur (peers.go).
func (ps *P2PServer) AddPeer(addr string) {
	ps.mu.Lock()
	for _, p := range ps.peers {
		if p == addr {
			ps.mu.Unlock()
			return
		}
	}
	ps.peers = append(ps.peers, addr)
	ps.mu.Unlock()
	ps.protectPeer(addr)
}

// Peers yapılandırılmış eşlerin bir kopyasını döndürür (yedekler dahil değil: activePeers,
// backup.go).
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
	ps.setQuit(stop)
	ps.TriggerSync() // açılışta hemen senkronize ol
	go ps.syncLoop(stop)
	go ps.tipPollLoop(stop)
	go ps.relayLoop(stop)
	go ps.txRelayLoop(stop)
	go ps.orphanLoop(stop)
	go ps.restoredRelayLoop(stop)
	go ps.relayGateLoop(stop)
	if ps.discover && ps.addrs != nil {
		go ps.addrLoop(stop)
		go ps.backupLoop(stop)
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		id := peerIDOf(conn.RemoteAddr())
		group := id.group
		if ps.prot.isDiscouraged(id) {
			ps.log.Debug("geri çevrilen eş, bağlantı kapatıldı", "remote", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		if !ps.connQuota.tryAcquire(group) {
			// Debug: kota aşımı saldırgan denetimindedir, log taşkını olmasın.
			ps.log.Debug("adres grubu başına bağlantı sınırı aşıldı", "remote", conn.RemoteAddr().String())
			conn.Close()
			continue
		}
		lvl := ps.prot.levelOf(id)
		if ps.conns.tryAcquire(lvl) {
			go func() {
				defer func() { ps.conns.release(lvl); ps.connQuota.release(group) }()
				ps.handleConnection(conn, id)
			}()
		} else {
			ps.connQuota.release(group)
			// Debug: korunmayan pay saldırgan denetiminde dolabilir, log taşkını olmasın.
			ps.log.Debug("bağlantı sınırı dolu, bağlantı reddedildi", "remote", conn.RemoteAddr().String(), "duzey", int(lvl))
			conn.Close()
		}
	}
}

func (ps *P2PServer) handleConnection(conn net.Conn, id peerID) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	group := id.group

	// Bellek tavanı: aynı anda en fazla maxConcurrentDecodes mesaj çözümlenir; tek bir adres
	// grubu bunların en çok decodesPerGroup kadarını tutar (yavaş gönderen eş, okuma süresi
	// boyunca tüm çözümleme yuvalarını kilitleyemez).
	if !ps.decodeQuota.acquire(group, quotaWait) {
		ps.log.Debug("adres grubu başına çözümleme sınırı aşıldı", "remote", remote)
		return
	}
	lvl := ps.prot.levelOf(id)
	if !ps.decodes.acquire(lvl, connReadTimeout) {
		ps.decodeQuota.release(group)
		return
	}
	// Okuma süresi yuva alındıktan sonra başlar: yuva beklemesi okuma bütçesini yemez. Süre
	// her okumada asgari hıza göre tazelenir (push.go); hıza takılan bağlantı bozuk sayılmaz.
	var msg message
	r := &rateReader{conn: conn, lim: &ps.pushLim, start: time.Now()}
	err := json.NewDecoder(io.LimitReader(r, maxBlockMessageBytes)).Decode(&msg)
	ps.decodes.release(lvl)
	ps.decodeQuota.release(group)
	if err != nil {
		if malformedMessage(err) {
			ps.prot.discourage(id, discouragePeriod)
			ps.log.Debug("bozuk mesaj; eş geri çevriliyor", "remote", remote, "err", err)
			return
		}
		ps.log.Debug("eş mesajı okunamadı", "remote", remote, "err", err)
		return
	}
	if !ps.versionOK(msg.Version) {
		ps.log.Debug("eş sürümü desteklenmiyor; mesaj bırakıldı", "remote", remote, "version", msg.Version)
		return
	}

	switch msg.Type {
	case msgBlock:
		if msg.Block != nil {
			ps.handleBlock(remote, id, msg.Block)
		}
	case msgGetHeaders:
		ps.serveHeaders(conn, remote, id, msg.Locator)
	case msgGetBlock:
		ps.serveBlock(conn, remote, id, msg.Hash)
	case msgTxs:
		ps.handleTxs(remote, id, msg.Txs)
	case msgGetMempool:
		ps.serveMempool(conn, remote, id)
	case msgGetTxs:
		ps.serveTxs(conn, remote, id, msg.TxIDs)
	case msgWaitTip:
		ps.serveWaitTip(conn, remote, id, msg.Hash)
	case msgGetAddr:
		ps.seeProbe(msg.Probe) // sunum kotasından önce: kota doluyken de kendine bağlanma anlaşılır
		ps.serveGetAddr(conn, remote, id, msg.Network)
	case msgAddr:
		// Adres yalnızca kendi isteğimize yanıt olarak alınır; kendiliğinden gelen yok sayılır.
		ps.log.Debug("istenmemiş adres mesajı yok sayıldı", "remote", remote)
	default:
		ps.log.Debug("bilinmeyen mesaj türü", "remote", remote, "type", msg.Type)
	}
}

// handleBlock duyurulan bloğu doğrular; uç olduysa eşlere yayar. Ebeveyni bilinmiyorsa
// senkronizasyonu tetikler: gelen bağlantının eşine geri bağlanılamaz, eksik atalar
// yapılandırılmış eşlerden çekilir.
//
// Ucumuzu uzatan yeni bir bloğu getiren eş korunanlara girer; durumdan bağımsız geçersiz blok
// gönderen eş geri çevrilir, beklenen hedefi taşımayan blok kademeli cezalanır (peers.go).
func (ps *P2PServer) handleBlock(remote string, id peerID, b *core.Block) {
	// Yeni düğüme sahte düşük işli zincir blok blok yutturulamasın (bkz. core/minwork.go);
	// gerçek zincir eşikten geçen başlık zinciriyle senkronizasyondan gelir.
	if ps.Blockchain.RelayBelowMinWork(b) {
		ps.log.Debug("en az zincir işinin altındaki blok bırakıldı", "remote", remote, "index", b.Index)
		return
	}
	// Tüm uzlaşma kuralları (PoW, coinbase, imzalar, bakiye, nonce, zaman) burada uygulanır.
	err := ps.Blockchain.AcceptBlock(b)
	if errors.Is(err, core.ErrBadDifficulty) {
		ps.prot.badDifficulty(id) // ilk ihlal uyarı: sürüm farkı dürüst eşte de olur
	} else if neverHonestBlock(err) {
		ps.prot.discourage(id, discouragePeriod)
	}
	switch {
	case err == nil:
		tip := ps.Blockchain.TipHash()
		ps.log.Info("eşten gelen blok kabul edildi", "remote", remote, "index", b.Index, "uc", tip)
		if tip == b.Hash {
			ps.prot.noteBlock(id)
			ps.relay(b)
		}
	case errors.Is(err, core.ErrBadPrevHash):
		ps.log.Debug("ebeveyni bilinmeyen blok, senkronizasyon tetiklendi", "remote", remote, "index", b.Index)
		ps.TriggerSync()
	case errors.Is(err, core.ErrKnownBlock):
	case errors.Is(err, core.ErrTimestampFuture), errors.Is(err, core.ErrSideChainLimit),
		errors.Is(err, core.ErrKnownInvalid), errors.Is(err, core.ErrBadDifficulty),
		errors.Is(err, core.ErrOrphanTooEasy):
		// Önbelleğe alınmayan ya da ucuz ret yolları: aynı blokla tekrar tekrar tetiklenebilir,
		// log taşkını olmasın diye Debug.
		ps.log.Debug("eşten gelen blok reddedildi", "remote", remote, "index", b.Index, "err", err)
	default:
		ps.log.Warn("eşten gelen blok reddedildi", "remote", remote, "index", b.Index, "err", err)
	}
}

// handleTxs duyurulan işlemleri havuz politikasıyla (core.Mempool.Add: uzlaşma denetimleri ve
// imza) kabul eder ve yalnızca yeni kabul edilenleri yeniden yayar: işlemi zaten bilen eş
// ErrTxKnown alır, yayın döngüsü kendiliğinden biter. Düğüm senkronize değilken (en az zincir
// işinin altında) durum eski olduğundan işlemler bırakılır. Dürüst eş yalnızca kendi kabul
// ettiği işlemleri yayar; biçimi ya da imzası geçersiz işlem içeren grubun kalanı işlenmez
// ve gönderen eşin (iç anahtarın; yükselirse adres grubunun) işlemleri bir süre bırakılır (bkz.
// txGate). Durum farkından doğan retler (yetersiz bakiye, nonce, dolu havuz vb.) olağandır ve
// eşi kesmez; adres grubu başına işlem sayısı ayrıca hız sınırlıdır.
func (ps *P2PServer) handleTxs(remote string, id peerID, txs []core.Transaction) {
	if len(txs) > maxTxsPerMsg {
		ps.log.Debug("işlem grubu çok büyük", "remote", remote, "adet", len(txs))
		ps.txGate.penalize(id)
		return
	}
	if ps.Blockchain.BelowMinChainWork() {
		return
	}
	if !ps.txGate.allow(id, len(txs)) {
		ps.log.Debug("işlem hız sınırı ya da ceza: işlem grubu bırakıldı", "remote", remote, "adet", len(txs))
		return
	}
	for _, tx := range txs {
		// Önbellekteki ret: Add (ve imza doğrulaması) atlanır; geçersiz işlem yine cezalanır.
		// Uç denetimi işlem başına tazelenir: önceki işlemin Add'i sırasında reorg olmuş olabilir.
		switch ps.rejects.check(tx.IDBytes(), ps.onMainFn()) {
		case rejectNone:
		case rejectInvalid:
			ps.log.Debug("eşten bilinen geçersiz işlem; grubun kalanı bırakıldı, eş cezalı", "remote", remote)
			ps.txGate.penalize(id)
			return
		default:
			continue
		}
		err := ps.addPeerTx(tx)
		switch {
		case err == nil:
			ps.RelayTx(tx)
			ps.admitOrphans(tx.Sender, tx.Nonce+1)
		case errors.Is(err, core.ErrNonceGap):
			// Öncül henüz gelmedi (gruplar yer değiştirebilir): yetim tamponu (orphans.go).
			if !ps.bufferOrphan(tx, time.Now()) {
				ps.log.Debug("eşten geçersiz yetim işlem; grubun kalanı bırakıldı, eş cezalı", "remote", remote)
				ps.txGate.penalize(id)
				return
			}
		case invalidTx(err):
			ps.log.Debug("eşten geçersiz işlem; grubun kalanı bırakıldı, eş cezalı", "remote", remote, "err", err)
			ps.txGate.penalize(id)
			return
		default:
			// Durum farkı ya da yineleme: olağan, log taşkını olmasın.
		}
	}
}

// invalidTx işlemin durumdan bağımsız olarak geçersiz olduğunu (dürüst eşin yaymayacağını)
// söyler.
func invalidTx(err error) bool {
	for _, e := range []error{core.ErrBadSignature, core.ErrBadSender, core.ErrBadReceiver,
		core.ErrSelfTransfer, core.ErrNonPositiveAmount, core.ErrNegativeFee, core.ErrBadExpiry,
		core.ErrUnexpectedCoinbase} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// RelayTx havuza yeni kabul edilmiş işlemi yayın kapısından (relaygate.go: aynı yuvanın sık yer
// değiştirmeleri bekletilir) geçirip yayın kuyruğuna koyar.
func (ps *P2PServer) RelayTx(tx core.Transaction) {
	ps.relayTxAt(tx, time.Now())
}

// enqueueRelay işlemi yayın kuyruğuna koyar. Kuyruk doluysa ya da taşma boş değilse (sıra
// korunur) işlem kimliği taşmaya girer; taşma da doluysa işlem atılır ve havuz eşitlemesine
// kalır (relayoverflow.go).
func (ps *P2PServer) enqueueRelay(tx core.Transaction) {
	o := ps.txOverflow
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ids) == 0 {
		select {
		case ps.txRelayCh <- tx:
			return
		default:
		}
	}
	if !o.pushLocked(tx.ID()) {
		ps.log.Debug("işlem yayın taşması dolu, işlem yayınlanmadı", "tx", tx.ID(), "atılan", o.dropped)
	}
}

// txRelayLoop kuyruktaki işlemleri gruplayıp tek işçiyle yayar. Gruplar ve taşma boşaltması
// gönderen kovasından harcar (relaybudget.go); kova yetmeyen grup tutulur ve sonraki turda
// yeniden denenir.
func (ps *P2PServer) txRelayLoop(stop <-chan struct{}) {
	t := time.NewTicker(txRelayInterval)
	defer t.Stop()
	st := &txRelayState{bucket: newSendBucket(time.Now())}
	for {
		// Dolu grup kova yetmediği için tutuluyorsa kuyruk okunmaz (nil kanal seçilmez).
		var in <-chan core.Transaction
		if len(st.batch) < maxTxsPerMsg {
			in = ps.txRelayCh
		}
		select {
		case <-stop:
			return
		case tx := <-in:
			st.batch = append(st.batch, tx)
			if len(st.batch) == maxTxsPerMsg {
				ps.flushTxBatch(st, time.Now())
			}
		case <-t.C:
			ps.relayTick(st, time.Now(), stop)
		}
	}
}

// reply yanıtı parça başına süreyle yazar (deadlineWriter): okumayan eş yuvayı tutamaz.
func (ps *P2PServer) reply(conn net.Conn, remote string, m message) {
	w := &deadlineWriter{conn: conn, chunk: writeChunk, per: chunkWriteTimeout,
		end: time.Now().Add(transferTimeout)}
	if err := json.NewEncoder(w).Encode(stamped(m)); err != nil {
		ps.log.Debug("yanıt gönderilemedi", "remote", remote, "err", err)
	}
}

// serveHeaders konum belirleyiciye göre başlık sunar. Ağdan gelen her özet biçimce
// denetlenir; başlıklar indeksten okunur (disk okuması yok).
func (ps *P2PServer) serveHeaders(conn net.Conn, remote string, id peerID, locator []string) {
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
	ok, lvl := ps.acquireServe(id)
	if !ok {
		return
	}
	defer ps.releaseServe(id, lvl)
	ps.reply(conn, remote, message{Type: msgHeaders, Headers: ps.Blockchain.HeadersAfter(locator, core.MaxHeadersPerMsg)})
}

// serveBlock indeksteki bir bloğu sunar; eşzamanlı sunum serves havuzuyla sınırlıdır.
func (ps *P2PServer) serveBlock(conn net.Conn, remote string, id peerID, hash string) {
	if !core.IsHashHex(hash) {
		ps.log.Debug("geçersiz özet", "remote", remote)
		return
	}
	ok, lvl := ps.acquireServe(id)
	if !ok {
		return
	}
	defer ps.releaseServe(id, lvl)
	b, err := ps.Blockchain.BlockByHash(hash)
	if err != nil {
		ps.reply(conn, remote, message{Type: msgBlock})
		return
	}
	ps.reply(conn, remote, message{Type: msgBlock, Block: b})
}

// acquireServe önce adres grubunun, sonra ortak sunum yuvalarından birini alır; yuva kısa
// sürede boşalmazsa istek yanıtsız kapanır.
// İkinci değer, yuvanın hangi korunma düzeyiyle alındığıdır (releaseServe'e verilir).
func (ps *P2PServer) acquireServe(id peerID) (bool, level) {
	if !ps.serveQuota.acquire(id.group, quotaWait) {
		ps.log.Debug("adres grubu başına sunum sınırı aşıldı", "group", id.group)
		return false, levelNone
	}
	lvl := ps.prot.levelOf(id)
	if !ps.serves.acquire(lvl, quotaWait) {
		ps.serveQuota.release(id.group)
		return false, levelNone
	}
	return true, lvl
}

func (ps *P2PServer) releaseServe(id peerID, lvl level) {
	ps.serves.release(lvl)
	ps.serveQuota.release(id.group)
}

// request bir eşe tek bir istek gönderip tek bir yanıt okur. Desteklenmeyen sürümdeki yanıt
// errPeerVersion döner; çağıranlar onu ağ hatası gibi sayar (geri çevirme yok; ön-senkronizasyon
// gövde indirmesinde ağ hataları gibi ertelemeye sayılır).
func (ps *P2PServer) request(peer string, req message, limit int64) (*message, error) {
	conn, err := net.DialTimeout("tcp", peer, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(transferTimeout)); err != nil {
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(stamped(req)); err != nil {
		return nil, err
	}
	var resp message
	if err := json.NewDecoder(io.LimitReader(conn, limit)).Decode(&resp); err != nil {
		if malformedMessage(err) {
			ps.noteViolation(peer, err) // yalnızca yedeğe işaret konur (backup.go)
		}
		return nil, err
	}
	if !ps.versionOK(resp.Version) {
		return nil, fmt.Errorf("%w: %d", errPeerVersion, resp.Version)
	}
	ps.noteReached(peer, conn.RemoteAddr(), time.Now())
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
		peers := ps.activePeers()
		ps.pruneSyncState(peers, time.Now())
		for _, peer := range peers {
			err := ps.syncFrom(peer, stop)
			if err != nil {
				ps.log.Warn("eşle senkronizasyon başarısız", "peer", peer, "err", err)
			}
			ps.maybePullMempool(peer, err, stop)
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
	if st := ps.presyncs[peer]; st != nil {
		return ps.continuePresync(peer, st, stopped)
	}
	backoff := ps.presyncBackedOff(peer)
	window := ps.syncWindow
	if backoff {
		// Ertelemede eşten turda tek başlık mesajı işlenir: ucumuzu uzatan başlıklar ilk yeni
		// başlıkta ucun işini geçtiğinden olağan senkronizasyon etkilenmez.
		window = min(window, core.MaxHeadersPerMsg)
	}
	s := bc.NewHeaderSync()
	s.SetLimit(window)
	herr := ps.syncHeaders(peer, s, bc.Locator(), stopped)
	if !s.MoreWork() {
		if s.Full() && backoff {
			return errPresyncBackoff
		}
		if !s.Full() && s.Len() > lowWorkHeaderLimit(window) {
			// Pencereyi doldurmayan uzun, az işli yanıt: ön-senkronizasyona girmeden aynı
			// ertelemeye alınır (aksi halde eş her turda pencereye yakın başlık doğrulatırdı).
			if !backoff {
				ps.dropPresync(peer, true, errLowWorkHeaders)
			}
			return errLowWorkHeaders
		}
		if s.Full() {
			// Pencere doldu ama zincir eşiği geçmedi: daha derin bir dal ya da düşük işli spam.
			// Başlıklar saklanmadan sabit bellekte sayılır (core/presync.go).
			st := &peerPresync{p: bc.NewPresync(ps.syncWindow)}
			ps.presyncs[peer] = st
			ps.log.Info("başlık penceresi doldu, ön-senkronizasyon başlıyor", "peer", peer)
			return ps.continuePresync(peer, st, stopped)
		}
		return herr
	}
	if err := ps.downloadBodies(peer, s, stopped); err != nil {
		return err
	}
	return herr
}

// peerPresync bir eşle süren ön-senkronizasyonun turlar arası durumudur: sabit boyutludur
// (kontrol noktaları hariç) ve eş başına tektir; süre bütçesi dolan tur kaldığı yerden sürer.
type peerPresync struct {
	p      *core.Presync
	resume string // yeniden indirmede gövdeleri indirilmiş son yetkili başlık
	fails  int    // art arda başarısız gövde indirme turu
}

// maxPresyncFails art arda bu kadar turda gövdeleri indirilemeyen ön-senkronizasyon bırakılır.
const maxPresyncFails = 3

// Başarısız ön-senkronizasyon ertelemesi (kullanıcı kararı 2026-11-11). Ön-senkronizasyon
// zinciri vermeden bırakılırsa (eşin zinciri eşiğe ulaşmadan bitti, geçersiz başlık,
// yeniden indirme ilerlemedi, gövdeler gelmedi) o eşle yeni ön-senkronizasyon ertelenir:
// süre her başarısızlıkta ikiye katlanır, yalnızca başarılı bir ön-senkronizasyon (uç
// zincirini geçti ya da gövdeler indirildi) sıfırlar. Ucumuzu uzatmak sıfırlamaz: eş dürüst
// blok ileterek cezasını silemez. Böylece önceden hazırlanmış düşük işli bir başlık zinciri
// (taban zorluğunda ucuz, zaman sınırıyla ana zincir kadar uzun) bedavaya her turda yeniden
// doğrulatılamaz. Ertelemede o eşten derin bir dalın benimsenmesi en çok presyncBackoffMax
// gecikir.
const (
	presyncBackoffMin = 10 * time.Minute
	presyncBackoffMax = 4 * time.Hour
)

type presyncPenalty struct {
	until time.Time
	delay time.Duration
}

var (
	errPresyncEnd     = errors.New("eşin zinciri ön-senkronizasyon eşiğine ulaşmadan bitti")
	errPresyncInvalid = errors.New("ön-senkronizasyonda geçersiz başlık")
	errPresyncBackoff = errors.New("eşle ön-senkronizasyon ertelendi (önceki başarısız oldu)")
	errLowWorkHeaders = errors.New("eş ucun işini geçmeyen uzun bir başlık zinciri gönderdi")
)

// lowWorkHeaderLimit ucun işini geçmeyen yanıtta cezasız kabul edilen en fazla yeni başlıktır:
// dürüst bir yan dal ya da bizden geride kalan eş bundan azını gönderir.
func lowWorkHeaderLimit(window int) int {
	return min(core.MaxHeadersPerMsg, window/2)
}

// presyncBackedOff eşle yeni ön-senkronizasyonun ertelenip ertelenmediğini söyler.
func (ps *P2PServer) presyncBackedOff(peer string) bool {
	pen := ps.presyncBackoff[ps.presyncKey(peer)]
	return pen != nil && time.Now().Before(pen.until)
}

// dropPresync eşin ön-senkronizasyon durumunu bırakır. failed ise (zincir alınamadı) erteleme
// başlar ya da ikiye katlanır; değilse erteleme sıfırlanır.
func (ps *P2PServer) dropPresync(peer string, failed bool, reason error) {
	delete(ps.presyncs, peer)
	key := ps.presyncKey(peer)
	if !failed {
		delete(ps.presyncBackoff, key)
		return
	}
	d := presyncBackoffMin
	if pen := ps.presyncBackoff[key]; pen != nil {
		d = min(2*pen.delay, presyncBackoffMax)
	}
	ps.presyncBackoff[key] = &presyncPenalty{until: time.Now().Add(d), delay: d}
	ps.log.Info("ön-senkronizasyon bırakıldı, eşle yenisi ertelendi", "peer", peer, "erteleme", d, "neden", reason)
}

// continuePresync ön-senkronizasyonu sürdürür; eşik geçildiyse başlıkları parça parça
// yeniden indirir ve yalnızca kontrol noktalarıyla eşleşen önekin gövdelerini indirir.
// Durum yalnızca kalıcı başarısızlıkta (eşin zinciri eşiğe ulaşmadan bitti, geçersiz
// başlık, kontrol noktası tutmadı, yetki düştü) ya da son başlığa ulaşılınca silinir.
func (ps *P2PServer) continuePresync(peer string, st *peerPresync, stopped func() bool) error {
	bc := ps.Blockchain
	if !st.p.Done() {
		err := ps.presyncHeaders(peer, st.p, stopped)
		if !st.p.Done() {
			if errors.Is(err, errPresyncEnd) || errors.Is(err, errPresyncInvalid) {
				ps.log.Info("ön-senkronizasyon eşiğe ulaşmadı", "peer", peer, "dogrulanan", st.p.Count())
				ps.dropPresync(peer, true, err)
			}
			return err
		}
		ps.log.Info("ön-senkronizasyon eşiği geçti, başlıklar yeniden indiriliyor", "peer", peer, "dogrulanan", st.p.Count())
	}
	deadline := time.Now().Add(syncPeerBudget)
	for !stopped() && time.Now().Before(deadline) {
		s := bc.NewProvenSync(st.p, ps.syncWindow)
		loc := bc.Locator()
		if st.resume != "" {
			loc = withFirst(st.resume, loc)
		}
		herr := ps.syncHeaders(peer, s, loc, stopped)
		if !s.MoreWork() {
			ps.dropPresync(peer, false, nil) // uç ön-senkronizasyonun işini geçti: olağan senkronizasyon sürer
			return herr
		}
		if s.AuthorizedTip() == "" {
			if herr == nil || headerRejected(herr) {
				err := fmt.Errorf("ön-senkronizasyon yeniden indirmesi ilerlemedi: %v", herr)
				ps.dropPresync(peer, true, err) // eş ön-senkronizasyondaki zinciri vermiyor
				return err
			}
			return herr // geçici (ağ, süre): sonraki tur
		}
		if s.AuthorizedTip() == st.resume {
			err := fmt.Errorf("ön-senkronizasyon yeniden indirmesi ilerlemedi: %v", herr)
			ps.dropPresync(peer, true, err) // eş ilerleme vermiyor (yalnızca bilinen önek)
			return err
		}
		before := bc.TipHash()
		if err := ps.downloadBodies(peer, s, stopped); err != nil {
			if errors.Is(err, errSyncBudget) || bc.TipHash() != before {
				// Yavaş ama ilerleyen eş (ör. en az zincir işinin altında ilk eşitleme)
				// başarısız sayılmaz; kalan gövdeler sonraki turda.
				st.fails = 0
				return err
			}
			if st.fails++; st.fails >= maxPresyncFails {
				ps.dropPresync(peer, true, err) // eş gövdeleri vermiyor ya da geçersiz veriyor
			}
			return err
		}
		st.fails = 0
		st.resume = s.AuthorizedTip()
		if s.ReachedFinal() || headerRejected(herr) {
			// Son başlığa ulaşıldı (kalan iş olağan senkronizasyonla gelir) ya da eş yetkili
			// önekten sonra tutarsızlaştı (yetkili kısım indirildi, gerisi bırakıldı).
			ps.dropPresync(peer, false, nil)
			return herr
		}
	}
	return errSyncBudget
}

// presyncHeaders ön-senkronizasyona başlık yanıtları besler; süre bütçesi dolarsa durum
// korunur ve sonraki turda sürer.
func (ps *P2PServer) presyncHeaders(peer string, p *core.Presync, stopped func() bool) error {
	bc := ps.Blockchain
	deadline := time.Now().Add(syncPeerBudget)
	for {
		if stopped() || time.Now().After(deadline) {
			return errSyncBudget
		}
		loc := bc.Locator()
		if p.Last() != "" {
			loc = withFirst(p.Last(), loc)
		}
		resp, err := ps.request(peer, message{Type: msgGetHeaders, Locator: loc}, maxHeadersMessageBytes)
		if err != nil {
			return err
		}
		if resp.Type != msgHeaders {
			return errBadResponse
		}
		if len(resp.Headers) == 0 {
			return errPresyncEnd
		}
		if _, err := p.Add(resp.Headers); err != nil {
			if errors.Is(err, core.ErrTimestampFuture) {
				return nil // geçici: kalan başlıklar sonraki turda
			}
			if neverHonestBlock(err) {
				ps.noteViolation(peer, err)
			}
			return fmt.Errorf("%w: %v", errPresyncInvalid, err)
		}
		if p.Done() {
			return nil
		}
		if len(resp.Headers) < core.MaxHeadersPerMsg {
			return errPresyncEnd
		}
	}
}

// headerRejected başlık zincirinin kalıcı olarak reddedildiğini söyler (geçici ağ ya da
// süre hatalarının tersine): ön-senkronizasyon durumu bırakılır.
func headerRejected(err error) bool {
	for _, e := range []error{core.ErrBadHeaders, core.ErrKnownInvalid, core.ErrBadPrevHash,
		core.ErrBadDifficulty, core.ErrInsufficientPoW, core.ErrBadHash, core.ErrBadIndex,
		core.ErrTimestampOld, core.ErrBadVersion} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// withFirst konum belirleyicinin başına bir özet ekler ve boyutu sınırlar.
func withFirst(h string, loc []string) []string {
	out := append([]string{h}, loc...)
	if len(out) > core.MaxLocatorHashes {
		out = out[:core.MaxLocatorHashes]
	}
	return out
}

// downloadBodies oturumun yetkili eksik gövdelerini sırayla indirip doğrular; kısmi
// ilerleme de duyurulur. Yeni uç bu eşten indirildiyse eşe geri itilmez.
func (ps *P2PServer) downloadBodies(peer string, s *core.HeaderSync, stopped func() bool) error {
	bc := ps.Blockchain
	before := bc.TipHash()
	got := map[string]bool{}
	defer func() {
		if tip := bc.TipHash(); tip != before {
			ps.log.Info("eşle senkronize olundu", "peer", peer, "uc", tip)
			if b, err := bc.BlockByHash(tip); err == nil {
				from := ""
				if got[tip] {
					from = peer
				}
				ps.relayFrom(b, from)
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
			if neverHonestBlock(err) {
				ps.noteViolation(peer, err)
			}
			return fmt.Errorf("blok %d: %w", resp.Block.Index, err)
		}
		got[h] = true
	}
	return nil
}

// syncHeaders eşten başlıkları oturuma toplar. Dönen hata bilgi amaçlıdır: oturumdaki
// başlıklar her durumda geçerlidir. Geçici durumlar (gelecek tarihli başlık, oturum sınırı)
// hata sayılmaz; kalan başlıklar sonraki turda gelir.
//
// Tur sınırı yalnızca yeni başlık ekleyen yanıtları sayar: zaten indekste olan başlıklar
// (ör. yarım kalmış derin bir reorg'un indirilmiş kısmı) indekste kesin artan yükseklikte
// ilerlediğinden sayıları indeksle sınırlıdır ve süre bütçesine tabidir.
func (ps *P2PServer) syncHeaders(peer string, s *core.HeaderSync, loc []string, stopped func() bool) error {
	bc := ps.Blockchain
	deadline := time.Now().Add(syncPeerBudget)
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
			if neverHonestBlock(err) {
				ps.noteViolation(peer, err)
			}
			return err
		}
		if s.Len() > n {
			rounds++
		}
		if len(resp.Headers) < core.MaxHeadersPerMsg || s.Full() {
			return nil // dolu pencere için bir yanıt daha istenmez
		}
		loc = withFirst(s.Last(), bc.Locator())
	}
	return errSyncBudget
}

// relay bloğu yayın kuyruğuna koyar; kuyruk doluysa atar (eşler onu senkronizasyonla alır).
// Ucu değiştiren her yol (gelen blok, senkronizasyon, madenci) buradan geçer: uç bekleyenler
// kuyruk dolu olsa da uyanır.
func (ps *P2PServer) relay(b *core.Block) {
	ps.relayFrom(b, "")
}

// relayFrom relay gibidir; blok from eşinden indirildiyse o eşe geri itilmez (push.go).
func (ps *P2PServer) relayFrom(b *core.Block, from string) {
	ps.notifyTip()
	select {
	case ps.relayCh <- relayItem{b: b, from: from}:
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
		case it := <-ps.relayCh:
			ps.broadcastFrom(message{Type: msgBlock, Block: it.b}, it.from)
		}
	}
}

// BroadcastBlock bloğu bilinen tüm eşlere duyurur. Yayın döngüsü kendiliğinden biter: bloğu
// zaten bilen eş ErrKnownBlock alır ve yeniden yaymaz.
func (ps *P2PServer) BroadcastBlock(block *core.Block) {
	ps.broadcast(message{Type: msgBlock, Block: block})
}

// broadcast mesajı bir kez kodlar ve her yapılandırılmış eşin gönderim kuyruğuna koyar
// (peersend.go); gönderimi beklemez. Erişilemeyen ya da yavaş okuyan eş yalnızca kendi kuyruğunu
// geciktirir.
func (ps *P2PServer) broadcast(m message) {
	ps.broadcastFrom(m, "")
}

// broadcastFrom broadcast gibidir; skip eşini atlar (mesajın kaynağı).
func (ps *P2PServer) broadcastFrom(m message, skip string) {
	data, err := json.Marshal(stamped(m))
	if err != nil {
		ps.log.Error("yayın mesajı kodlanamadı", "type", m.Type, "err", err)
		return
	}
	data = append(data, '\n') // json.Encoder biçimi; kuyruklar yalnızca okur
	block := m.Type == msgBlock
	now := time.Now()
	for _, peer := range ps.activePeers() {
		if skip != "" && peer == skip {
			continue
		}
		ps.enqueueSend(peer, data, block, now)
	}
}
