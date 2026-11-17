package network

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// acceptCheckDelay kazılan bloğun eşlerde aranmadan önce beklenen süredir (yayın + doğrulama).
const acceptCheckDelay = 10 * time.Second

// Kabul denetiminde ağ hatası veren eş acceptCheckAttempts kez, aralarda acceptRetryDelay
// beklenerek sorgulanır. Bir düğümün bir eşe açtığı bütün bağlantılar eş için tek adres
// grubudur; grup kotası (connsPerGroup, quota.go) doluyken eş yeni bağlantıyı kabul anında
// kapatır ve sorgu EOF alır. Bu geçici hata "blok yok" sayılmaz.
const (
	acceptCheckAttempts = 3
	acceptRetryDelay    = 2 * time.Second
)

// minWorkWait düğüm senkronize değilken kazımın yeniden denenme aralığıdır (testler değiştirir).
var minWorkWait = 5 * time.Second

// Miner düğümün kazım döngüsüdür. Kazım zincir kilidi tutulmadan yapılır (core.Mine); uç
// değişirse şablon yenilenir. Düğüm senkronize değilken (en az zincir işinin altında) ya da
// bildiği daha çok işli zincirin kazım duraklatma eşiği kadar gerisinde kalıp ona geçemezken
// (core/behind.go) kazılmaz. Kazılan blok eşlere yayılır ve bir süre sonra eşlerde aranır:
// hiçbir eş bloğu saklamamışsa madenci uyarılır (saat, ağ/genesis ayarı ya da bağlantı sorunu).
type Miner struct {
	ps      *P2PServer
	address string
	log     *slog.Logger

	checkDelay    time.Duration
	checkAttempts int           // eş başına en çok sorgu (ağ hatasında yeniden deneme)
	retryDelay    time.Duration // yeniden deneme turları arası bekleme
	checking      atomic.Bool   // aynı anda tek kabul denetimi (eş bağlantısı taşkını olmasın)
	mined         atomic.Int64  // zincire eklenen kendi bloklarımız
	unaccepted    atomic.Int64  // yanıt veren eşlerin hiçbirinde bulunamayan kendi bloklarımız
	unreachable   atomic.Int64  // hiçbir eşin yanıt vermediği (denetlenemeyen) kendi bloklarımız
}

// NewMiner ödülleri address'e giden bir madenci kurar.
func NewMiner(ps *P2PServer, address string) (*Miner, error) {
	if !crypto.IsValidAddress(address) {
		return nil, core.ErrBadReceiver
	}
	return &Miner{ps: ps, address: address, log: slog.Default().With("component", "miner"),
		checkDelay: acceptCheckDelay, checkAttempts: acceptCheckAttempts, retryDelay: acceptRetryDelay}, nil
}

// Run stop kapanana kadar kazar.
func (m *Miner) Run(stop <-chan struct{}) {
	bc := m.ps.Blockchain
	waiting, paused := false, false
	for {
		select {
		case <-stop:
			return
		default:
		}
		// Uç en az zincir işinin altındaysa düğüm senkronize değildir: eski uç üzerine boşa
		// kazılmaz; eşik aşılınca kazım kendiliğinden başlar.
		if bc.BelowMinChainWork() {
			if !waiting {
				m.log.Warn("düğüm senkronize değil (en az zincir işinin altında); kazım bekliyor")
				waiting = true
			}
			if !sleep(stop, minWorkWait) {
				return
			}
			continue
		}
		if waiting {
			m.log.Info("en az zincir işine ulaşıldı; kazım başlıyor")
			waiting = false
		}
		// Düğüm bildiği daha çok işli zincirin eşik kadar gerisindeyse ve ona geçemiyorsa ağ o
		// dalda saatlerdir ilerliyordur: bayat ucun üstüne boşa kazılmaz (core/behind.go).
		// Düğüm o zincire geçince ya da açık eşiğin altına inince kazım kendiliğinden sürer.
		if bc.MiningPaused() {
			if !paused {
				m.log.Error("kazım duraklatıldı: düğüm bildiği daha çok işli zincirin çok gerisinde ve ona geçemiyor; veri dizinini onarın (yedek) ya da silip yeniden eşitleyin",
					"geride_blok", bc.BehindBlocks())
				paused = true
			}
			if !sleep(stop, minWorkWait) {
				return
			}
			continue
		}
		if paused {
			m.log.Info("kazım duraklaması bitti; kazım sürüyor")
			paused = false
		}
		// Şablon yalnızca uç değişince yenilenir: kazım sürerken gelen işlem bir sonraki bloğu bekler.
		txs, err := m.ps.Mempool.Select(core.MaxTxPerBlock)
		if err != nil {
			m.log.Error("havuzdan işlem seçilemedi; yalnızca coinbase kazılıyor", "err", err)
			txs = nil
		}
		t, err := bc.NewTemplate(txs, m.address)
		if err != nil {
			m.log.Error("kazım şablonu kurulamadı", "err", err)
			if !sleep(stop, time.Second) {
				return
			}
			continue
		}
		b, err := bc.Mine(t, stop)
		switch {
		case errors.Is(err, core.ErrMiningStopped):
			return
		case errors.Is(err, core.ErrStaleTemplate):
			continue // uç değişti: yeni şablon
		case errors.Is(err, core.ErrMiningPaused):
			continue // döngü başındaki duraklama beklemesine
		case err != nil:
			m.log.Error("kazım başarısız", "err", err)
			if !sleep(stop, time.Second) {
				return
			}
			continue
		}
		if err := bc.SubmitBlock(b); err != nil {
			if !errors.Is(err, core.ErrStaleTemplate) {
				// Ör. regtest'te damgalar medyan+1 ile FTL'ye dayandı: boş döngüde log taşmasın.
				m.log.Warn("kazılan blok zincire eklenemedi", "index", b.Index, "hash", b.Hash, "err", err)
				if !sleep(stop, time.Second) {
					return
				}
			}
			continue
		}
		m.mined.Add(1)
		m.log.Info("blok kazıldı", "index", b.Index, "hash", b.Hash)
		m.ps.relay(b)
		if m.checking.CompareAndSwap(false, true) {
			go func() {
				defer m.checking.Store(false)
				m.checkAccepted(b, stop)
			}()
		}
	}
}

// checkAccepted bekleme süresinden sonra bloğu eşlerde arar. Ağ hatası veren eş, en çok
// checkAttempts kez sorgulanana kadar turlar halinde yeniden denenir; yanıt veren eş bir daha
// sorgulanmaz. Bir eşte bulunursa sessizce döner. Yanıt veren eşlerin hiçbirinde yoksa
// "eşler reddetmiş olabilir" uyarısı verir (unaccepted); hiçbir eş yanıt vermediyse blok
// hakkında bilgi yoktur, yalnızca bağlantı uyarısı verilir (unreachable).
func (m *Miner) checkAccepted(b *core.Block, stop <-chan struct{}) {
	if !sleep(stop, m.checkDelay) {
		return
	}
	peers := m.ps.Peers()
	if len(peers) == 0 {
		return
	}
	pending := peers
	answered, otherNet := 0, 0
	var lastErr error
	for attempt := 0; attempt < max(m.checkAttempts, 1) && len(pending) > 0; attempt++ {
		if attempt > 0 && !sleep(stop, m.retryDelay) {
			return
		}
		var failed []string
		for _, peer := range pending {
			select {
			case <-stop: // yanıtsız eş başına sorgu saniyeler sürebilir; kapanış beklemesin
				return
			default:
			}
			ok, err := m.ps.PeerHasBlock(peer, b.Hash)
			if errors.Is(err, errWrongNetwork) {
				// Kesin yanıt: başka ağdaki eş bloğu tutamaz, yeniden sorulmaz (netid.go).
				answered++
				otherNet++
				continue
			}
			if err != nil {
				failed = append(failed, peer)
				lastErr = err
				continue
			}
			if ok {
				return
			}
			answered++
		}
		pending = failed
	}
	if answered == 0 {
		m.unreachable.Add(1)
		m.log.Warn("kazılan bloğun kabulü denetlenemedi: hiçbir eş sorguya yanıt vermedi; bağlantıyı ve eşlerin erişilebilirliğini denetleyin (eş bağlantı kotası dolu olabilir)",
			"index", b.Index, "hash", b.Hash, "es_sayisi", len(peers), "err", lastErr)
		return
	}
	m.unaccepted.Add(1)
	m.log.Warn("kazılan blok yanıt veren hiçbir eşte yok: eşler reddetmiş olabilir; saatinizi (NTP), ağ/genesis ayarını ve bağlantıyı denetleyin",
		"index", b.Index, "hash", b.Hash, "es_sayisi", len(peers), "yanit_veren", answered, "ulasilamayan", len(pending),
		"baska_agda", otherNet)
}

// PeerHasBlock eşin bloğu doğrulayıp sakladığını (ana ya da yan zincirde) sorar.
func (ps *P2PServer) PeerHasBlock(peer, hash string) (bool, error) {
	resp, err := ps.request(peer, message{Type: msgGetBlock, Hash: hash}, maxBlockMessageBytes)
	if err != nil {
		return false, err
	}
	if resp.Type != msgBlock {
		return false, errBadResponse
	}
	return resp.Block != nil && resp.Block.Hash == hash, nil
}

// sleep d kadar ya da stop kapanana kadar bekler; stop kapandıysa false döner.
func sleep(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}
