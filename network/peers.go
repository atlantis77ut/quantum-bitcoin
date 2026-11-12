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
// Geri çevirme: hiçbir dürüst eşin göndermeyeceği, durumdan bağımsız geçersiz girdi (PoW'u
// tutmayan, özeti ya da Merkle kökü yanlış, çok işlemli ya da ebeveyni bilinip beklenen hedefi
// taşımayan blok, JSON sözdizimi hatası) gönderen grubun bağlantıları discouragePeriod boyunca
// kabul anında, yuva kullanılmadan kapatılır. Ucumuza göre denetlenen yetim kapısı
// (ErrOrphanTooEasy; düğüm geride kalınca dürüst bloğa da çarpar), JSON tür hataları (sürüm
// farkı), durum hataları, zaman aşımı ve gelecek tarihli blok bu listeye girmez. Yapılandırılmış
// eşlerin grupları hiç geri çevrilmez. Aynı gruptaki dürüst eşler (bulut kiracısı, Tor)
// etkilenir; süre bu yüzden kısadır.
const (
	reservedConns        = 16 // maxConcurrentConns (64) içinden; 8'i yalnızca yapılandırılmış eşlere
	reservedDecodes      = 3  // maxConcurrentDecodes (8) içinden; 2'si yalnızca yapılandırılmış eşlere
	reservedServes       = 6  // maxConcurrentServes (16) içinden; 3'ü yalnızca yapılandırılmış eşlere
	maxRecentBlockGroups = 8
	discouragePeriod     = time.Hour
	maxDiscouraged       = 10_000
	peerResolveTimeout   = 2 * time.Second
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

// protection korunan grupları ve geri çevrilen grupları tutar.
type protection struct {
	mu          sync.Mutex
	now         func() time.Time
	configured  map[string]bool
	recent      []string // en yeni sonda, en çok maxRecentBlockGroups
	discouraged map[string]time.Time
}

func newProtection() *protection {
	return &protection{now: time.Now, configured: map[string]bool{}, discouraged: map[string]time.Time{}}
}

func (p *protection) levelOf(group string) level {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[group] {
		return levelConfigured
	}
	for _, g := range p.recent {
		if g == group {
			return levelEarned
		}
	}
	return levelNone
}

func (p *protection) isProtected(group string) bool { return p.levelOf(group) != levelNone }

func (p *protection) addConfigured(group string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.configured[group] = true
}

// noteBlock grubu ucumuzu uzatan yeni bir blok getiren en son grup yapar.
func (p *protection) noteBlock(group string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, g := range p.recent {
		if g == group {
			p.recent = append(p.recent[:i], p.recent[i+1:]...)
			break
		}
	}
	p.recent = append(p.recent, group)
	if len(p.recent) > maxRecentBlockGroups {
		p.recent = append([]string(nil), p.recent[len(p.recent)-maxRecentBlockGroups:]...)
	}
}

// discourage grubu discouragePeriod boyunca geri çevirir; yapılandırılmış eşin grubu hiç
// geri çevrilmez. Tablo doluysa süresi bitenler ve yine doluysa kayıtların onda biri rastgele
// silinir (map sırası rastgele): tablo sınırı aşılmaz ve tam tarama en çok her
// maxDiscouraged/10 yeni kayıtta bir yapılır.
func (p *protection) discourage(group string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.configured[group] {
		return
	}
	now := p.now()
	if _, ok := p.discouraged[group]; !ok && len(p.discouraged) >= maxDiscouraged {
		for k, until := range p.discouraged {
			if !now.Before(until) {
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
	p.discouraged[group] = now.Add(discouragePeriod)
}

func (p *protection) isDiscouraged(group string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.discouraged[group]
	if !ok || p.configured[group] {
		return false
	}
	if !p.now().Before(until) {
		delete(p.discouraged, group)
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
		core.ErrTooManyTx, core.ErrBadDifficulty} {
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
