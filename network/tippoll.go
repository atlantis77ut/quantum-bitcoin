package network

import (
	"errors"
	"net"
	"time"

	"quantum-bitcoin/core"
)

// Uç bekleme (uzun yoklama; docs/ES-KIMLIGI.md §4 adım 4, kullanıcı kararı 2026-11-13). Eşler
// yalnızca kendi yapılandırılmış eşlerine yayın yapar; NAT arkasındaki ya da gelen bağlantı
// almayan düğüm yeni blokları eskiden yalnızca syncInterval'lik senkronizasyon turunda
// öğrenirdi. Düğüm artık her yapılandırılmış eşe "uç değişince yanıtla" isteği (msgWaitTip)
// gönderir; eş isteği en çok tipWaitHold tutar, ucu istekteki özetten farklı olunca (ya da süre
// dolunca) uç başlığıyla yanıtlar, düğüm hemen yenisini açar. İstek eşin bildiğimiz son ucunu
// taşır, kendi ucumuzu değil: çatallı ya da önde olan düğümde sıkı döngü oluşmaz. Bugünkü
// tek mesajlı istek/yanıt modeline uyar; oturum ve tahliye yoktur.
//
// Sunucu: bekleyen istekler ayrı, paylı bir yuva havuzunda (waits) ve grup başına waitsPerGroup
// kotasıyla durur; beklerken bağlantı yuvasını da tutar (yavaş gönderimle bağlantı yuvası
// tutmak bugün de mümkündür; bekleme havuzunun açık payı bağlantılarınkinin yarısıdır). Havuz
// ya da kota doluysa, ya da düğüm en az zincir işinin altındaysa (ucu henüz anlamlı değil) boş
// bir msgTip ile hemen yanıtlanır: "destekliyor ama şimdi bekletemiyor". Dürüst istemciler
// sunucu açısından çoğunlukla korunmayan düzeydedir (NAT arkasındaki düğüm sunucunun -peer
// listesinde değildir, blok kazmadıkça koruma da kazanmaz); ayrılmış pay daha çok açık iki
// düğüm arasında işe yarar. Aynı /24 ya da /48 arkasındaki istemciler (CGNAT) tek bekleme
// yuvasını paylaşır; yuvayı alamayan bugünkü gibi senkronizasyon turuna kalır.
//
// İstemci: yanıttaki başlık ana zincirimizde değilse önce doğrulanır (HeaderSync: zorluk, PoW,
// zaman; gerçek iş ister). Ucumuzu uzatan ve daha çok işli başlığın gövdesi aynı eşten getblock
// ile alınır (bir tur daha). Ebeveyni bilinmeyen başlık PoW'dan önce, bedavaya reddedilir:
// senkronizasyonu bu ve daha çok işli başka dal tetikler, tetik bütün eşler için tipSyncFloor'da
// en çok bir kezdir. Ağ hatası, eski sürümlü eş (bilinmeyen türü yanıtsız kapatır) ve geçersiz
// yanıt üstel ertelemeye girer.
const (
	msgWaitTip = "waittip" // Hash: eşin bildiğimiz ucu (ilk istekte boş) → tip
	msgTip     = "tip"     // Headers: eşin uç başlığı; boşsa eş şimdi bekletemiyor

	// tipWaitHold transferTimeout'tan (istemcinin istek süresi) kısa olmalıdır.
	tipWaitHold    = 25 * time.Second
	tipWaitRecheck = time.Second // uç bildirimi kaçarsa yedek denetim

	// Bekleme havuzu: açık pay 24, bağlantıların açık payı (48) dolmaz; 4'ü yalnızca
	// yapılandırılmış eşlere.
	maxConcurrentWaits = 32
	reservedWaits      = 8
	waitsPerGroup      = 1

	maxTipMessageBytes = 4 << 10 // tek başlıklı yanıt ~0,5 KiB

	tipPollScan       = 5 * time.Second // sonradan eklenen yapılandırılmış eşler bu aralıkla fark edilir
	tipPollMinGap     = time.Second
	tipPollBusyDelay  = syncInterval
	tipPollBackoffMin = 5 * time.Second
	tipPollBackoffMax = 5 * time.Minute
	tipSyncFloor      = 10 * time.Second
)

// tipOutcome bir uç isteğinin sonucudur ve sonraki isteğin ne zaman açılacağını belirler:
// tipNeutral (geçerli yanıt, ilerleme yok) tipPollMinGap sonra; tipProgress (blok alındı ya da
// eşin ucu bekleme boyunca değişmedi) ertelemeyi de sıfırlar; tipBusy (eş destekliyor ama şimdi
// bekletemiyor) tipPollBusyDelay sonra, ertelemeyi büyütmeden; tipFailed (ağ hatası, eski
// sürümlü eş, geçersiz yanıt) üstel ertelemeyle.
type tipOutcome int

const (
	tipNeutral tipOutcome = iota
	tipProgress
	tipBusy
	tipFailed
)

// tipSignal uç bildirim kanalını ve kapanış kanalını döndürür.
func (ps *P2PServer) tipSignal() (<-chan struct{}, <-chan struct{}) {
	ps.tipMu.Lock()
	defer ps.tipMu.Unlock()
	return ps.tipCh, ps.quit
}

// notifyTip uç bekleyenleri uyandırır.
func (ps *P2PServer) notifyTip() {
	ps.tipMu.Lock()
	defer ps.tipMu.Unlock()
	close(ps.tipCh)
	ps.tipCh = make(chan struct{})
}

func (ps *P2PServer) setQuit(q <-chan struct{}) {
	ps.tipMu.Lock()
	defer ps.tipMu.Unlock()
	ps.quit = q
}

// serveWaitTip ucumuz known'dan farklı olunca ya da tipWait dolunca uç başlığıyla yanıtlar.
// Bekleme yuvası ya da grup kotası doluysa, ya da düğüm en az zincir işinin altındaysa hemen
// boş msgTip döner. Sunucu kapanırsa yanıtsız döner.
func (ps *P2PServer) serveWaitTip(conn net.Conn, remote string, id peerID, known string) {
	if known != "" && !core.IsHashHex(known) {
		ps.log.Debug("geçersiz özet", "remote", remote)
		return
	}
	if ps.Blockchain.BelowMinChainWork() || !ps.waitQuota.tryAcquire(id.group) {
		ps.reply(conn, remote, message{Type: msgTip})
		return
	}
	defer ps.waitQuota.release(id.group)
	lvl := ps.prot.levelOf(id)
	if !ps.waits.tryAcquire(lvl) {
		// Debug: korunmayan pay saldırgan denetiminde dolabilir, log taşkını olmasın.
		ps.log.Debug("uç bekleme yuvaları dolu", "remote", remote, "duzey", int(lvl))
		ps.reply(conn, remote, message{Type: msgTip})
		return
	}
	defer ps.waits.release(lvl)
	hold := time.NewTimer(ps.tipWait)
	defer hold.Stop()
	recheck := time.NewTicker(tipWaitRecheck)
	defer recheck.Stop()
	expired := false
	for {
		// Bildirim kanalı uç okunmadan önce alınır: arada değişen uç kaçmaz.
		changed, quit := ps.tipSignal()
		h := ps.Blockchain.TipHeader()
		if h.Hash != known || expired {
			ps.reply(conn, remote, message{Type: msgTip, Headers: []core.BlockHeader{h}})
			return
		}
		select {
		case <-changed:
		case <-recheck.C:
		case <-hold.C:
			expired = true
		case <-quit:
			return
		}
	}
}

// tipPollLoop her etkin eş (yapılandırılmış eşler ve yedekler, backup.go) için bir uç bekleme
// döngüsü başlatır; eklenen eşler tipPollScan içinde fark edilir, çıkan eşin döngüsü kendi
// durdurma kanalıyla durur. Döngü sayısı etkin eş sayısıyla sınırlıdır.
func (ps *P2PServer) tipPollLoop(stop <-chan struct{}) {
	started := map[string]chan struct{}{}
	defer func() {
		for _, c := range started {
			close(c)
		}
	}()
	t := time.NewTicker(tipPollScan)
	defer t.Stop()
	for {
		live := map[string]bool{}
		for _, p := range ps.activePeers() {
			live[p] = true
			if started[p] == nil {
				c := make(chan struct{})
				started[p] = c
				go ps.pollPeerTip(p, c)
			}
		}
		for p, c := range started {
			if !live[p] {
				close(c)
				delete(started, p)
			}
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// pollPeerTip bir eşe art arda uç isteği açar; aralık son isteğin sonucuna bağlıdır (tipOutcome).
// "Meşgul" yanıtı yalnızca aralığı uzatır, yedeği değiştirmez: sunucunun bekleme kotası istemci
// grubuna bağlıdır, aynı gruptaki üçüncü taraf onu doldurabilir (ES-KESFI.md §4.4).
func (ps *P2PServer) pollPeerTip(peer string, stop <-chan struct{}) {
	known, fails := "", 0
	for {
		start := time.Now()
		var out tipOutcome
		known, out = ps.pollTip(peer, known, stop)
		wait := tipPollMinGap - time.Since(start)
		switch out {
		case tipProgress:
			fails = 0
		case tipBusy:
			wait = tipPollBusyDelay
		case tipFailed:
			fails++
			wait = tipBackoff(fails)
		}
		t := time.NewTimer(max(wait, 0))
		select {
		case <-stop:
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// tipBackoff art arda fails başarısız istekten sonraki beklemedir: tipPollBackoffMin'den
// başlayıp ikiye katlanır, en çok tipPollBackoffMax.
func tipBackoff(fails int) time.Duration {
	d := tipPollBackoffMin
	for i := 1; i < fails && d < tipPollBackoffMax; i++ {
		d *= 2
	}
	return min(d, tipPollBackoffMax)
}

// pollTip eşe bir uç isteği gönderip yanıtı işler; eşin bildiğimiz yeni ucunu ve sonucu döndürür.
func (ps *P2PServer) pollTip(peer, known string, stop <-chan struct{}) (string, tipOutcome) {
	resp, err := ps.request(peer, message{Type: msgWaitTip, Hash: known}, maxTipMessageBytes)
	if err != nil {
		ps.log.Debug("uç isteği başarısız", "peer", peer, "err", err)
		return known, tipFailed
	}
	if resp.Type != msgTip || len(resp.Headers) > 1 {
		ps.log.Debug("uç isteğine beklenmeyen yanıt", "peer", peer, "type", resp.Type, "adet", len(resp.Headers))
		return known, tipFailed
	}
	if len(resp.Headers) == 0 {
		return known, tipBusy
	}
	h := resp.Headers[0]
	if !core.IsHashHex(h.Hash) {
		ps.log.Debug("uç isteğine geçersiz özet", "peer", peer)
		return known, tipFailed
	}
	if h.Hash == known {
		return known, tipProgress // bekleme süresi doldu, eşin ucu değişmedi
	}
	select {
	case <-stop:
		return h.Hash, tipNeutral
	default:
	}
	return h.Hash, ps.adoptTip(peer, h)
}

// adoptTip eşin yeni uç başlığını değerlendirir. Başlık ana zincirimizdeyse bir şey yapılmaz.
// Değilse önce doğrulanır; ucumuzu uzatan ve daha çok işli başlığın gövdesi aynı eşten alınır.
// Gövde HeaderSync.Missing'ten geçmez: yalnızca uca eklenen blok indirilir, yan zincir izinlerine
// (senkronizasyon döngüsünün oturumuna) dokunulmaz.
func (ps *P2PServer) adoptTip(peer string, h core.BlockHeader) tipOutcome {
	bc := ps.Blockchain
	if _, ok := bc.HeaderByHash(h.Hash); ok {
		return tipNeutral
	}
	s := bc.NewHeaderSync()
	s.SetLimit(1)
	if _, err := s.Add([]core.BlockHeader{h}); err != nil {
		switch {
		case errors.Is(err, core.ErrBadPrevHash):
			// Eş birden çok blok önde ya da başka dalda: atalar senkronizasyonla gelir. Bu başlık
			// PoW'dan önce, bedavaya reddedilir; yalnızca yapılandırılmış eşinki tetikler (yedek ve
			// çıkarılmış yedeğin durmakta olan döngüsü tetiklemez; ES-KESFI.md §4.4).
			if ps.isConfigured(peer) {
				ps.triggerTipSync(time.Now())
			}
			return tipNeutral
		case errors.Is(err, core.ErrTimestampFuture):
			return tipNeutral // geçici; blok senkronizasyonla gelir
		default:
			if neverHonestBlock(err) {
				ps.noteViolation(peer, err)
			}
			ps.log.Debug("eşin uç başlığı geçersiz", "peer", peer, "err", err)
			return tipFailed
		}
	}
	if !s.MoreWork() {
		return tipNeutral
	}
	if h.PrevHash != bc.TipHash() {
		// Daha çok işli başka dal (başlık gerçek PoW taşır): yeniden düzenleme senkronizasyonla.
		ps.triggerTipSync(time.Now())
		return tipNeutral
	}
	resp, err := ps.request(peer, message{Type: msgGetBlock, Hash: h.Hash}, maxBlockMessageBytes)
	if err != nil {
		ps.log.Debug("uç bloğu indirilemedi", "peer", peer, "err", err)
		return tipFailed
	}
	if resp.Type != msgBlock || resp.Block == nil || resp.Block.Hash != h.Hash {
		ps.log.Debug("uç bloğu isteğine beklenmeyen yanıt", "peer", peer)
		return tipFailed
	}
	// Tüm uzlaşma kuralları AcceptBlock'ta uygulanır.
	switch err := bc.AcceptBlock(resp.Block); {
	case err == nil:
		ps.log.Info("uç beklemesiyle blok alındı", "peer", peer, "index", resp.Block.Index, "hash", h.Hash)
		if bc.TipHash() == h.Hash {
			ps.relayFrom(resp.Block, peer) // eş bloğu zaten biliyor
		}
		return tipProgress
	case errors.Is(err, core.ErrKnownBlock):
		return tipNeutral
	case bc.TipHash() != h.PrevHash:
		// Uç bu arada (senkronizasyonla) ilerledi, blok yan dala düştü: eşin hatası değil.
		ps.log.Debug("uç beklemesiyle alınan blok uca eklenemedi", "peer", peer, "err", err)
		return tipNeutral
	default:
		if neverHonestBlock(err) {
			ps.noteViolation(peer, err)
		}
		ps.log.Debug("uç beklemesiyle alınan blok reddedildi", "peer", peer, "err", err)
		return tipFailed
	}
}

// tipEpoch tetik zamanlarının başlangıcıdır; aradaki süre monoton saatle ölçülür, duvar saati
// geri atlasa da taban çalışır.
var tipEpoch = time.Now()

// triggerTipSync senkronizasyonu tetikler; bütün eşler için tipSyncFloor içinde en çok bir kez.
func (ps *P2PServer) triggerTipSync(now time.Time) {
	at := int64(now.Sub(tipEpoch)) + 1 // 0 "hiç tetiklenmedi" demektir
	last := ps.lastTipSync.Load()
	if last != 0 && at-last < int64(tipSyncFloor) {
		return
	}
	if ps.lastTipSync.CompareAndSwap(last, at) {
		ps.TriggerSync()
	}
}
