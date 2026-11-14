package network

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"slices"
	"time"
)

// Yedek kullanım (docs/ES-KESFI.md §4.1, §4.3–4.4, §5 madde 4; düğüm politikası, uzlaşma kuralı
// değil). Yapılandırılmış eşlerin hiçbirine backupLimits.after boyunca başarılı bir istek
// yapılamazsa düğüm adres tablosundan (ve -peer verilmişse gömülü listeden) en çok
// backupLimits.max yedek eş seçer. Yedekler senkronizasyon, uç beklemesi, blok ve işlem itmesi ve
// havuz eşitlemesine katılır (activePeers). Bir yapılandırılmış eş backupLimits.exitAfter boyunca
// kesintisiz ulaşılır olunca yedekler bırakılır (histerezis).
//
// Yedekler korunmaz, adres kaynağı değildir ve `getaddr` yanıtında verilmez (bunlar yalnızca
// Peers() ile çalışır); kazıcının kabul denetimi de yalnızca yapılandırılmış eşlere sorar (gövde
// kanıtı yalnızca saklamayı gösterir; yedekler düşmüşken uyarı susmasın). Aday önce bir `getaddr`
// sınamasıyla denenir: istek bu sınamaya özgü rastgele bir belirteç taşır, kendi sunucumuz
// belirteci görürse aday kendimiziz. Sınama yanıtı yalnızca ağ kimliğiyle denetlenir, adresleri
// öğrenilmez. Yedekler arasında IPv4 /16 ve IPv6 /32 başına bir eş olur (regtest'te sınır yok:
// bütün düğümler aynı makinededir).
//
// Taşıma hataları (bağlantı kurulamaması, zaman aşımı) işaret koymaz, yedeği yalnızca değiştirir:
// backupLimits.stale boyunca başarılı isteği olmayan yedek çıkarılır ve son başarısızlığı yazılır;
// aday backupLimits.retry sonra yeniden denenir. Uç beklemesindeki "meşgul" yanıtı yedeği
// değiştirmez (tippoll.go). Bozuk işaret (backupLimits.badFor) yalnızca durumdan
// bağımsız, kanıtlı ihlalde konur: başka ağ kimliği, JSON sözdizimi bozuk yanıt
// (malformedMessage) ya da dürüst eşin göndermeyeceği başlık veya blok (neverHonestBlock).
// Yapılandırılmış eşlere eskisi gibi hiçbir işaret konmaz.

// backupLimits yedek kullanım eşikleridir; Serve'den önce ayarlanır (testler kısaltır).
type backupLimits struct {
	after     time.Duration // yapılandırılmış eşlere bu kadar ulaşılamayınca yedekler devreye girer
	exitAfter time.Duration // yapılandırılmış eş bu kadar kesintisiz ulaşılır olunca yedekler çıkar
	fresh     time.Duration // son başarısı bundan yeni yapılandırılmış eş ulaşılır sayılır
	stale     time.Duration // bu kadar başarılı isteği olmayan yedek değiştirilir
	retry     time.Duration // başarısız aday bu kadar sonra yeniden denenir
	badFor    time.Duration // kanıtlı ihlalin bozuk işaret süresi
	scan      time.Duration // yönetici döngüsünün aralığı
	max       int           // en çok yedek eş
	probes    int           // tarama başına en çok aday sınaması (bayat tablo döngüyü tutmasın)
}

func defaultBackupLimits() backupLimits {
	return backupLimits{
		after:     10 * time.Minute,
		exitAfter: 10 * time.Minute,
		fresh:     2 * time.Minute,
		stale:     5 * time.Minute,
		retry:     30 * time.Minute,
		badFor:    24 * time.Hour,
		scan:      30 * time.Second,
		max:       4,
		probes:    8,
	}
}

// backupSuccessWrite yedeğin son başarısının diske yazılma aralığıdır: başarı her taramada
// bellekte güncellenir, tablo yalnızca bu kadar ilerleyince yeniden yazılır.
const backupSuccessWrite = 30 * time.Minute

// maxSelfAddrs kendimiz olduğu görülen adreslerin tavanıdır: sınama belirtecini bize geri ileten
// her yeni aday kalıcı olarak eklenmesin. Tavan doluysa aday yine tablodan çıkar (gömülü liste eşi
// retry boyunca denenmez); kaynak onu yeniden verirse yeniden sınanır (tarama başına en çok
// backupLimits.probes sınama).
const maxSelfAddrs = 64

var (
	errSelfConnect  = errors.New("aday kendi düğümümüz")
	errPeerInactive = errors.New("eş etkin kümeden çıktı")
)

// seedMark gömülü liste adayının (tabloda olmayan) başarısızlık ve bozuk işaretidir.
type seedMark struct {
	failed   time.Time
	badUntil time.Time
}

// backupMode erişilemezlik tespiti ve histerezistir; yalnızca yönetici döngüsünden kullanılır.
type backupMode struct {
	active  bool
	upSince time.Time // yapılandırılmış eşlerin yeniden ulaşılır görüldüğü an (etkinken)
}

// update yapılandırılmış eşlere son başarılı isteğin anına (lastOK) göre durumu günceller ve
// yedeklerin gerekip gerekmediğini döndürür.
func (m *backupMode) update(now, lastOK time.Time, l *backupLimits) bool {
	if !m.active {
		if now.Sub(lastOK) >= l.after {
			m.active, m.upSince = true, time.Time{}
		}
		return m.active
	}
	if now.Sub(lastOK) >= l.fresh {
		m.upSince = time.Time{}
		return true
	}
	if m.upSince.IsZero() {
		m.upSince = now
	}
	if now.Sub(m.upSince) >= l.exitAfter {
		m.active = false
	}
	return m.active
}

// activePeers senkronizasyonun, uç beklemesinin, havuz eşitlemesinin ve yayının kullandığı
// eşlerdir: yapılandırılmış eşler, sonra etkin yedekler.
func (ps *P2PServer) activePeers() []string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]string, 0, len(ps.peers)+len(ps.backups))
	out = append(out, ps.peers...)
	return append(out, ps.backups...)
}

// backupList etkin yedeklerin bir kopyasıdır.
func (ps *P2PServer) backupList() []string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return append([]string(nil), ps.backups...)
}

// isBackup eşin etkin bir yedek olup olmadığını söyler.
func (ps *P2PServer) isBackup(peer string) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return slices.Contains(ps.backups, peer)
}

// isConfigured eşin yapılandırılmış olup olmadığını söyler. Yedeğe özgü kurallar bunu kullanır,
// isBackup'ı değil: çıkarılan yedeğin süren isteği ya da durmakta olan döngüsü de yedek sayılır.
func (ps *P2PServer) isConfigured(peer string) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return slices.Contains(ps.peers, peer)
}

// addBackup adayı etkin yedeklere ekler; yer yoksa ya da zaten eşse false.
func (ps *P2PServer) addBackup(addr string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.backups) >= ps.backupLim.max || slices.Contains(ps.backups, addr) ||
		slices.Contains(ps.peers, addr) {
		return false
	}
	ps.backups = append(ps.backups, addr)
	return true
}

// removeBackup yedeği etkin kümeden çıkarır ve gönderim kuyruğunu boşaltır; yedek değilse false.
// Diğer eş başına durum sahibinde temizlenir: senkronizasyon durumu tur başında, uç bekleme
// döngüsü taramasında.
func (ps *P2PServer) removeBackup(peer string) bool {
	ps.mu.Lock()
	i := slices.Index(ps.backups, peer)
	if i >= 0 {
		ps.backups = slices.Delete(ps.backups, i, i+1)
	}
	ps.mu.Unlock()
	if i < 0 {
		return false
	}
	ps.discardSender(peer)
	return true
}

// clearBackups bütün yedekleri bırakır ve sayısını döndürür.
func (ps *P2PServer) clearBackups() int {
	ps.mu.Lock()
	old := ps.backups
	ps.backups = nil
	ps.mu.Unlock()
	for _, b := range old {
		ps.discardSender(b)
	}
	return len(old)
}

// retireBackup taşıma hatası yüzünden yedeği değiştirir: işaret konmaz, aday retry sonra yeniden
// denenebilir.
func (ps *P2PServer) retireBackup(peer, reason string) {
	if ps.removeBackup(peer) {
		ps.candidateFailed(peer, time.Now())
		ps.log.Info("yedek eş değiştiriliyor", "peer", peer, "neden", reason)
	}
}

// noteViolation eşin durumdan bağımsız, kanıtlı ihlalini işler: eş yedekse bozuk işaretlenir ve
// hemen çıkarılır; yapılandırılmış eşe bir şey yapılmaz.
func (ps *P2PServer) noteViolation(peer string, err error) {
	if !ps.removeBackup(peer) {
		return
	}
	ps.candidateBad(peer, time.Now().Add(ps.backupLim.badFor))
	ps.log.Warn("yedek eş kanıtlı ihlal nedeniyle bırakıldı", "peer", peer, "err", err,
		"sure", ps.backupLim.badFor)
}

// candidateFailed adayın son başarısızlığını tabloya ya da gömülü liste işaretine yazar.
func (ps *P2PServer) candidateFailed(addr string, now time.Time) {
	if ps.addrs != nil {
		ps.addrs.markFailure(addr, now)
	}
	ps.probeMu.Lock()
	defer ps.probeMu.Unlock()
	if slices.Contains(ps.seeds, addr) {
		m := ps.seedMarks[addr]
		m.failed = now
		ps.seedMarks[addr] = m
	}
}

// candidateBad adaya until'e kadar bozuk işaret koyar.
func (ps *P2PServer) candidateBad(addr string, until time.Time) {
	if ps.addrs != nil {
		ps.addrs.markBad(addr, until)
	}
	ps.probeMu.Lock()
	defer ps.probeMu.Unlock()
	if slices.Contains(ps.seeds, addr) {
		m := ps.seedMarks[addr]
		m.badUntil = until
		ps.seedMarks[addr] = m
	}
}

// discardSender eşin gönderim kuyruğunu boşaltır; çalışanı yoksa durumu siler. Çalışan varsa
// durum kalır ve sonraki taramada silinir: yeniden eklenen eş aynı durumu kullanır, aynı eşe iki
// gönderim döngüsü olmaz (itme yuvaları eş dizgesiyle anahtarlıdır).
func (ps *P2PServer) discardSender(peer string) {
	ps.sendersMu.Lock()
	defer ps.sendersMu.Unlock()
	if s := ps.senders[peer]; s != nil && s.discard() {
		delete(ps.senders, peer)
	}
}

// pruneSenders etkin olmayan eşlerin gönderim durumunu boşaltır (anlık görüntüsü çıkarmadan önce
// alınmış bir yayın durumu yeniden oluşturmuş olabilir).
func (ps *P2PServer) pruneSenders(active []string) {
	live := setOf(active)
	ps.sendersMu.Lock()
	defer ps.sendersMu.Unlock()
	for p, s := range ps.senders {
		if !live[p] && s.discard() {
			delete(ps.senders, p)
		}
	}
}

// pruneSyncState eş başına senkronizasyon durumunu tur başında etkin kümeye göre budar
// (yalnızca senkronizasyon döngüsünden). Çıkan eşin süren ön-senkronizasyonu başarısız sayılır ve
// erteleme başlatır: eş kendini çıkarttırıp ertelemeyi atlayamasın. Ön-senkronizasyon ertelemesi
// budanmaz, çünkü yedek değişince sıfırlanmamalıdır (presyncKey); yalnızca yapılandırılmamış
// eşlerin süresi presyncBackoffMax'tan uzun süre önce dolmuş kayıtları silinir.
func (ps *P2PServer) pruneSyncState(active []string, now time.Time) {
	live := setOf(active)
	for p := range ps.presyncs {
		if !live[p] {
			ps.dropPresync(p, true, errPeerInactive)
		}
	}
	for p := range ps.lastMempoolSync {
		if !live[p] {
			delete(ps.lastMempoolSync, p)
		}
	}
	configured := setOf(ps.Peers())
	for k, pen := range ps.presyncBackoff {
		if !configured[k] && now.After(pen.until.Add(presyncBackoffMax)) {
			delete(ps.presyncBackoff, k)
		}
	}
}

// presyncKey ön-senkronizasyon ertelemesinin anahtarıdır: yapılandırılmış eşte eş dizgesi,
// diğerlerinde (yedek ya da çıkarılmış yedek) IP: aynı makinenin başka portları ertelemeyi
// sıfırlayamasın, yedek değişince ya da ihlalle çıkarılınca da sıfırlanmasın.
func (ps *P2PServer) presyncKey(peer string) string {
	if !ps.isConfigured(peer) {
		if host, _, err := net.SplitHostPort(peer); err == nil {
			return host
		}
	}
	return peer
}

func setOf(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// reachedAt eşe son başarılı isteğin anıdır; hiç yoksa sıfır.
func (ps *P2PServer) reachedAt(peer string) time.Time {
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	return ps.reached[peer].at
}

// configuredLastOK yapılandırılmış eşlere son başarılı isteğin anıdır; since'ten eskiyse since
// (açılışta yedekler hemen devreye girmesin).
func (ps *P2PServer) configuredLastOK(since time.Time) time.Time {
	last := since
	peers := ps.Peers()
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	for _, p := range peers {
		if at := ps.reached[p].at; at.After(last) {
			last = at
		}
	}
	return last
}

// pruneReached yalnızca adres kaynaklarının ve etkin yedeklerin ulaşılma kaydını tutar: sınanan
// adayların kayıtları silinir (yalnızca yönetici döngüsünden).
func (ps *P2PServer) pruneReached() {
	keep := setOf(ps.addrSources())
	for _, b := range ps.backupList() {
		keep[b] = true
	}
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	for p := range ps.reached {
		if !keep[p] {
			delete(ps.reached, p)
		}
	}
}

// backupGroup yedeklerin dağıtıldığı gruptur: IPv4 /16, IPv6 /32; regtest'te adresin kendisi, IP
// olmayan (gömülü listedeki ad) adreste ad.
func (ps *P2PServer) backupGroup(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		return host
	case ps.localAddrs:
		return addr
	case ip.To4() != nil:
		return hex.EncodeToString(ip.To4()[:2]) + "/16"
	default:
		return hex.EncodeToString(ip[:4]) + "/32"
	}
}

// backupExclusions yedek olamayacak adreslerdir: yapılandırılmış eşler (dizgeleri, kanonik
// biçimleri ve ulaşılan IP'leri: DNS adıyla yapılandırılmış eşin öğrenilmiş kopyası da elenir),
// gömülü liste eşlerinin ad yerine ulaşılan IP'leri (eşin kendisi aday kalır) ve kendimiz olduğu
// görülen adresler.
func (ps *P2PServer) backupExclusions() map[string]bool {
	ex := map[string]bool{}
	for _, p := range ps.Peers() {
		ex[p] = true
		if a, ok := canonicalAddr(p, true); ok {
			ex[a] = true
		}
	}
	srcs := ps.addrSources()
	ps.reachedMu.Lock()
	for _, s := range srcs {
		r, ok := ps.reached[s]
		if !ok {
			continue
		}
		a, ok := canonicalAddr(r.addr, true)
		if !ok {
			continue
		}
		if own, _ := canonicalAddr(s, true); a == own && !ex[s] {
			continue // IP biçimli gömülü liste eşinin kendisi
		}
		ex[a] = true
	}
	ps.reachedMu.Unlock()
	ps.probeMu.Lock()
	for a := range ps.selfAddrs {
		ex[a] = true
	}
	ps.probeMu.Unlock()
	return ex
}

// backupCandidates sırasıyla denenecek adaylardır: tablodakiler (adres öğrenme), sonra -peer
// verilmişse gömülü liste.
func (ps *P2PServer) backupCandidates(now time.Time) []string {
	out := ps.addrs.candidates(now, ps.backupLim.retry)
	configured := setOf(ps.Peers())
	ps.probeMu.Lock()
	defer ps.probeMu.Unlock()
	for _, s := range ps.seeds {
		m := ps.seedMarks[s]
		if configured[s] || now.Before(m.badUntil) || (!m.failed.IsZero() && now.Sub(m.failed) < ps.backupLim.retry) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// newProbeToken sınamaya özgü, tahmin edilemez bir belirteçtir.
func newProbeToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand Go 1.22'de yalnızca işletim sistemi kaynağı yoksa hata verir
	}
	return hex.EncodeToString(b[:])
}

// seeProbe kendi sınama belirtecimizi gören sunucu tarafıdır (handleConnection, sunum
// kotasından önce: kota dolu olsa da kendine bağlanma anlaşılır).
func (ps *P2PServer) seeProbe(token string) {
	if token == "" {
		return
	}
	ps.probeMu.Lock()
	defer ps.probeMu.Unlock()
	if _, ok := ps.probes[token]; ok {
		ps.probes[token] = true
	}
}

// probeBackup adayı bir `getaddr` sınamasıyla dener: kendimiz miyiz, aynı ağda mı, yanıt veriyor
// mu. Yanıttaki adresler öğrenilmez (aday güvenilen bir kaynak değildir).
func (ps *P2PServer) probeBackup(addr string) error {
	token := newProbeToken()
	ps.probeMu.Lock()
	ps.probes[token] = false
	ps.probeMu.Unlock()
	_, genesis := ps.Blockchain.Network()
	resp, err := ps.request(addr, message{Type: msgGetAddr, Network: genesis, Probe: token}, maxAddrMessageBytes)
	ps.probeMu.Lock()
	self := ps.probes[token]
	delete(ps.probes, token)
	ps.probeMu.Unlock()
	switch {
	case self:
		return errSelfConnect
	case err != nil:
		return err
	case resp.Type != msgAddr || resp.Network == "":
		return errBadResponse
	case resp.Network != genesis:
		return errWrongNetwork
	}
	return nil
}

// backupLoop yedek eş yöneticisidir (keşif açıkken): erişilemezlik tespiti, yedeklerin seçimi ve
// değişimi, çıkan eşlerin gönderim durumunun ve sınanan adayların ulaşılma kaydının budanması.
func (ps *P2PServer) backupLoop(stop <-chan struct{}) {
	lim := &ps.backupLim
	t := time.NewTicker(lim.scan)
	defer t.Stop()
	started := time.Now()
	var mode backupMode
	added := map[string]time.Time{}
	for {
		now := time.Now()
		was := mode.active
		switch active := mode.update(now, ps.configuredLastOK(started), lim); {
		case active:
			if !was {
				ps.log.Warn("hiçbir yapılandırılmış eşe ulaşılamıyor; yedek eşler devreye giriyor",
					"sure", lim.after)
			}
			ps.tendBackups(now, added, stop)
		case was:
			n := ps.clearBackups()
			clear(added)
			ps.log.Info("yapılandırılmış eşlere yeniden ulaşıldı; yedek eşler bırakıldı", "adet", n)
		}
		ps.pruneSenders(ps.activePeers())
		ps.pruneReached()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// tendBackups yanıtsız yedekleri değiştirir, yedeklerin son başarısını tabloya işler ve eksik
// yedekleri tamamlar.
func (ps *P2PServer) tendBackups(now time.Time, added map[string]time.Time, stop <-chan struct{}) {
	current := ps.backupList()
	live := setOf(current)
	for p := range added {
		if !live[p] {
			delete(added, p)
		}
	}
	for _, b := range current {
		at := ps.reachedAt(b)
		last := at
		if a := added[b]; a.After(last) {
			last = a
		}
		if now.Sub(last) >= ps.backupLim.stale {
			ps.retireBackup(b, "yanıt yok")
			continue
		}
		ps.addrs.markSuccess(b, at)
	}
	ps.fillBackups(added, stop)
}

// fillBackups eksik yedekleri adaylardan tamamlar: tarama başına en çok backupLimits.probes
// sınama, grup başına bir yedek.
func (ps *P2PServer) fillBackups(added map[string]time.Time, stop <-chan struct{}) {
	lim := &ps.backupLim
	current := ps.backupList()
	if len(current) >= lim.max {
		return
	}
	exclude := ps.backupExclusions()
	groups := map[string]bool{}
	for _, b := range current {
		exclude[b] = true
		groups[ps.backupGroup(b)] = true
	}
	probes := 0
	for _, c := range ps.backupCandidates(time.Now()) {
		if len(current) >= lim.max || probes >= lim.probes {
			return
		}
		select {
		case <-stop:
			return
		default:
		}
		g := ps.backupGroup(c)
		if exclude[c] || groups[g] {
			continue
		}
		exclude[c] = true
		probes++
		err := ps.probeBackup(c)
		now := time.Now()
		switch {
		case err == nil:
			if !ps.addBackup(c) {
				continue
			}
			current = append(current, c)
			groups[g] = true
			added[c] = now
			ps.addrs.markSuccess(c, now)
			ps.log.Info("yedek eş eklendi", "peer", c)
		case errors.Is(err, errSelfConnect):
			ps.addrs.remove(c)
			ps.candidateFailed(c, now) // gömülü liste eşi tavan doluysa da retry boyunca denenmez
			ps.probeMu.Lock()
			if len(ps.selfAddrs) < maxSelfAddrs {
				ps.selfAddrs[c] = true
			}
			ps.probeMu.Unlock()
			ps.log.Info("tablodaki adres kendi düğümümüz; çıkarıldı", "addr", c)
		case errors.Is(err, errWrongNetwork), malformedMessage(err):
			ps.candidateBad(c, now.Add(lim.badFor))
			ps.log.Warn("yedek adayı kanıtlı ihlal nedeniyle işaretlendi", "addr", c, "err", err,
				"sure", lim.badFor)
		default:
			ps.candidateFailed(c, now)
			ps.log.Debug("yedek adayına ulaşılamadı", "addr", c, "err", err)
		}
	}
}
