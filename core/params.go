package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	"quantum-bitcoin/crypto"
)

// Params bir ağın uzlaşma kimliğidir: ad, başlangıç hedefi ve genesis bloğu.
// Genesis değerleri sabittir ve GenesisHash testlerde bağımsız (Python) vektörlerle sabitlenir.
type Params struct {
	Name             string
	Bits             uint32 // başlangıç hedefi: 1. ve 2. blok bunu taşır, ASERT çapasıdır
	PowLimitBits     uint32 // ağın en kolay hedefi (taban): ASERT buna kırpar; Bits bundan kolay olamaz
	GenesisTimestamp int64
	GenesisMessage   string // genesis'in bu andan önce bilinemeyeceğini gösteren taahhüt
	GenesisNonce     uint64
	GenesisHash      string // boşsa ağ henüz başlatılamaz (lansman öncesi mainnet)

	// Emisyon (bkz. emission.go). Değerler TargetSpacing'den withEmission ile türetilir.
	TargetSpacing   int64 // hedef blok aralığı, saniye; lansmandan önce dondurulur
	InitialSubsidy  int64 // ilk dönem blok ödülü, taban birim
	TailSubsidy     int64 // kuyruk emisyonu: yarılanmış ödülün altına inemeyeceği taban
	HalvingInterval int64 // yarılanma aralığı, blok
	SlowStartBlocks int64 // ödülün 0'dan InitialSubsidy'ye doğrusal yükseldiği ilk blok sayısı

	// CoinbaseMaturity: H bloğunun ödülü + ücretleri H+CoinbaseMaturity bloğu bağlanırken
	// alacaklandırılır (ertelenmiş ödeme). Değer TargetSpacing'den türetilir (~4 saat).
	CoinbaseMaturity int64

	// Zorluk (bkz. asert.go): HalfLife ASERT yarı ömrüdür (saniye). NoRetarget yalnızca
	// regtest içindir: hedef Bits'te sabit kalır, testler hızlı ve öngörülebilir olsun.
	HalfLife   int64
	NoRetarget bool

	// MinChainWork sürüme gömülü en az zincir işidir (düğüm politikası, bkz. minwork.go);
	// nil ise 0. Başlığa ve genesis'e girmez.
	MinChainWork *big.Int
}

// 60 sn blok süresi için emisyon (kullanıcı kararı, 2026-11-08): 5 QBTC ilk ödül,
// 2.100.000 blokta (~4 yıl) bir yarılanma, 0,1 QBTC kuyruk, 20.000 blokluk başlangıç
// rampası. Başka bir blok süresinde ödüller süreyle orantılı, aralıklar ters orantılı
// ölçeklenir; böylece zamana göre emisyon eğrisi aynı kalır. Blok süresi zincir ortasında
// değiştirilmez: lansmandan önce ölçülüp dondurulur.
const (
	referenceSpacing         int64 = 60
	referenceInitialSubsidy  int64 = 5 * Coin
	referenceTailSubsidy     int64 = Coin / 10
	referenceHalvingInterval int64 = 2_100_000
	referenceSlowStartBlocks int64 = 20_000

	// Coinbase olgunluğu (kullanıcı kararı, 2026-11-08): 60 sn'de 240 blok (~4 saat).
	referenceCoinbaseMaturity int64 = 240
)

// withEmission emisyon değerlerini p.TargetSpacing'den türetir. Ölçekleme tam bölünmüyorsa
// paket başlatılırken panikler: bu bir kod sabiti hatasıdır ve sessizce yuvarlanmamalıdır.
func withEmission(p *Params) *Params {
	s := p.TargetSpacing
	if s <= 0 || referenceInitialSubsidy*s%referenceSpacing != 0 ||
		referenceTailSubsidy*s%referenceSpacing != 0 ||
		referenceHalvingInterval*referenceSpacing%s != 0 ||
		referenceSlowStartBlocks*referenceSpacing%s != 0 ||
		referenceCoinbaseMaturity*referenceSpacing%s != 0 {
		panic(fmt.Sprintf("%s: blok süresi %d sn emisyonu tam ölçeklemiyor", p.Name, s))
	}
	p.InitialSubsidy = referenceInitialSubsidy * s / referenceSpacing
	p.TailSubsidy = referenceTailSubsidy * s / referenceSpacing
	p.HalvingInterval = referenceHalvingInterval * referenceSpacing / s
	p.SlowStartBlocks = referenceSlowStartBlocks * referenceSpacing / s
	p.CoinbaseMaturity = referenceCoinbaseMaturity * referenceSpacing / s
	p.HalfLife = referenceHalfLife
	return p
}

// ErrBadParams ağ parametreleri tutarsız.
var ErrBadParams = errors.New("geçersiz ağ parametreleri")

// Validate emisyon parametrelerinin ödül fonksiyonunu tanımsız bırakmadığını denetler
// (sıfıra bölme, negatif ödül, kuyruğun ilk ödülden büyük olması vb.).
func (p *Params) Validate() error {
	switch {
	case p == nil:
		return ErrBadParams
	case p.TargetSpacing <= 0:
		return fmt.Errorf("%w: %s blok aralığı %d", ErrBadParams, p.Name, p.TargetSpacing)
	case p.InitialSubsidy <= 0 || p.InitialSubsidy > maxSubsidy:
		return fmt.Errorf("%w: %s ilk ödül %d", ErrBadParams, p.Name, p.InitialSubsidy)
	case p.TailSubsidy <= 0 || p.TailSubsidy > p.InitialSubsidy:
		return fmt.Errorf("%w: %s kuyruk ödülü %d", ErrBadParams, p.Name, p.TailSubsidy)
	case p.HalvingInterval <= 0:
		return fmt.Errorf("%w: %s yarılanma aralığı %d", ErrBadParams, p.Name, p.HalvingInterval)
	case p.SlowStartBlocks < 0 || p.SlowStartBlocks > maxSlowStart || p.SlowStartBlocks >= p.HalvingInterval:
		return fmt.Errorf("%w: %s başlangıç rampası %d", ErrBadParams, p.Name, p.SlowStartBlocks)
	case p.CoinbaseMaturity < 1:
		return fmt.Errorf("%w: %s coinbase olgunluğu %d", ErrBadParams, p.Name, p.CoinbaseMaturity)
	case p.HalfLife < minHalfLifePerFTL*MaxFutureDrift:
		// Tek bloğun zaman damgasını FTL kadar ileri atmanın kazancı 2^(FTL/yarı ömür) − 1.
		return fmt.Errorf("%w: %s yarı ömür %d sn, FTL için en az %d", ErrBadParams, p.Name,
			p.HalfLife, minHalfLifePerFTL*MaxFutureDrift)
	case p.NoRetarget && p.Name != "regtest":
		return fmt.Errorf("%w: %s yeniden hedeflemesiz olamaz", ErrBadParams, p.Name)
	case p.MinChainWork != nil && p.MinChainWork.Sign() < 0:
		return fmt.Errorf("%w: %s en az zincir işi negatif", ErrBadParams, p.Name)
	}
	start, err := TargetFromBits(p.Bits)
	if err != nil {
		return fmt.Errorf("%w: %s başlangıç hedefi %08x", ErrBadParams, p.Name, p.Bits)
	}
	limit, err := TargetFromBits(p.PowLimitBits)
	if err != nil {
		return fmt.Errorf("%w: %s zorluk tabanı %08x", ErrBadParams, p.Name, p.PowLimitBits)
	}
	if start.Cmp(limit) > 0 {
		return fmt.Errorf("%w: %s başlangıç hedefi %08x tabandan (%08x) kolay", ErrBadParams, p.Name,
			p.Bits, p.PowLimitBits)
	}
	if p.Name == "mainnet" {
		if maxT, _ := TargetFromBits(mainnetMaxPowLimitBits); limit.Cmp(maxT) > 0 {
			return fmt.Errorf("%w: mainnet tabanı %08x, en kolay %08x", ErrBadParams, p.PowLimitBits,
				mainnetMaxPowLimitBits)
		}
	}
	return nil
}

// powLimitTarget ağın zorluk tabanıdır (Validate geçerliliğini denetler).
func (p *Params) powLimitTarget() *big.Int {
	t, err := TargetFromBits(p.PowLimitBits)
	if err != nil {
		return new(big.Int).Set(powLimit) // Validate'ten geçmiş parametrelerde gelinmez
	}
	return t
}

// mainnetMaxPowLimitBits mainnet tabanının olabileceği en kolay hedeftir: 2^224 (Bitcoin
// zorluk 1, blok başına ~2^32 özet). Kullanıcı kararı (2026-11-08): mainnet tabanı lansmanda,
// başlangıç hedefiyle birlikte ona oranla seçilir (başlangıçtan 2^12–2^16 kat kolay, ama
// 2^224'ten kolay değil ve tabanın gerektirdiği hashrate tek ucuz bir ASIC'in, ~10 TH/s,
// altında kalacak düzeyde). Taban düşük zorluklu başlık spam'ine ya da uzun menzilli yeniden
// yazıma karşı savunma DEĞİLDİR (2^224 bir ASIC için bedavadır); onlara karşı en az zincir
// işi / başlık ön-senkronizasyonu gerekir. Taban yalnızca duraklama sonrası hedefin ne kadar
// kolaylaşabileceğini sınırlar. Mutlak ASERT kırpılınca açığı silmez, biriktirir.
const mainnetMaxPowLimitBits = 0x1d00ffff

// sigTag imza alanının sürüm etiketidir.
const sigTag = "QBTC-SIG-v4"

var (
	// MainnetParams: genesis kasıtlı olarak tanımsızdır. Genesis bugünden sabitlenirse onu bilen
	// herkes lansmana kadar gizlice zincir kazıp "premine yok" vaadini delebilir. Mainnet genesis'i
	// lansman anında öngörülemez bir mesajla (ör. o anki bir Bitcoin blok özeti) üretilecektir.
	// Başlangıç hedefi ve taban da genesis'le birlikte lansmanda seçilir (BitsForHashrate);
	// buradaki değerler yer tutucudur.
	MainnetParams = withEmission(&Params{Name: "mainnet", Bits: 0x1d00ffff, PowLimitBits: 0x1d00ffff,
		TargetSpacing: 60})

	// TestnetParams: herkese açık test ağı (hedef eski zorluk 4'e eşdeğer). Taban başlangıçtan
	// 2^8 kat kolaydır: kırpma ve başlangıcın altına kolaylaşma yolu testnette sınanır.
	TestnetParams = withEmission(&Params{
		Name:             "testnet",
		Bits:             0x1f00ffff,
		PowLimitBits:     0x2000ffff,
		GenesisTimestamp: 1794096000, // 2026-11-08T00:00:00Z
		GenesisMessage:   "Q-BTC testnet genesis 2026-11-08",
		GenesisNonce:     19141,
		GenesisHash:      "0000f47b1643fb44236004eadf3a39b5e1999ccfa4f6cb941f14975ee07b1be0",
		TargetSpacing:    60,
	})

	// RegtestParams: yerel geliştirme ve testler için en kolay hedef (powLimit'e yakın).
	// Yalnızca testler içindir: ilk ödül 50 QBTC (mevcut tutar testleri bu ölçekte yazılı),
	// yarılanma 150 blokta bir ve rampa yok; testler dönem ve kuyruk geçişlerini (1350.
	// blok) kısa zincirde görebilsin. Coinbase olgunluğu 3 blok: testler sınırı ve olgunluğu
	// aşan reorg'ları kısa zincirde sınayabilsin. Hedef sabittir (NoRetarget); ASERT testleri
	// yeniden hedeflemeli bir kopya kullanır. Mainnet/testnet bundan etkilenmez.
	RegtestParams = regtestEmission(withEmission(&Params{
		Name:             "regtest",
		Bits:             0x200fffff,
		PowLimitBits:     0x200fffff,
		GenesisTimestamp: 1794096000,
		GenesisMessage:   "Q-BTC regtest genesis",
		GenesisNonce:     10,
		GenesisHash:      "0c361f2df64b66a48865cda689233554a7faa517a584dd034ce882a987d20eef",
		TargetSpacing:    60,
	}))
)

func regtestEmission(p *Params) *Params {
	p.InitialSubsidy = 50 * Coin
	p.HalvingInterval = 150
	p.SlowStartBlocks = 0
	p.CoinbaseMaturity = 3
	p.NoRetarget = true
	return p
}

// ParamsByName komut satırındaki ağ adını parametrelere çevirir.
func ParamsByName(name string) (*Params, error) {
	for _, p := range []*Params{MainnetParams, TestnetParams, RegtestParams} {
		if p.Name == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("bilinmeyen ağ: %q (mainnet, testnet, regtest)", name)
}

// SigDomain imzaları bu ağa ve genesis'ine bağlayan alan etiketidir.
func (p *Params) SigDomain() (string, error) {
	if p == nil || p.GenesisHash == "" {
		return "", ErrNoGenesis
	}
	return sigTag + "|" + p.Name + "|" + p.GenesisHash, nil
}

// GenesisReceiver genesis coinbase'inin alıcısıdır: mesajın özetinden türetilmiş, biçimce
// geçerli bir adres. Genesis tutarı sıfır olduğundan bu adresin harcanabilirliği önemsizdir.
func (p *Params) GenesisReceiver() string {
	h := sha256.Sum256([]byte(p.GenesisMessage))
	return crypto.AddressPrefix + hex.EncodeToString(h[:20])
}
