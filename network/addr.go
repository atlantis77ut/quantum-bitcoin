package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"quantum-bitcoin/core"
)

// Adres öğrenme (docs/ES-KESFI.md §4.2–4.3, §4.6, §5 madde 3; kullanıcı kararları 2026-11-14;
// düğüm politikası, uzlaşma kuralı değil). Keşif lansmanda yedek roldedir; bu dosya yalnızca
// öğrenir: öğrenilen adresler hiçbir giden bağlantıda kullanılmaz (yedek kullanım ayrı PR).
//
// Adres yalnızca güvenilen eşlerden öğrenilir: yapılandırılmış eşler ve gömülü başlangıç listesi
// her addrEvery'de bir `getaddr` ile sorulur. Yanıtlayan, kendi yapılandırılmış eşlerinden son
// addrReachedWindow içinde bir isteğine yanıt aldıklarını verir (bir atlama; itme başarısı
// sayılmaz, çünkü tampona sığan itme kimse okumasa da başarılı görünür). Verilen adres ulaşılan
// bağlantının IP'sidir, ad değildir. Yanıt en çok maxAddrsPerMsg adres ve yanıtlayanın genesis
// özetini taşır; özeti boş ya da sınırı aşan yanıt bütünüyle, başka ağın yanıtı uyarıyla
// reddedilir ve o kaynak addrWrongNetDelay ertelenir (yapılandırılmış eş geri çevrilmez; bozuk
// işaret yedek kullanımla gelir). Kendiliğinden gelen `addr` yok sayılır. Yalnızca IP ve port
// alınır, regtest dışında yalnızca yönlendirilebilir adresler.
//
// Tablo küçüktür (addrTableMax), kaynak başına en çok addrPerSource kayıt tutar; dolunca en
// eski görülen düşer, addrExpiry boyunca hiçbir kaynağın vermediği kayıt silinir. Tablo
// `network/`'ün sahibi olduğu ayrı bir LevelDB'de (zincir dizininin yanında `<db>_peers`)
// her değişiklikte tek batch'le yazılır; zincirin şema silinmesinden etkilenmez. Ağ ya da kayıt
// sürümü tutmazsa tablo uyarıyla sıfırlanır (ipucu önbelleğidir, durum değil).
//
// -discover=false (borsa profili) keşfi kapatır: düğüm `getaddr` sormaz, gelen isteğe adressiz
// yanıt verir (yapılandırılmış eşleri açığa çıkmaz). P2PServer'da keşif EnableDiscovery'ye kadar
// kapalıdır, ama main.go'da -discover varsayılan olarak açıktır: bayraksız güncellenen düğüm
// yapılandırılmış eşlerinin adreslerini vermeye başlar.
const (
	msgGetAddr = "getaddr" // Network (genesis özeti) → addr
	msgAddr    = "addr"

	maxAddrsPerMsg      = 32
	maxAddrMessageBytes = 8 << 10 // 32 × ~48 baytlık "[IPv6]:port" ve alanlar

	addrReachedWindow = 24 * time.Hour
	addrTableMax      = 256
	addrPerSource     = 32
	addrExpiry        = 7 * 24 * time.Hour

	addrInterval      = time.Hour
	addrScanInterval  = time.Minute
	addrBackoffMin    = 5 * time.Minute
	addrBackoffMax    = 6 * time.Hour
	addrWrongNetDelay = 24 * time.Hour

	addrBookVersion = 1
)

var errWrongNetwork = errors.New("eş başka bir ağda")

// seedPeers ağ başına ikiliye gömülü başlangıç listesidir (ES-KESFI.md §4.6; lansmanda DNS
// tohumu yok). -peer verilmezse yapılandırılmış eş yerine geçer, verilirse yalnızca adres
// kaynağıdır. Mainnet listesi lansmanda en az üç bağımsız işletmecinin düğümleriyle doldurulur
// ve imzalı sürüm notunda yayımlanır (MAINNET-ONCESI.md madde 6c).
var seedPeers = map[string][]string{
	"mainnet": nil,
	"testnet": nil,
	"regtest": nil,
}

// SeedPeers ağın gömülü başlangıç listesinin bir kopyasıdır.
func SeedPeers(network string) []string {
	return append([]string(nil), seedPeers[network]...)
}

// defaultPorts ağ başına varsayılan P2P portudur (kullanıcı kararı 2026-11-14): aynı makinedeki
// testnet ve mainnet düğümleri ayrı P2P portlarında dinler. API portu (-api) ağdan bağımsızdır;
// aynı makinedeki ikinci düğüme ayrıca verilmelidir.
var defaultPorts = map[string]string{
	"mainnet": "8001",
	"testnet": "18001",
	"regtest": "28001",
}

// DefaultPort ağın varsayılan P2P portudur; bilinmeyen ağda boş.
func DefaultPort(network string) string {
	return defaultPorts[network]
}

// unroutable regtest dışında öğrenilmeyen özel amaçlı adres bloklarıdır (IANA özel amaçlı adres
// kayıtları, RFC 6890 ve sonrası): yazılımın kendisi, özel, paylaşımlı, yerel bağlantı,
// dokümantasyon, kıyaslama, çok noktaya yayın, ayrılmış; IPv4 uyumlu ve SIIT (::ffff:0:0:0/96)
// biçimleri ile yerel NAT64 (64:ff9b:1::/48). 2001::/23 içindeki küresel atamalar (Teredo, AMT,
// AS112) öğrenilebilir.
var unroutable = cidrs(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/96", "::ffff:0:0:0/96", "64:ff9b:1::/48", "100::/64", "2001:2::/48", "2001:10::/28",
	"2001:20::/28", "2001:db8::/32", "3fff::/20", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
)

// embeddedV4 IPv4 adresi gömen IPv6 biçimleridir: NAT64 (64:ff9b::/96, RFC 6052; son 4 bayt) ve
// 6to4 (2002::/16, RFC 3056; 2.–5. baytlar). Gömülü adres yönlendirilemezse IPv6 adresi de
// öğrenilmez; yoksa bir kaynak düğümü ileride bu köprülerle iç ağ adreslerine yönlendirebilirdi.
var embeddedV4 = []struct {
	block *net.IPNet
	off   int
}{
	{cidrs("64:ff9b::/96")[0], 12},
	{cidrs("2002::/16")[0], 2},
}

func cidrs(list ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(list))
	for _, c := range list {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets = append(nets, n)
	}
	return nets
}

// isUnroutable ip'nin (4 ya da 16 bayt) özel amaçlı bir blokta olup olmadığıdır; gömülü IPv4
// taşıyan biçimlerde gömülü adres de denetlenir.
func isUnroutable(ip net.IP) bool {
	for _, n := range unroutable {
		if n.Contains(ip) {
			return true
		}
	}
	if len(ip) == net.IPv6len {
		for _, e := range embeddedV4 {
			if e.block.Contains(ip) {
				return isUnroutable(net.IP(ip[e.off : e.off+net.IPv4len]))
			}
		}
	}
	return false
}

// canonicalAddr ağdan ya da diskten gelen adresi denetler ve "ip:port" biçimine getirir (IPv4
// eşlemeli IPv6 → IPv4). Yalnızca IP (ad ve bölge kimliği değil), 1–65535 port; allowLocal
// (regtest) değilse yalnızca yönlendirilebilir adresler.
func canonicalAddr(s string, allowLocal bool) (string, bool) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", false
	}
	ip := net.ParseIP(host)
	p, err := strconv.ParseUint(port, 10, 16)
	if ip == nil || err != nil || p == 0 {
		return "", false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return "", false
	}
	if !allowLocal && isUnroutable(ip) {
		return "", false
	}
	return net.JoinHostPort(ip.String(), strconv.FormatUint(p, 10)), true
}

// addrEntry tablodaki bir adrestir. Zamanlar Unix saniyesidir; LastSuccess, LastFailure ve
// BadUntil yedek kullanımla dolar (ES-KESFI.md §4.3).
type addrEntry struct {
	Addr        string `json:"addr"`
	Source      string `json:"source"` // adresi en son veren güvenilen eş
	FirstSeen   int64  `json:"first_seen"`
	LastSeen    int64  `json:"last_seen"` // bir kaynağın en son verdiği an
	LastSuccess int64  `json:"last_success,omitempty"`
	LastFailure int64  `json:"last_failure,omitempty"`
	BadUntil    int64  `json:"bad_until,omitempty"`
}

// addrMeta tablonun ait olduğu ağ ve kayıt biçimidir.
type addrMeta struct {
	Version int    `json:"version"`
	Network string `json:"network"` // genesis özeti
}

var (
	addrMetaKey   = []byte("meta")
	addrKeyPrefix = []byte("a/")
)

// AddrBook öğrenilen eş adresleri tablosudur; db nil ise yalnızca bellektedir.
type AddrBook struct {
	mu         sync.Mutex
	network    string // genesis özeti
	allowLocal bool
	entries    map[string]*addrEntry
	db         *leveldb.DB
	log        *slog.Logger
}

// MemoryAddrBook kalıcı olmayan bir tablodur (dizin açılamazsa ve testlerde).
func MemoryAddrBook(bc *core.Blockchain) *AddrBook {
	name, genesis := bc.Network()
	return &AddrBook{network: genesis, allowLocal: name == core.RegtestParams.Name,
		entries: map[string]*addrEntry{}, log: slog.Default().With("component", "addrbook")}
}

// OpenAddrBook path'teki tabloyu açar; ağ ya da sürüm tutmazsa uyarıyla sıfırlar. Geçersiz ya
// da süresi dolmuş kayıtlar yüklenmez.
func OpenAddrBook(path string, bc *core.Blockchain) (*AddrBook, error) {
	db, err := leveldb.OpenFile(path, nil)
	if err != nil {
		return nil, err
	}
	b := MemoryAddrBook(bc)
	b.db = db
	if err := b.load(time.Now()); err != nil {
		db.Close()
		return nil, err
	}
	return b, nil
}

// Close tabloyu kapatır; sonraki kayıtlar sessizce atlanır.
func (b *AddrBook) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.db == nil {
		return nil
	}
	err := b.db.Close()
	b.db = nil
	return err
}

func (b *AddrBook) load(now time.Time) error {
	raw, err := b.db.Get(addrMetaKey, nil)
	if err != nil && !errors.Is(err, leveldb.ErrNotFound) {
		return err
	}
	var meta addrMeta
	if err == nil {
		err = json.Unmarshal(raw, &meta)
	}
	if err != nil || meta.Version != addrBookVersion || meta.Network != b.network {
		if raw != nil {
			b.log.Warn("eş adresi tablosu başka bir ağa ya da sürüme ait; sıfırlanıyor",
				"surum", meta.Version, "ag", meta.Network)
		}
		return b.saveLocked()
	}
	it := b.db.NewIterator(util.BytesPrefix(addrKeyPrefix), nil)
	defer it.Release()
	var stored []*addrEntry
	keys := 0
	for it.Next() {
		keys++
		var e addrEntry
		addr, ok := canonicalAddr(string(it.Key()[len(addrKeyPrefix):]), b.allowLocal)
		if json.Unmarshal(it.Value(), &e) != nil || !ok || e.Addr != addr || e.Source == "" {
			b.log.Debug("geçersiz eş adresi kaydı atlandı", "anahtar", string(it.Key()))
			continue
		}
		stored = append(stored, &e)
	}
	if err := it.Error(); err != nil {
		return err
	}
	// En yeni görülenler önce: sınırlar diskteki fazlalığı en eskilerden budar.
	sort.Slice(stored, func(i, j int) bool { return stored[i].LastSeen > stored[j].LastSeen })
	perSource := map[string]int{}
	for _, e := range stored {
		if now.Unix()-e.LastSeen > int64(addrExpiry/time.Second) || len(b.entries) >= addrTableMax ||
			perSource[e.Source] >= addrPerSource {
			continue
		}
		perSource[e.Source]++
		b.entries[e.Addr] = e
	}
	if len(b.entries) != keys {
		return b.saveLocked() // atlanan ve budanan kayıtlar diskten de silinir
	}
	return nil
}

// saveLocked tabloyu tek batch'le yazar (eski kayıtlar silinir). Kapalı tablo atlanır.
func (b *AddrBook) saveLocked() error {
	if b.db == nil {
		return nil
	}
	batch := new(leveldb.Batch)
	it := b.db.NewIterator(util.BytesPrefix(addrKeyPrefix), nil)
	for it.Next() {
		batch.Delete(append([]byte(nil), it.Key()...))
	}
	it.Release()
	if err := it.Error(); err != nil {
		return err
	}
	for addr, e := range b.entries {
		v, err := json.Marshal(e)
		if err != nil {
			return err
		}
		batch.Put(append(append([]byte(nil), addrKeyPrefix...), addr...), v)
	}
	meta, err := json.Marshal(addrMeta{Version: addrBookVersion, Network: b.network})
	if err != nil {
		return err
	}
	batch.Put(addrMetaKey, meta)
	return b.db.Write(batch, nil)
}

// learn src'nin verdiği adresleri tabloya işler ve yeni eklenen sayısını döndürür. known
// zaten yapılandırılmış (ya da gömülü) adresleri eler.
func (b *AddrBook) learn(src string, addrs []string, known func(string) bool, now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	changed := b.expireLocked(now)
	perSource := map[string]int{}
	for _, e := range b.entries {
		perSource[e.Source]++
	}
	added := 0
	seen := map[string]bool{}
	for _, raw := range addrs {
		addr, ok := canonicalAddr(raw, b.allowLocal)
		if !ok || seen[addr] || known(addr) {
			continue
		}
		seen[addr] = true
		if e, ok := b.entries[addr]; ok {
			// Kaynağın payı doluysa kayıt tazelenir ama kaynağı değişmez.
			if e.Source != src && perSource[src] < addrPerSource {
				perSource[e.Source]--
				perSource[src]++
				e.Source = src
			}
			e.LastSeen = now.Unix()
			changed = true
			continue
		}
		if perSource[src] >= addrPerSource {
			continue
		}
		if len(b.entries) >= addrTableMax {
			old := b.oldestLocked()
			perSource[b.entries[old].Source]--
			delete(b.entries, old)
		}
		b.entries[addr] = &addrEntry{Addr: addr, Source: src, FirstSeen: now.Unix(), LastSeen: now.Unix()}
		perSource[src]++
		added++
		changed = true
	}
	if changed {
		if err := b.saveLocked(); err != nil && !errors.Is(err, leveldb.ErrClosed) {
			b.log.Warn("eş adresi tablosu kaydedilemedi", "err", err)
		}
	}
	return added
}

// expireLocked addrExpiry boyunca hiçbir kaynağın vermediği kayıtları siler.
func (b *AddrBook) expireLocked(now time.Time) bool {
	changed := false
	for addr, e := range b.entries {
		if now.Unix()-e.LastSeen > int64(addrExpiry/time.Second) {
			delete(b.entries, addr)
			changed = true
		}
	}
	return changed
}

// oldestLocked en eski görülen kaydın adresidir (tablo boş değilken).
func (b *AddrBook) oldestLocked() string {
	old := ""
	for addr, e := range b.entries {
		if old == "" || e.LastSeen < b.entries[old].LastSeen ||
			(e.LastSeen == b.entries[old].LastSeen && addr < old) {
			old = addr
		}
	}
	return old
}

// Len tablodaki adres sayısıdır.
func (b *AddrBook) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// snapshot tablonun kopyasıdır (testler ve loglar için).
func (b *AddrBook) snapshot() map[string]addrEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]addrEntry, len(b.entries))
	for k, e := range b.entries {
		out[k] = *e
	}
	return out
}

// reachedPeer bir yapılandırılmış eşe son başarılı isteğin anı ve ulaşılan adresidir.
type reachedPeer struct {
	addr string
	at   time.Time
}

// EnableDiscovery adres öğrenmeyi açar: düğüm yapılandırılmış eşlerini ve seeds'i `getaddr`
// ile sorar, gelen `getaddr` isteklerini yanıtlar. Serve'den önce çağrılır.
func (ps *P2PServer) EnableDiscovery(book *AddrBook, seeds []string) {
	ps.discover = true
	ps.addrs = book
	ps.seeds = append([]string(nil), seeds...)
}

// noteReached eşe başarılı bir istek yapıldığını ve ulaşılan adresi kaydeder (request).
func (ps *P2PServer) noteReached(peer string, remote net.Addr, now time.Time) {
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	ps.reached[peer] = reachedPeer{addr: remote.String(), at: now}
}

// noteNetwork kaynağın `getaddr` yanıtındaki ağ kimliğinin sonucunu kaydeder: başka ağdaki
// yapılandırılmış eş, senkronizasyon yanıtlarıyla ulaşılmış görünse de adres yanıtında verilmez.
// Kaydı aynı ağın yanıtı siler; süresi yeniden sorma aralığının (addrWrongNetDelay) iki katıdır,
// böylece 24 sa sonraki soru ağ hatasıyla biterse eş hemen yeniden verilmez.
func (ps *P2PServer) noteNetwork(src string, wrong bool, now time.Time) {
	ps.reachedMu.Lock()
	defer ps.reachedMu.Unlock()
	for s, until := range ps.wrongNet {
		if !now.Before(until) {
			delete(ps.wrongNet, s)
		}
	}
	if wrong {
		ps.wrongNet[src] = now.Add(2 * addrWrongNetDelay)
	} else {
		delete(ps.wrongNet, src)
	}
}

// reachedAddrs son addrReachedWindow içinde isteğine yanıt alınmış yapılandırılmış eşlerin
// ulaşılan adresleridir: en çok maxAddrsPerMsg, karışık sırada. Gömülü liste (-peer verilmişse),
// öğrenilen adresler ve başka ağda olduğu görülen eşler verilmez.
func (ps *P2PServer) reachedAddrs(now time.Time) []string {
	peers := ps.Peers()
	seen := map[string]bool{}
	var out []string
	ps.reachedMu.Lock()
	for _, p := range peers {
		r, ok := ps.reached[p]
		if !ok || now.Sub(r.at) > addrReachedWindow {
			continue
		}
		if until, wrong := ps.wrongNet[p]; wrong && now.Before(until) {
			continue
		}
		if a, ok := canonicalAddr(r.addr, ps.localAddrs); ok && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	ps.reachedMu.Unlock()
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	if len(out) > maxAddrsPerMsg {
		out = out[:maxAddrsPerMsg]
	}
	return out
}

// serveGetAddr adres isteğini yanıtlar. Yanıt her zaman genesis özetimizi taşır; keşif
// kapalıysa ya da istek başka ağdansa adres verilmez. Sunum yuvası ve grup kotasıyla sınırlıdır.
func (ps *P2PServer) serveGetAddr(conn net.Conn, remote string, id peerID, network string) {
	ok, lvl := ps.acquireServe(id)
	if !ok {
		return
	}
	defer ps.releaseServe(id, lvl)
	_, genesis := ps.Blockchain.Network()
	resp := message{Type: msgAddr, Network: genesis}
	if ps.discover && network == genesis {
		resp.Addrs = ps.reachedAddrs(time.Now())
	}
	ps.reply(conn, remote, resp)
}

// addrSources sorulacak güvenilen eşlerdir: yapılandırılmış eşler ve gömülü liste.
func (ps *P2PServer) addrSources() []string {
	srcs := ps.Peers()
	for _, s := range ps.seeds {
		dup := false
		for _, p := range srcs {
			if p == s {
				dup = true
				break
			}
		}
		if !dup {
			srcs = append(srcs, s)
		}
	}
	return srcs
}

// knownAddrs yapılandırılmış eşlerin ve gömülü listenin IP biçimli adresleridir (öğrenilmez).
func (ps *P2PServer) knownAddrs() map[string]bool {
	known := map[string]bool{}
	for _, s := range ps.addrSources() {
		if a, ok := canonicalAddr(s, true); ok {
			known[a] = true
		}
	}
	return known
}

// askAddrs src'ye `getaddr` sorar ve yanıtı tabloya işler; yeni eklenen sayısını döndürür.
func (ps *P2PServer) askAddrs(src string, now time.Time) (int, error) {
	_, genesis := ps.Blockchain.Network()
	resp, err := ps.request(src, message{Type: msgGetAddr, Network: genesis}, maxAddrMessageBytes)
	if err != nil {
		return 0, err
	}
	switch {
	case resp.Type != msgAddr || resp.Network == "":
		return 0, errBadResponse
	case resp.Network != genesis:
		ps.noteNetwork(src, true, now)
		return 0, fmt.Errorf("%w: %s", errWrongNetwork, resp.Network)
	}
	ps.noteNetwork(src, false, now)
	if len(resp.Addrs) > maxAddrsPerMsg {
		return 0, fmt.Errorf("%w: %d adres", errBadResponse, len(resp.Addrs))
	}
	known := ps.knownAddrs()
	return ps.addrs.learn(src, resp.Addrs, func(a string) bool { return known[a] }, now), nil
}

// addrLoop güvenilen eşleri sırayla sorar: başarıda addrEvery sonra, hatada addrBackoffMin'den
// addrBackoffMax'a ikiye katlanan erteleme, başka ağda addrWrongNetDelay. Eski sürümlü eş
// (bilinmeyen türü yanıtsız kapatır) ağ hatası gibi ertelenir.
func (ps *P2PServer) addrLoop(stop <-chan struct{}) {
	t := time.NewTicker(ps.addrScanEvery)
	defer t.Stop()
	next := map[string]time.Time{}
	backoff := map[string]time.Duration{}
	for {
		now := time.Now()
		srcs := ps.addrSources()
		live := map[string]bool{}
		for _, src := range srcs {
			live[src] = true
			if now.Before(next[src]) {
				continue
			}
			select {
			case <-stop:
				return
			default:
			}
			added, err := ps.askAddrs(src, time.Now())
			switch {
			case err == nil:
				backoff[src] = 0
				next[src] = time.Now().Add(ps.addrEvery)
				if added > 0 {
					ps.log.Info("eş adresleri öğrenildi", "kaynak", src, "yeni", added, "toplam", ps.addrs.Len())
				}
			case errors.Is(err, errWrongNetwork):
				ps.log.Warn("adres kaynağı başka bir ağda; erteleniyor", "kaynak", src, "err", err,
					"sure", addrWrongNetDelay)
				next[src] = time.Now().Add(addrWrongNetDelay)
			default:
				d := min(max(2*backoff[src], addrBackoffMin), addrBackoffMax)
				backoff[src] = d
				next[src] = time.Now().Add(d)
				ps.log.Debug("adres kaynağı sorulamadı", "kaynak", src, "err", err, "erteleme", d)
			}
		}
		for src := range next {
			if !live[src] {
				delete(next, src)
				delete(backoff, src)
			}
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
