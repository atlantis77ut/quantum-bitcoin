package network

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"quantum-bitcoin/core"
)

// Korunan gruplar için ayrılmış yuvalar ve geri çevirme listesi (CLAUDE.md madde 5; kullanıcı
// kararı 2026-11-11). Her bağlantı tek mesaj taşıdığından tahliye edilecek uzun ömürlü eş
// yoktur; saldırı, çok sayıda adres grubunun ortak yuvaları (bağlantı, çözümleme, sunum)
// doldurmasıdır. Bu yüzden her ortak havuzun bir kısmı yalnızca korunan gruplara açıktır ve
// bunun da bir alt payı yalnızca yapılandırılmış eşlere: korunmayan çağıran havuzun, "paylaşılan"
// ve "açık" payın jetonlarını alır; blokla korunan havuzun ve paylaşılanın; yapılandırılmış eş
// yalnızca havuzun. Böylece korunmayan gruplar ne kadar çok olursa olsun ayrılan yuvalar boş
// kalır, blokla koruma kazanan gruplar da yapılandırılmış eşlerin alt payına giremez.
//
// Korunan gruplar:
//   - yapılandırılmış eşlerin (-peer) grupları (adres bir kez çözülür; sonraki DNS değişikliği
//     izlenmez);
//   - ucumuzu uzatan yeni bir bloğu ilk getiren son maxRecentBlockGroups grup (gerçek işle
//     taşınır; işlem göndermek koruma vermez). Blok başkasınca kazılmış olabilir: yeni blokları
//     hızla ileten herkes bu korumayı kazanabilir; bu yüzden kazanılan katman yapılandırılmış
//     eşlerin alt payını kullanamaz. Kazanılmış liste en yeniye göre güncellenir.
//
// Kazanılmış koruma ve geri çevirme iç anahtara (IPv4 /32, IPv6 /64) bağlıdır, yapılandırılmış
// eşler ve yuva payları dış gruba (innerkey.go): bir grup kazanılmış listede en çok bir yer
// tutar, korunan yalnızca bloğu getiren iç anahtardır.
//
// Geri çevirme: hiçbir dürüst eşin göndermeyeceği, durumdan bağımsız geçersiz girdi (PoW'u
// tutmayan, özeti ya da Merkle kökü yanlış, çok işlemli blok, JSON sözdizimi hatası) gönderen
// eşin bağlantıları discouragePeriod boyunca kabul anında, yuva kullanılmadan kapatılır.
// Ebeveyni bilinip beklenen hedefi taşımayan blok (ErrBadDifficulty) sürüm farkıyla dürüst eşten
// de gelebilir, kademeli cezalanır (badDifficulty). Grupta innerEscalateWindow içinde
// innerEscalateK farklı iç anahtar cezalanırsa bütün grup geri çevrilir. Ucumuza göre denetlenen
// yetim kapısı (ErrOrphanTooEasy; düğüm geride kalınca dürüst bloğa da çarpar), JSON tür
// hataları (sürüm farkı), durum hataları, zaman aşımı ve gelecek tarihli blok bu listeye girmez.
// Yapılandırılmış eşlerin grupları hiç geri çevrilmez.
const (
	reservedConns        = 16 // maxConcurrentConns (64) içinden; 8'i yalnızca yapılandırılmış eşlere
	reservedDecodes      = 3  // maxConcurrentDecodes (8) içinden; 2'si yalnızca yapılandırılmış eşlere
	reservedServes       = 6  // maxConcurrentServes (16) içinden; 3'ü yalnızca yapılandırılmış eşlere
	maxRecentBlockGroups = 8
	discouragePeriod     = time.Hour
	maxDiscouraged       = 10_000
	peerResolveTimeout   = 2 * time.Second
	badDifficultyPenalty = 10 * time.Minute // ErrBadDifficulty'nin ikinci ihlali; sonra ikiye katlanır
	strikeReset          = discouragePeriod // ihlalsiz bu süreden sonra ErrBadDifficulty sayacı sıfırlanır
)

// level bir adres grubunun korunma düzeyidir.
type level int

const (
	levelNone       level = iota // korunmayan
	levelEarned                  // ucumuzu uzatan yeni blok getirdi
	levelConfigured              // yapılandırılmış eş
)

// slotPool korunan gruplara ayrılmış paylı bir yuva havuzudur. Jetonlar dar paydan geniş
// havuza doğru alınır (open → shared → all); daha yüksek düzey yalnızca geniş olanları alır.
type slotPool struct {
	all    chan struct{} // toplam yuva
	shared chan struct{} // yapılandırılmış eşler dışındakilerin payı
	open   chan struct{} // korunmayanların payı (toplam − ayrılan)
}

// newSlotPool total yuvanın reserved kadarını korunanlara, bunun yarısını (yukarı yuvarlanmış)
// yalnızca yapılandırılmış eşlere ayırır.
func newSlotPool(total, reserved int) *slotPool {
	cfg := (reserved + 1) / 2
	return &slotPool{all: make(chan struct{}, total), shared: make(chan struct{}, total-cfg),
		open: make(chan struct{}, total-reserved)}
}

// chans düzeyin sırayla alması gereken jeton kanallarıdır.
func (p *slotPool) chans(l level) []chan struct{} {
	switch l {
	case levelConfigured:
		return []chan struct{}{p.all}
	case levelEarned:
		return []chan struct{}{p.shared, p.all}
	default:
		return []chan struct{}{p.open, p.shared, p.all}
	}
}

// tryAcquire beklemeden yuva alır.
func (p *slotPool) tryAcquire(l level) bool {
	cs := p.chans(l)
	for i, c := range cs {
		select {
		case c <- struct{}{}:
		default:
			for _, d := range cs[:i] {
				<-d
			}
			return false
		}
	}
	return true
}

// acquire yuva için en çok wait bekler.
func (p *slotPool) acquire(l level, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	cs := p.chans(l)
	for i, c := range cs {
		select {
		case c <- struct{}{}:
		case <-timer.C:
			for _, d := range cs[:i] {
				<-d
			}
			return false
		}
	}
	return true
}

// release acquire/tryAcquire ile alınan yuvayı, alındığı korunma düzeyiyle bırakır.
func (p *slotPool) release(l level) {
	for _, c := range p.chans(l) {
		<-c
	}
}

// protection korunan grupları, kazanılmış korumayı ve geri çevrilen eşleri tutar. Yapılandırılmış
// eşler dış grupla, kazanılmış koruma ve geri çevirme iç anahtarla tanınır (innerkey.go).
type protection struct {
	mu          sync.Mutex
	now         func() time.Time
	configured  map[string]bool      // dış grup
	recent      []peerID             // en yeni sonda; grup başına en çok bir kayıt, en çok maxRecentBlockGroups
	discouraged map[string]time.Time // iç anahtar ya da yükseltilmiş dış grup → bitiş
	strikes     map[string]*strike   // iç anahtar → ErrBadDifficulty ihlalleri
	esc         *escalator
}

type strike struct {
	n    int
	last time.Time // son ihlalin geri çevirmesinin bittiği an (uyarıda ihlal anı)
}

func newProtection() *protection {
	return &protection{now: time.Now, configured: map[string]bool{}, discouraged: map[string]time.Time{},
		strikes: map[string]*strike{}, esc: newEscalator()}
}

func (p *protection) levelOf(id peerID) level {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[id.group] {
		return levelConfigured
	}
	for _, r := range p.recent {
		if r == id {
			return levelEarned
		}
	}
	return levelNone
}

func (p *protection) isProtected(id peerID) bool { return p.levelOf(id) != levelNone }

func (p *protection) addConfigured(group string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.configured[group] = true
}

// noteBlock eşi ucumuzu uzatan yeni bir blok getiren en son eş yapar. Aynı dış gruptan önceki
// kayıt düşer: bir grup kazanılmış listede en çok bir yer tutar (hızlı ileten tek bir /24
// listeyi dolduramaz), koruma yalnızca bloğu getiren iç anahtarındır.
func (p *protection) noteBlock(id peerID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, r := range p.recent {
		if r.group == id.group {
			p.recent = append(p.recent[:i], p.recent[i+1:]...)
			break
		}
	}
	p.recent = append(p.recent, id)
	if len(p.recent) > maxRecentBlockGroups {
		p.recent = append([]peerID(nil), p.recent[len(p.recent)-maxRecentBlockGroups:]...)
	}
}

// discourage eşin iç anahtarını d boyunca geri çevirir; grupta innerEscalateWindow içinde
// innerEscalateK farklı iç anahtar cezalandıysa bütün grubu discouragePeriod boyunca.
// Yapılandırılmış eşin grubu hiç geri çevrilmez.
func (p *protection) discourage(id peerID, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[id.group] {
		return
	}
	now := p.now()
	p.setLocked(id.inner, now.Add(d), now)
	if p.esc.note(id, now) {
		p.setLocked(id.group, now.Add(discouragePeriod), now)
	}
}

// badDifficulty ErrBadDifficulty ihlalini kademeli işler (kullanıcı kararı 2026-11-13): beklenen
// hedefi taşımayan blok sürüm ya da parametre farkıyla dürüst eşten de gelebilir. İç anahtarın
// ilk ihlali yalnızca uyarıdır; ikincisi badDifficultyPenalty, sonrakiler ikiye katlanarak en
// çok discouragePeriod geri çevirir. Son geri çevirme bittikten sonra strikeReset boyunca ihlal
// olmazsa sayaç sıfırlanır (geri çevrilen eş bağlanamadığından süre cezanın bitiminden sayılır;
// aksi halde tavan cezası biter bitmez yeni bir bedava uyarı doğardı). Uyarı
// dahil her ihlal grup yükseltmesine sayılır: /24 içinde adres değiştiren saldırgan grubun
// bütün ilk ihlallerini bedava kullanamaz. Geri çevirme süresini döndürür (uyarıda 0).
func (p *protection) badDifficulty(id peerID) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[id.group] {
		return 0
	}
	now := p.now()
	s := p.strikes[id.inner]
	if s == nil || now.Sub(s.last) >= strikeReset {
		if s == nil && len(p.strikes) >= maxDiscouraged {
			p.pruneStrikesLocked(now)
		}
		s = &strike{}
		p.strikes[id.inner] = s
	}
	s.n++
	var d time.Duration
	if s.n >= 2 {
		d = badDifficultyPenalty
		for i := 2; i < s.n && d < discouragePeriod; i++ {
			d *= 2
		}
		d = min(d, discouragePeriod)
		p.setLocked(id.inner, now.Add(d), now)
	}
	s.last = now.Add(d)
	if p.esc.note(id, now) {
		p.setLocked(id.group, now.Add(discouragePeriod), now)
	}
	return d
}

// pruneStrikesLocked süresi dolan ihlal sayaçlarını, tablo yine doluysa onda birini rastgele
// siler.
func (p *protection) pruneStrikesLocked(now time.Time) {
	for k, s := range p.strikes {
		if now.Sub(s.last) >= strikeReset {
			delete(p.strikes, k)
		}
	}
	for k := range p.strikes {
		if len(p.strikes) <= maxDiscouraged-maxDiscouraged/10 {
			break
		}
		delete(p.strikes, k)
	}
}

// setLocked anahtarı until'e kadar geri çevirir; süren daha uzun geri çevirme kısalmaz. Tablo
// doluysa süresi bitenler ve yine doluysa kayıtların onda biri rastgele silinir (map sırası
// rastgele): tablo sınırı aşılmaz ve tam tarama en çok her maxDiscouraged/10 yeni kayıtta bir
// yapılır.
func (p *protection) setLocked(key string, until, now time.Time) {
	if old, ok := p.discouraged[key]; ok {
		if old.After(until) {
			return
		}
	} else if len(p.discouraged) >= maxDiscouraged {
		for k, u := range p.discouraged {
			if !now.Before(u) {
				delete(p.discouraged, k)
			}
		}
		for k := range p.discouraged {
			if len(p.discouraged) <= maxDiscouraged-maxDiscouraged/10 {
				break
			}
			delete(p.discouraged, k)
		}
	}
	p.discouraged[key] = until
}

// isDiscouraged eşin iç anahtarının ya da grubunun geri çevrilip çevrilmediğini söyler.
func (p *protection) isDiscouraged(id peerID) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[id.group] {
		return false
	}
	return p.activeLocked(id.inner) || p.activeLocked(id.group)
}

func (p *protection) activeLocked(key string) bool {
	until, ok := p.discouraged[key]
	if !ok {
		return false
	}
	if !p.now().Before(until) {
		delete(p.discouraged, key)
		return false
	}
	return true
}

// protectPeer yapılandırılmış eşin adres grubunu (ad ise çözülen her adresin grubunu) korur.
// Çözülemeyen ad atlanır.
func (ps *P2PServer) protectPeer(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if net.ParseIP(host) != nil {
		ps.prot.addConfigured(peerGroup(host))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerResolveTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		ps.log.Warn("eş adı çözülemedi; grubu korunmuyor", "peer", addr, "err", err)
		return
	}
	for _, ip := range ips {
		ps.prot.addConfigured(peerGroup(ip.IP.String()))
	}
}

// neverHonestBlock bloğun durumdan bağımsız olarak geçersiz olduğunu (dürüst eşin
// göndermeyeceğini) söyler.
func neverHonestBlock(err error) bool {
	for _, e := range []error{core.ErrInsufficientPoW, core.ErrBadHash, core.ErrBadMerkleRoot,
		core.ErrTooManyTx} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// malformedMessage mesajın JSON sözdizimi olarak bozuk olduğunu söyler; tür hatası (sürüm
// farkı), zaman aşımı, bağlantı kopması ve boyut sınırında kesilme (yarım mesaj) dürüst eşte
// de olabilir, sayılmaz.
func malformedMessage(err error) bool {
	var syn *json.SyntaxError
	return errors.As(err, &syn)
}
