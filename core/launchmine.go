package core

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// Mainnet genesis kazım aracının çekirdeği (docs/LANSMAN.md §4.1). Komut satırı, aygıt seçimi, ölçüm
// raporu ve dosya yazımı ana pakettedir (genesismine.go); burada lansman girdilerinin denetimi,
// yerel saat denetimi, T_g'yi tazeleyen nonce araması ve bulunan genesis'in lansman dosyası
// yükleyicisiyle (launch.go) baştan doğrulanması vardır. Araç yeni bir uzlaşma kuralı getirmez:
// yazdığı dosya her düğümün açılışta uyguladığı applyLaunch'tan geçmek zorundadır.

// ErrLaunchCheck lansman girdileri ya da ortamı bir lansman denetiminden geçemedi; genesis kazılmaz
// ya da bulunan genesis yazılmaz.
var ErrLaunchCheck = errors.New("lansman denetimi geçmedi")

// genesisRefresh arama sürerken T_g'nin kaç saniyede bir yerel saate tazelendiğidir. Tazelenen zaman
// damgası başlığı değiştirir ve arama yeni başlıkla baştan sürer; denemeler bağımsız olduğundan
// beklenen süre değişmez.
const genesisRefresh = 10

// launchMaxTipAge onaylayan Bitcoin bloğunun zamanının yerel saatten en çok kaç saniye geride
// olabileceğidir. Yalnızca aracın saat denetimidir, lansman dosyasının bir şartı değildir. Araç en
// yeni iki Bitcoin başlığıyla çalıştırılır (mesajdaki blok ve onu onaylayan uç blok); uç bloğun yaşı
// ortalama ~10 dakikadır. Fark bunu aşıyorsa başlıklar eskidir (ör. saatler önceki ön-uçuştan
// kalmıştır), yerel saat ileridedir ya da Bitcoin'de uzun süredir blok yoktur; üçünde de yeni blok
// beklenir. Bu sınır veri kuralından (launchMaxGenesisLag) bağımsızdır: o değişse de ileri giden
// saat burada yakalanır.
const launchMaxTipAge = 30 * 60

// genesisMaxAge bulunan genesis'in T_g'sinin bulunduğu andan en çok kaç saniye geride olabileceğidir:
// bir tazeleme aralığı ve en çok o kadar süren son tur. Olağan tur ~100 ms sürer; farkın bunu aşması
// makinenin uykuya geçtiğini, sürücünün takıldığını ya da saatin ileri atladığını gösterir ve deneme
// biter. Böylece "yayın anı − T_g" ne aramanın ne de tek bir turun uzunluğuyla sessizce şişer.
const genesisMaxAge = 2 * genesisRefresh

// LaunchInfo doğrulanmış lansman girdilerinin özetidir (günlük ve lansman kaydı için): ağ, başlangıç
// hedefi ve taban, genesis mesajı, mesajdaki Bitcoin bloğunun özeti ve zamanı (t_b), onu onaylayan
// bloğun özeti ve zamanı.
type LaunchInfo struct {
	Network                 string
	Bits                    uint32
	PowLimitBits            uint32
	Message                 string
	BitcoinHash             string
	BitcoinTime             int64
	BitcoinConfirmationHash string
	BitcoinConfirmationTime int64
}

// LaunchPlan veri denetimlerinden geçmiş lansman girdileridir: genesis'i henüz tanımlanmamış ağ ve
// genesis'in bağlanacağı iki Bitcoin başlığı. Kurulurken saate bakılmaz; saat CheckClock ile ve
// arama boyunca Mine ile denetlenir.
type LaunchPlan struct {
	// Progress nil değilse Mine her yeni başlıkta (ilk başlık ve her T_g tazelemesi) çağırır:
	// başlığın T_g'si ve o ana kadar denenen nonce sayısı.
	Progress func(timestamp int64, tried uint64)

	params  *Params // genesis'siz ağın kopyası
	rules   launchRules
	headers []string
	anchor  launchAnchor
}

// MinedGenesis kazılmış genesis'tir. File kanonik lansman dosyasıdır (core/mainnet.json'a yazılacak
// baytlar; yükleyiciden geçmiştir). Timestamp T_g'dir. Tried bütün başlıklarda denenen nonce sayısı,
// Headers denenen başlık sayısıdır (her T_g tazelemesi yeni bir başlıktır). Started ve Found aramanın
// başladığı ve nonce'un bulunduğu anlardır (saatin verdiği Unix saniyeleri).
type MinedGenesis struct {
	File      []byte
	Hash      string
	Timestamp int64
	Nonce     uint64
	Tried     uint64
	Headers   int
	Started   int64
	Found     int64
}

// NewMainnetLaunchPlan mainnet genesis'i kazılmadan önceki veri denetimlerini yapar. blockHeader
// genesis mesajına girecek, bir onay almış Bitcoin bloğunun, confirmationHeader onu onaylayan bloğun
// 80 baytlık başlığıdır (160 küçük harf hex). Reddedilenler: mainnet genesis'i zaten tanımlıysa;
// başlangıç hedefi ve taban henüz seçilmediyse ya da taban kuralını tutmuyorsa; başlıklardan biri
// kendi iş ispatını taşımıyorsa, hedefi gerçek Bitcoin zorluğu ölçeğinde değilse ya da ikincisi
// ilkine bağlanmıyorsa. Kurallar lansman dosyası yükleyicisinin kurallarıdır (mainnetLaunchRules).
func NewMainnetLaunchPlan(blockHeader, confirmationHeader string) (*LaunchPlan, error) {
	return newLaunchPlan(MainnetParams, mainnetLaunchRules, blockHeader, confirmationHeader)
}

func newLaunchPlan(p *Params, rules launchRules, blockHeader, confirmationHeader string) (*LaunchPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: ağ parametreleri yok", ErrLaunchCheck)
	}
	if p.GenesisHash != "" || p.GenesisMessage != "" || p.GenesisTimestamp != 0 || p.GenesisNonce != 0 {
		return nil, fmt.Errorf("%w: %s genesis'i zaten tanımlı (%q); genesis yeniden kazılmaz", ErrLaunchCheck, p.Name,
			p.GenesisHash)
	}
	if !rules.targetsChosen {
		return nil, fmt.Errorf("%w: %s başlangıç hedefi ve tabanı henüz seçilmedi (yer tutucu); önce ölçülen hashrate'ten seçilip yazılmalı",
			ErrLaunchCheck, p.Name)
	}
	if err := rules.checkTargets(p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLaunchCheck, err)
	}
	headers := []string{blockHeader, confirmationHeader}
	anchor, err := rules.parseAnchor(headers)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLaunchCheck, err)
	}
	q := *p
	return &LaunchPlan{params: &q, rules: rules, headers: headers, anchor: anchor}, nil
}

// Info doğrulanmış girdilerin özetidir.
func (pl *LaunchPlan) Info() LaunchInfo {
	a := pl.anchor
	return LaunchInfo{Network: pl.params.Name, Bits: pl.params.Bits, PowLimitBits: pl.params.PowLimitBits,
		Message: a.message(), BitcoinHash: a.block.hash, BitcoinTime: a.block.time,
		BitcoinConfirmationHash: a.confirmation.hash, BitcoinConfirmationTime: a.confirmation.time}
}

// CheckClock yerel saatin (now, Unix saniyesi) Bitcoin başlıklarının zamanlarıyla tutarlı olduğunu
// denetler. İki ayrı şey denetlenir. Birincisi: T_g yerel saattir; bu yüzden saat, T_g'nin veri
// kurallarını tutmalıdır (checkGenesisTime): mesajdaki bloğun zamanından (t_b) küçük olamaz ve ondan
// en çok launchMaxGenesisLag saniye sonra olabilir. Bunlar lansman dosyası yükleyicisinin de
// şartlarıdır. İkincisi: yerel saat onaylayan bloğun zamanına yakın olmalıdır; blok saatten en çok
// MaxFutureDrift saniye ileride (saat geride ya da başlığın damgası ileride) ve en çok
// launchMaxTipAge saniye geride olabilir (başlıklar eski ya da saat ileride). Bunlar yalnızca
// aracın denetimleridir.
//
// Amaç saat dilimi ve donanım saati kaynaklı kaymaları ve eski başlıkları yakalamaktır. Bitcoin
// damgaları madenci beyanıdır ve gerçek zamandan dakikalarca sapabilir; dakikalar ölçeğindeki saat
// sapmasını saat eşitlemesi denetimi ve ikinci makine yakalar (docs/LANSMAN.md §3.2).
func (pl *LaunchPlan) CheckClock(now int64) error {
	if err := pl.rules.checkGenesisTime(now, pl.anchor); err != nil {
		return fmt.Errorf("%w: T_g yerel saattir: %v", ErrLaunchCheck, err)
	}
	tc := pl.anchor.confirmation.time
	if ahead := tc - now; ahead > MaxFutureDrift {
		return fmt.Errorf("%w: onaylayan Bitcoin bloğunun zamanı (%d) yerel saatten (%d) %d sn ileride, en çok %d sn olabilir (yerel saat geride ya da başlığın damgası ileride)",
			ErrLaunchCheck, tc, now, ahead, MaxFutureDrift)
	}
	if age := now - tc; age > launchMaxTipAge {
		return fmt.Errorf("%w: onaylayan Bitcoin bloğunun zamanı (%d) yerel saatten (%d) %d sn geride, en çok %d sn olabilir (başlıklar eski, yerel saat ileride ya da Bitcoin'de uzun süredir blok yok: yeni Bitcoin bloğu beklenmeli)",
			ErrLaunchCheck, tc, now, age, launchMaxTipAge)
	}
	return nil
}

// Mine genesis'i arar ve lansman dosyasını kurar. clock yerel saattir (Unix saniyesi); stop nil
// olabilir. T_g ilk başlıkta ve sonra her genesisRefresh saniyede bir saatten okunur; her yeni
// başlıkta CheckClock yeniden çalışır ve arama nonce 0'dan başlar. Saat geri giderse, bir denetim
// düşerse ya da nonce bulunduğunda başlık genesisMaxAge'den eskiyse deneme biter. Bulunan nonce
// yalnızca kazıldığı başlıkla birlikte kullanılır, CPU'da yeniden özetlenir ve kurulan dosya
// yükleyiciden (Verify) geçirilir. Kazımdan (Mine, mining.go) farklı olarak hatalı arka uçta
// CPU'ya dönülmez: mainnet genesis'i yalnızca açıklanan GPU kazıcısıyla kazılır.
func (pl *LaunchPlan) Mine(be MineBackend, clock func() int64, stop <-chan struct{}) (*MinedGenesis, error) {
	if be == nil || clock == nil {
		return nil, fmt.Errorf("%w: kazım arka ucu ve saat verilmeli", ErrLaunchCheck)
	}
	count := be.RoundSize()
	if count == 0 {
		return nil, errZeroRound
	}
	target, err := TargetFromBits(pl.params.Bits)
	if err != nil {
		return nil, err
	}
	tgt := targetBytes(target)
	res := &MinedGenesis{}
	var hdr [HeaderSize]byte
	var tg int64
	var nonce uint64
	for {
		if stop != nil {
			select {
			case <-stop:
				return nil, ErrMiningStopped
			default:
			}
		}
		now := clock()
		if res.Headers > 0 && now < tg {
			return nil, clockWentBack(tg, now)
		}
		if res.Headers == 0 || now >= tg+genesisRefresh {
			if err := pl.CheckClock(now); err != nil {
				return nil, err
			}
			if hdr, err = pl.header(now); err != nil {
				return nil, err
			}
			tg, nonce = now, 0
			if res.Headers == 0 {
				res.Started = now
			}
			res.Headers++
			if pl.Progress != nil {
				pl.Progress(tg, res.Tried)
			}
		}
		n, found, err := be.Scan(hdr, tgt, nonce, count)
		if err != nil {
			return nil, fmt.Errorf("kazım arka ucu (%s) hata verdi: %w", be.Name(), err)
		}
		if !found {
			res.Tried += count
			var more bool
			if nonce, more = nextRound(nonce, count); !more {
				return nil, fmt.Errorf("%w: nonce alanı tükendi", ErrLaunchCheck)
			}
			continue
		}
		// Arka ucun sonucuna güvenilmez: aynı başlık CPU'da yeniden özetlenir.
		digest, ok := checkNonce(hdr, n, &tgt)
		if !ok {
			return nil, fmt.Errorf("%w: kazım arka ucunun (%s) bulduğu nonce %d CPU doğrulamasından geçmedi",
				ErrLaunchCheck, be.Name(), n)
		}
		if d := n - nonce; d < count {
			res.Tried += d + 1
		} else {
			res.Tried += count
		}
		// Buradan sonra dönen her hata bulunan nonce'u ve özetini taşır: kullanılmayan bir bulgu da
		// günlükte iz bırakır (atılan denemeler lansman kaydında anılır).
		hash := hex.EncodeToString(digest[:])
		end := clock()
		if end < tg {
			return nil, fmt.Errorf("%w; bulunan nonce %d (özet %s) kullanılmadı", clockWentBack(tg, end), n, hash)
		}
		if end-tg > genesisMaxAge {
			return nil, fmt.Errorf("%w: nonce bulundu ama başlığı bayat (T_g %d, şimdi %d; en çok %d sn geride olabilir): tur çok uzun sürdü ya da saat ileri atladı; bulunan nonce %d (özet %s) kullanılmadı, deneme durduruldu",
				ErrLaunchCheck, tg, end, genesisMaxAge, n, hash)
		}
		res.Timestamp, res.Nonce, res.Hash, res.Found = tg, n, hash, end
		if res.File, err = pl.encode(res); err != nil {
			// Nonce geçerlidir ama kurulan dosya yükleyiciden geçmedi: bu bir lansman denetimi değil,
			// iç tutarsızlıktır. Hata bilinçli olarak sarılmaz; çağıran onu "denetim geçmedi" diye
			// sınıflayamaz.
			return nil, fmt.Errorf("genesis bulundu (özet %s, T_g %d, nonce %d) ama %v", hash, tg, n, err)
		}
		return res, nil
	}
}

func clockWentBack(tg, now int64) error {
	return fmt.Errorf("%w: yerel saat geri gitti (T_g %d, şimdi %d): saat eşitlemesi kararsız, deneme durduruldu",
		ErrLaunchCheck, tg, now)
}

// header zaman damgası verilen genesis adayının başlığıdır (nonce alanı 0).
func (pl *LaunchPlan) header(timestamp int64) ([HeaderSize]byte, error) {
	q := *pl.params
	q.GenesisTimestamp, q.GenesisMessage, q.GenesisNonce = timestamp, pl.anchor.message(), 0
	hdr, ok := genesisCandidate(&q).headerBytes()
	if !ok {
		return hdr, ErrBadHash
	}
	return hdr, nil
}

// encode bulunan genesis'in lansman dosyasını kurar ve yükleyiciyle baştan doğrular.
func (pl *LaunchPlan) encode(g *MinedGenesis) ([]byte, error) {
	p := pl.params
	lf := &launchFile{Network: p.Name, Timestamp: g.Timestamp, Message: pl.anchor.message(),
		Bits: fmt.Sprintf("%08x", p.Bits), PowLimitBits: fmt.Sprintf("%08x", p.PowLimitBits), Nonce: g.Nonce,
		Hash: g.Hash, BitcoinHeaders: pl.headers}
	raw, err := lf.encode()
	if err != nil {
		return nil, err
	}
	// Verify genesis'i dosyadaki alanlardan yeniden kurar ve özetini dosyadaki özetle (g.Hash)
	// karşılaştırır; başarılıysa dosya bulunan genesis'i taşır.
	if _, err := pl.Verify(raw); err != nil {
		return nil, fmt.Errorf("kurulan lansman dosyası kendi denetiminden geçmedi: %w", err)
	}
	return raw, nil
}

// Verify raw'ın bu planın ağı ve kurallarıyla geçerli, bu planın Bitcoin başlıklarına bağlanan bir
// lansman dosyası olduğunu baştan denetler: katı çözümleme, lansman şartları (applyLaunch), genesis'in
// CPU'da yeniden kurulması ve özetinin hedefi karşılaması. Genesis özetini döndürür. Araç diske
// yazdığı dosyayı geri okuyup bununla doğrular.
func (pl *LaunchPlan) Verify(raw []byte) (string, error) {
	lf, err := parseLaunchFile(raw)
	if err != nil {
		return "", err
	}
	if lf == nil {
		return "", fmt.Errorf("%w: dosya boş (lansman öncesi)", ErrBadLaunchFile)
	}
	q := *pl.params
	if err := applyLaunch(&q, lf, pl.rules); err != nil {
		return "", err
	}
	if lf.BitcoinHeaders[0] != pl.headers[0] || lf.BitcoinHeaders[1] != pl.headers[1] {
		return "", fmt.Errorf("%w: dosyadaki Bitcoin başlıkları bu denemenin başlıkları değil", ErrBadLaunchFile)
	}
	return q.GenesisHash, nil
}

// EmptyLaunchFile raw'ın boş lansman dosyası ("{}" ve satır sonu) olup olmadığını söyler. Kazım aracı
// yalnızca boş lansman dosyasının üzerine yazar.
func EmptyLaunchFile(raw []byte) bool {
	lf, err := parseLaunchFile(raw)
	return err == nil && lf == nil
}
