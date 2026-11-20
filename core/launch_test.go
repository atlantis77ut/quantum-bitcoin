package core

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Gerçek Bitcoin başlıkları (scripts/genesis_vektor_test.py'dekilerle aynı): 976600 ve 976601.
// bloklar ile zorluk tabanının dışında kalan örnek olarak 0 ve 1. bloklar. Özetleri ve iş ispatları
// testte yeniden hesaplanır; yanlış aktarılmış bir başlık geçemez.
const btc976600 = "00800e2013576923c649c2b677f84b358bdfb2761a914377515c01000000000000000000b39d9a5eab3b9edae48949c0e6b48d3a00f04e89972146d055219f711c47cde78918ff6a73160217df4de7ca"
const btc976601 = "0040002086eb092bfe0cf222993065c42ade49946ad9da30eafb0100000000000000000080590567edbccfab0ecc4939f86f5ff690425e8349d61ddf418abcb5a138d03de019ff6a7316021786be3bf6"
const btc976600Hash = "00000000000000000001fbea30dad96a9449de2ac465309922f20cfe2b09eb86"
const btc976600Prev = "000000000000000000015c517743911a76b2df8b354bf877b6c249c623695713"
const btc976601Hash = "00000000000000000001f07afd7e1e27a7d64535f3d62d4a98bf6156a7ad453d"
const btc976600Time = 1795102857
const btc0 = "0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c"
const btc1 = "010000006fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000982051fd1e4ba744bbbe680e1fee14677ba1a3c3540bf7b1cdb606e857233e0e61bc6649ffff001d01e36299"
const btc0Hash = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

// easyLaunchRules gerçek kurallardır; yalnızca tavan regtest hedefine çekilmiş ve hedefler seçilmiş
// sayılmıştır (gerçek tavanda nonce aramak testte yapılamaz). Taban kuralı ve Bitcoin zorluk tabanı
// gerçek değerlerindedir.
var easyLaunchRules = launchRules{targetsChosen: true, maxPowLimitBits: 0x200fffff, floorShift: mainnetFloorShift,
	bitcoinMaxBits: launchBitcoinMaxBits}

// easyMainnet genesis'siz, kolay hedefli bir "mainnet" kopyasıdır. Validate bu kopyayı reddeder
// (tabanı gerçek tavandan kolaydır): böyle bir ağla düğüm başlatılamaz.
func easyMainnet() *Params {
	return withEmission(&Params{Name: "mainnet", Bits: 0x200fffff, PowLimitBits: 0x200fffff, TargetSpacing: 60})
}

// minedLaunch p'nin hedefini karşılayan ilk nonce ile bir lansman dosyası kurar (kolay hedefte
// birkaç deneme).
func minedLaunch(t *testing.T, p *Params, timestamp int64, message string, headers ...string) *launchFile {
	t.Helper()
	q := *p
	q.GenesisTimestamp, q.GenesisMessage, q.GenesisHash = timestamp, message, "aranıyor"
	for q.GenesisNonce = 0; q.GenesisNonce < 100000; q.GenesisNonce++ {
		g, err := NewGenesisBlock(&q)
		if err != nil {
			t.Fatal(err)
		}
		if MeetsTarget(g.Hash, p.Bits) {
			return &launchFile{Network: p.Name, Timestamp: timestamp, Message: message, Bits: fmt.Sprintf("%08x", p.Bits),
				PowLimitBits: fmt.Sprintf("%08x", p.PowLimitBits), Nonce: q.GenesisNonce, Hash: g.Hash, BitcoinHeaders: headers}
		}
	}
	t.Fatal("nonce bulunamadı")
	return nil
}

// easyLaunch testlerin ortak geçerli örneğidir: 976600. bloğa bağlanan, kolay hedefli lansman dosyası.
func easyLaunch(t *testing.T) (*Params, *launchFile) {
	t.Helper()
	p := easyMainnet()
	return p, minedLaunch(t, p, btc976600Time+900, mainnetGenesisMessagePrefix+btc976600Hash, btc976600, btc976601)
}

func readTestdata(t *testing.T, elem ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"testdata"}, elem...)...))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
}

func panics(f func()) (did bool) {
	defer func() { did = recover() != nil }()
	f()
	return false
}

func hasGenesis(p *Params) bool {
	return p.GenesisHash != "" || p.GenesisMessage != "" || p.GenesisTimestamp != 0 || p.GenesisNonce != 0
}

// Gömülü lansman dosyası ile MainnetParams tutarlıdır. Dosya boşken mainnet genesis'sizdir; doluyken
// lansman şartları gerçek kurallarla tutar ve MainnetParams tam olarak dosyadaki genesis'tir. Taban
// kuralı genesis'ten bağımsızdır: hedefler lansmandan günler önce yazıldığında da tutmalıdır.
func TestMainnetLaunchFile(t *testing.T) {
	lf, err := parseLaunchFile(mainnetLaunchJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := MainnetParams.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := mainnetLaunchRules.checkTargets(MainnetParams); err != nil {
		t.Fatalf("mainnet hedefleri taban kuralını tutmuyor: %v", err)
	}
	// Hedefler yer tutucudan farklıysa "seçildi" işareti de true olmalıdır: hedef değişikliği işareti
	// unutamaz. Tersi serbesttir (ölçülen hashrate hedefi tavanda bırakabilir).
	placeholder := MainnetParams.Bits == mainnetMaxPowLimitBits && MainnetParams.PowLimitBits == mainnetMaxPowLimitBits
	if !placeholder && !mainnetLaunchRules.targetsChosen {
		t.Fatal("mainnet hedefleri yer tutucudan farklı ama mainnetTargetsChosen false")
	}
	if lf == nil {
		if hasGenesis(MainnetParams) {
			t.Fatalf("lansman dosyası boş ama mainnet genesis'i dolu: %+v", MainnetParams)
		}
		return
	}
	fresh := withEmission(&Params{Name: "mainnet", Bits: MainnetParams.Bits, PowLimitBits: MainnetParams.PowLimitBits,
		TargetSpacing: MainnetParams.TargetSpacing})
	if err := applyLaunch(fresh, lf, mainnetLaunchRules); err != nil {
		t.Fatal(err)
	}
	if MainnetParams.GenesisHash != lf.Hash || MainnetParams.GenesisTimestamp != lf.Timestamp ||
		MainnetParams.GenesisMessage != lf.Message || MainnetParams.GenesisNonce != lf.Nonce {
		t.Fatalf("MainnetParams lansman dosyasındaki genesis değil: %+v", MainnetParams)
	}
	// Yükleyici saate bakmaz; gelecekteki bir T_g burada yakalanır. 1. bloğun damgası T_g'den büyük
	// olmalı ve düğümlerin saatini en çok MaxFutureDrift geçebilir: T_g bundan ilerideyse zincir o
	// süre boyunca ilerleyemez. Lansmandan sonra bu koşul hep tutar.
	if ahead := lf.Timestamp - time.Now().Unix(); ahead > MaxFutureDrift {
		t.Fatalf("T_g bu makinenin saatinden %d sn ileride (en çok %d)", ahead, MaxFutureDrift)
	}
}

// Taban kuralı gerçek mainnet sınırlarıyla: bağımsız betiğin sınamasındaki tabloyla
// (scripts/genesis_vektor_test.py, test_real_targets) aynı çiftler.
func TestLaunchFloorRule(t *testing.T) {
	check := func(bits, limit uint32) error {
		p := *MainnetParams
		p.Bits, p.PowLimitBits = bits, limit
		return mainnetLaunchRules.checkTargets(&p)
	}
	accepted := [][2]uint32{
		{0x1d00ffff, 0x1d00ffff}, // başlangıç tavanda: taban da tavandır
		{0x1c00ffff, 0x1d00ffff}, // 2^16 katı tavanı aşar, taban tavana kırpılır
		{0x1b00ffff, 0x1d00ffff}, // 2^16 katı tam tavan
		{0x1a00ffff, 0x1c00ffff}, // tavanın altında: taban tam 2^16 kat
		{0x1b0404cb, 0x1d00ffff},
		{0x1a0404cb, 0x1c0404cb},
		{0x1a7fffff, 0x1c7fffff},
	}
	for _, c := range accepted {
		if err := check(c[0], c[1]); err != nil {
			t.Errorf("%08x/%08x reddedildi: %v", c[0], c[1], err)
		}
	}
	rejected := [][2]uint32{
		{0x200fffff, 0x200fffff}, // taban tavandan kolay
		{0x200fffff, 0x1d00ffff}, // başlangıç tabandan kolay
		{0x1d010000, 0x1d010000},
		{0x1d010000, 0x1d00ffff},
		{0x1a00ffff, 0x1d00ffff}, // taban kuralın verdiğinden kolay
		{0x1a00ffff, 0x1b00ffff}, // taban kuralın verdiğinden zor
		{0x1a00ffff, 0x1a00ffff},
		{0x1c00ffff, 0x1c00ffff},
		{0x1a0404cb, 0x1c00ffff},
		{0x1c000fff, 0x1d00ffff}, // kanonik olmayan başlangıç hedefi
		{0x1c00ffff, 0},
	}
	for _, c := range rejected {
		if err := check(c[0], c[1]); err == nil {
			t.Errorf("%08x/%08x kabul edildi", c[0], c[1])
		}
	}
	// LaunchTargets'in ölçülen hashrate'ten üreteceği her çift kuralı tutar.
	for _, h := range []float64{1, 5e7, 1e9, 3e9, 1e12, 1e15, 1e18} {
		sb, fb, err := LaunchTargets(h, MainnetParams.TargetSpacing)
		if err != nil {
			t.Fatalf("%v: %v", h, err)
		}
		if err := check(sb, fb); err != nil {
			t.Errorf("%v H/s için önerilen %08x/%08x kuralı tutmuyor: %v", h, sb, fb, err)
		}
	}
}

// Dolu dal bugün gömülü dosyayla çalışmaz; aynı yol kolay hedefli bir örnekle uçtan uca sınanır.
// Depodaki örnek dosyalar (testdata/launch) iki uygulamanın ortak sınama verisidir: lansman
// dosyasını bu test baştan kurar, vektörünü bağımsız betik üretmiştir (betiğin sınaması da aynı
// dosyaları kendi tarafından yeniden kurar). Lansman günü akışı da budur: kazım aracı dosyayı
// yazar, betik vektörü üretir, Go testi ikisini karşılaştırır.
func TestLaunchEasyTarget(t *testing.T) {
	p, lf := easyLaunch(t)
	raw, err := lf.encode()
	if err != nil {
		t.Fatal(err)
	}
	if fixture := readTestdata(t, "launch", "easy_mainnet.json"); !bytes.Equal(raw, fixture) {
		t.Fatalf("örnek lansman dosyası Go'nun kurduğuyla aynı değil:\n%s\nGo:\n%s", fixture, raw)
	}
	parsed, err := parseLaunchFile(raw)
	if err != nil || !reflect.DeepEqual(parsed, lf) {
		t.Fatalf("kanonik dosya çözülemedi: %+v %v", parsed, err)
	}
	if err := applyLaunch(p, parsed, easyLaunchRules); err != nil {
		t.Fatal(err)
	}
	if p.GenesisHash != lf.Hash || p.GenesisTimestamp != lf.Timestamp || p.GenesisMessage != lf.Message ||
		p.GenesisNonce != lf.Nonce {
		t.Fatalf("genesis alanları yazılmadı: %+v", p)
	}
	g, err := NewGenesisBlock(p)
	if err != nil || g.Hash != lf.Hash || !MeetsTarget(g.Hash, p.Bits) || g.Timestamp != btc976600Time+900 {
		t.Fatalf("genesis %+v: %v", g, err)
	}
	if d, err := p.SigDomain(); err != nil || d != sigTag+"|mainnet|"+lf.Hash {
		t.Fatalf("imza alanı %q: %v", d, err)
	}

	// Bağımsız betiğin aynı dosyadan ürettiği vektör, Bitcoin alanları dahil Go kodunun hesabıdır.
	file := loadGenesisVector(t, filepath.Join("testdata", "launch", "easy_mainnet_vector.json"))
	if want := expectedVector(t, p, lf, easyLaunchRules); !reflect.DeepEqual(file, want) {
		t.Fatalf("örnek vektör Go kodunun hesabıyla aynı değil:\ndosya %+v\nGo    %+v", file, want)
	}
	if file.BitcoinHash != btc976600Hash || file.BitcoinTime != btc976600Time || file.BitcoinConfirmationHash != btc976601Hash {
		t.Fatalf("örnek vektörün Bitcoin alanları beklenen blokları göstermiyor: %+v", file)
	}

	// mustLaunch aynı dosyayla dolu parametre döndürür; boş dosyayla parametre genesis'siz kalır.
	if q := mustLaunch(easyMainnet(), raw, easyLaunchRules); q.GenesisHash != lf.Hash {
		t.Fatalf("mustLaunch genesis'i yazmadı: %+v", q)
	}
	if q := mustLaunch(easyMainnet(), []byte("{}\n"), easyLaunchRules); hasGenesis(q) {
		t.Fatalf("boş dosya genesis yazdı: %+v", q)
	}

	// Gerçek tavan bu örneği reddeder (tabanı tavandan kolaydır): ikili onunla başlamaz, düğüm de
	// böyle bir "mainnet" ile zincir açmaz. Hedefler seçilmiş sayılır ki ret tavandan gelsin.
	realCeiling := mainnetLaunchRules
	realCeiling.targetsChosen = true
	if err := applyLaunch(easyMainnet(), parsed, realCeiling); !errors.Is(err, ErrBadLaunchFile) ||
		!strings.Contains(err.Error(), "tavandan") {
		t.Fatalf("gerçek tavan kolay hedefli örneği reddetmedi: %v", err)
	}
	if !panics(func() { mustLaunch(easyMainnet(), raw, realCeiling) }) {
		t.Fatal("gerçek tavanla geçersiz dosya paniğe yol açmadı")
	}
	// Hedefler seçilmemişken (yer tutucu) başka her şartı tutan dosya da reddedilir.
	unchosen := easyLaunchRules
	unchosen.targetsChosen = false
	if err := applyLaunch(easyMainnet(), parsed, unchosen); !errors.Is(err, ErrBadLaunchFile) ||
		!strings.Contains(err.Error(), "henüz seçilmedi") {
		t.Fatalf("hedefleri seçilmemiş ağ dolu dosyayı reddetmedi: %v", err)
	}
	if !panics(func() { mustLaunch(easyMainnet(), []byte("{}"), easyLaunchRules) }) {
		t.Fatal("kanonik olmayan boş dosya paniğe yol açmadı")
	}
	if err := p.Validate(); !errors.Is(err, ErrBadParams) {
		t.Fatalf("kolay tabanlı mainnet kopyası Validate'ten geçti: %v", err)
	}
	if _, err := NewBlockchain(t.TempDir(), p); !errors.Is(err, ErrBadParams) {
		t.Fatalf("kolay tabanlı mainnet kopyasıyla zincir açıldı: %v", err)
	}
}

// Lansman şartlarından herhangi biri tutmazsa dosya reddedilir ve parametreler değişmez. Her örnek
// bir şartı hedefler ve hata metninin o şartın denetiminden geldiği denetlenir: bir şartın denetimi
// kaldırılırsa, örneği başka bir denetimin reddetmesi testi yeşil bırakamaz. Mesajı ya da zaman
// damgasını değiştiren örnekler yeniden kazılır (genesis özeti tutar); yoksa onları özet
// uyuşmazlığı reddederdi.
func TestLaunchRejected(t *testing.T) {
	_, valid := easyLaunch(t)
	edited := func(mutate func(lf *launchFile)) *launchFile {
		lf := *valid
		lf.BitcoinHeaders = append([]string(nil), valid.BitcoinHeaders...)
		mutate(&lf)
		return &lf
	}
	remined := func(timestamp int64, message string, headers ...string) *launchFile {
		return minedLaunch(t, easyMainnet(), timestamp, message, headers...)
	}
	broken := func(header string) string { // nonce'un son yarım baytı değişir
		if strings.HasSuffix(header, "0") {
			return header[:len(header)-1] + "1"
		}
		return header[:len(header)-1] + "0"
	}
	// hashAt geçerli örneğin alanlarıyla, verilen nonce'taki genesis özetidir. minedLaunch 0'dan
	// başlayıp hedefi karşılayan ilk nonce'ta durur; daha küçük her nonce'un özeti hedefi karşılamaz.
	hashAt := func(nonce uint64) string {
		q := *easyMainnet()
		q.GenesisTimestamp, q.GenesisMessage, q.GenesisNonce, q.GenesisHash = valid.Timestamp, valid.Message, nonce, "aranıyor"
		g, err := NewGenesisBlock(&q)
		if err != nil {
			t.Fatal(err)
		}
		return g.Hash
	}
	if valid.Nonce == 0 || hashAt(valid.Nonce) != valid.Hash || MeetsTarget(hashAt(0), easyMainnet().Bits) {
		t.Fatalf("geçerli örnek beklenen biçimde değil: nonce %d, özet %s", valid.Nonce, valid.Hash)
	}
	prefix, at := mainnetGenesisMessagePrefix, valid.Timestamp
	cases := []struct {
		name string
		lf   *launchFile
		want string
	}{
		{"başka ağ", edited(func(lf *launchFile) { lf.Network = "testnet" }), "ağı için"},
		{"büyük harfli hedef", edited(func(lf *launchFile) { lf.Bits = "200FFFFF" }), `başlangıç hedefi "200FFFFF"`},
		{"başka hedef", edited(func(lf *launchFile) { lf.Bits = "1f00ffff" }), `başlangıç hedefi "1f00ffff"`},
		{"başka taban", edited(func(lf *launchFile) { lf.PowLimitBits = "1f00ffff" }), `taban "1f00ffff"`},
		{"sıfır zaman", remined(0, valid.Message, btc976600, btc976601), "zaman damgası 0"},
		{"negatif zaman", remined(-1, valid.Message, btc976600, btc976601), "zaman damgası -1"},
		{"T_g t_b'den küçük", remined(btc976600Time-1, valid.Message, btc976600, btc976601), "t_b = "},
		{"başlık yok", edited(func(lf *launchFile) { lf.BitcoinHeaders = nil }), "tam iki Bitcoin başlığı"},
		{"tek başlık", edited(func(lf *launchFile) { lf.BitcoinHeaders = []string{btc976600} }), "tam iki Bitcoin başlığı"},
		{"üç başlık", edited(func(lf *launchFile) { lf.BitcoinHeaders = append(lf.BitcoinHeaders, btc976601) }),
			"tam iki Bitcoin başlığı"},
		{"yer değiştirmiş başlıklar", remined(at, prefix+btc976601Hash, btc976601, btc976600), "bağlanmıyor"},
		{"aynı başlık iki kez", edited(func(lf *launchFile) { lf.BitcoinHeaders[1] = btc976600 }), "bağlanmıyor"},
		{"ilk başlığın iş ispatı bozuk", edited(func(lf *launchFile) { lf.BitcoinHeaders[0] = broken(btc976600) }),
			"mesajdaki Bitcoin başlığı: iş ispatı"},
		{"ikinci başlığın iş ispatı bozuk", edited(func(lf *launchFile) { lf.BitcoinHeaders[1] = broken(btc976601) }),
			"onaylayan Bitcoin başlığı: iş ispatı"},
		{"büyük harfli başlık", edited(func(lf *launchFile) { lf.BitcoinHeaders[0] = strings.ToUpper(btc976600) }),
			"mesajdaki Bitcoin başlığı: 160 küçük harf"},
		{"kısa başlık", edited(func(lf *launchFile) { lf.BitcoinHeaders[1] = btc976601[:158] }),
			"onaylayan Bitcoin başlığı: 160 küçük harf"},
		{"zorluk tabanının dışındaki başlıklar", remined(at, prefix+btc0Hash, btc0, btc1), "gerçek Bitcoin zorluğu"},
		{"mesajda onaylayan bloğun özeti", remined(at, prefix+btc976601Hash, btc976600, btc976601), `mesaj "`},
		{"mesajda büyük harfli özet", remined(at, prefix+strings.ToUpper(btc976600Hash), btc976600, btc976601), `mesaj "`},
		{"mesajın sonunda fazladan karakter", remined(at, valid.Message+" ", btc976600, btc976601), `mesaj "`},
		{"mesajda yükseklik", remined(at, prefix+"976600 "+btc976600Hash, btc976600, btc976601), `mesaj "`},
		{"başka nonce", edited(func(lf *launchFile) { lf.Nonce++ }), "genesis özeti"},
		{"özeti tutan ama hedefi karşılamayan nonce", edited(func(lf *launchFile) { lf.Nonce, lf.Hash = 0, hashAt(0) }),
			"genesis özeti"},
		{"başka özet", edited(func(lf *launchFile) { lf.Hash = strings.Repeat("0", 64) }), "genesis özeti"},
		{"büyük harfli özet", edited(func(lf *launchFile) { lf.Hash = strings.ToUpper(lf.Hash) }), "genesis özeti"},
		{"boş özet", edited(func(lf *launchFile) { lf.Hash = "" }), ErrNoGenesis.Error()},
	}
	for _, c := range cases {
		p := easyMainnet()
		err := applyLaunch(p, c.lf, easyLaunchRules)
		if !errors.Is(err, ErrBadLaunchFile) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (hata metninde beklenen parça: %q)", c.name, err, c.want)
		}
		if hasGenesis(p) {
			t.Errorf("%s: reddedilen dosya parametreleri değiştirdi: %+v", c.name, p)
		}
	}
	// Zorluk tabanı örneğinde tabanın dışında kalmak dışında her şey tutar: taban gevşetilince aynı
	// dosya kabul edilir.
	loose := easyLaunchRules
	loose.bitcoinMaxBits = 0x1d00ffff
	if err := applyLaunch(easyMainnet(), remined(at, prefix+btc0Hash, btc0, btc1), loose); err != nil {
		t.Errorf("Bitcoin zorluk tabanı gevşetilince zorluk 1 başlıklı dosya reddedildi: %v", err)
	}

	// Dosya geçerli olsa da ağın parametreleriyle tutmuyorsa reddedilir.
	other := easyMainnet()
	other.Bits = 0x1f00ffff
	if err := applyLaunch(other, valid, easyLaunchRules); !errors.Is(err, ErrBadLaunchFile) || hasGenesis(other) {
		t.Errorf("başka başlangıç hedefli ağ dosyayı kabul etti: %v", err)
	}
	// Taban kuralını tutmayan parametreler: kural tabanı tavana (200fffff) kırpar, 1f00ffff değil.
	// Hedef şartları genesis'ten önce denetlenir; bu dosya için nonce aranmaz.
	flat := easyMainnet()
	flat.Bits, flat.PowLimitBits = 0x1f00ffff, 0x1f00ffff
	lf := *valid
	lf.Bits, lf.PowLimitBits = "1f00ffff", "1f00ffff"
	if err := applyLaunch(flat, &lf, easyLaunchRules); !errors.Is(err, ErrBadLaunchFile) || hasGenesis(flat) ||
		!strings.Contains(err.Error(), "min(başlangıç hedefi") {
		t.Errorf("taban kuralını tutmayan parametreler kabul edildi: %v", err)
	}
	if err := easyLaunchRules.checkTargets(flat); err == nil {
		t.Error("taban kuralını tutmayan hedefler checkTargets'ten geçti")
	}
	flat.PowLimitBits = 0x200fffff
	if err := easyLaunchRules.checkTargets(flat); err != nil {
		t.Errorf("tavana kırpılmış taban reddedildi: %v", err)
	}
}

// Dosya ya tam olarak boş nesnedir ya da eksiksiz ve kanoniktir.
func TestParseLaunchFile(t *testing.T) {
	for _, empty := range []string{"{}\n", "{}\r\n"} {
		if lf, err := parseLaunchFile([]byte(empty)); lf != nil || err != nil {
			t.Errorf("%q: %+v %v", empty, lf, err)
		}
	}
	_, valid := easyLaunch(t)
	raw, err := valid.encode()
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if lf, err := parseLaunchFile([]byte(strings.ReplaceAll(text, "\n", "\r\n"))); err != nil || !reflect.DeepEqual(lf, valid) {
		t.Fatalf("CRLF satır sonlu dosya: %+v %v", lf, err)
	}
	nonceLine := fmt.Sprintf("\"nonce\": %d,", valid.Nonce)
	bad := []struct{ name, text string }{
		{"boş dosya", ""},
		{"satır sonu olmayan boş nesne", "{}"},
		{"baştaki boşluk", " {}\n"},
		{"null", "null\n"},
		{"dizi", "[]\n"},
		{"sonda satır sonu yok", strings.TrimSuffix(text, "\n")},
		{"fazladan satır", text + "\n"},
		{"iki nesne", text + text},
		{"bilinmeyen alan", strings.Replace(text, "{\n", "{\n  \"height\": 976600,\n", 1)},
		{"yinelenen anahtar", strings.Replace(text, "{\n", "{\n  \"network\": \"mainnet\",\n", 1)},
		{"büyük harfli anahtar", strings.Replace(text, `"nonce"`, `"Nonce"`, 1)},
		{"eksik alan", strings.Replace(text, "  \"bits\": \"200fffff\",\n", "", 1)},
		{"kesirli zaman", strings.Replace(text, "1795103757", "1795103757.0", 1)},
		{"metin nonce", strings.Replace(text, nonceLine, fmt.Sprintf("\"nonce\": \"%d\",", valid.Nonce), 1)},
		{"negatif nonce", strings.Replace(text, nonceLine, fmt.Sprintf("\"nonce\": -%d,", valid.Nonce), 1)},
		{"dört boşluk girinti", strings.ReplaceAll(text, "\n  ", "\n    ")},
		{"kaçış dizili mesaj", strings.Replace(text, "Q-BTC", `\`+"u0051-BTC", 1)},
		{"BOM", string(rune(0xFEFF)) + text},
		{"satır sonunda boşluk", strings.Replace(text, ",\n", ", \n", 1)},
		{"anahtar sırası değişmiş", strings.Replace(text, "  \"network\": \"mainnet\",\n  \"timestamp\": 1795103757,\n",
			"  \"timestamp\": 1795103757,\n  \"network\": \"mainnet\",\n", 1)},
	}
	for _, c := range bad {
		if c.text == text {
			t.Fatalf("%s: bozma metni değiştirmedi", c.name)
		}
		if lf, err := parseLaunchFile([]byte(c.text)); !errors.Is(err, ErrBadLaunchFile) || lf != nil {
			t.Errorf("%s: %+v %v", c.name, lf, err)
		}
	}
}

func TestParseBitcoinHeader(t *testing.T) {
	limit, ok := compactToBig(launchBitcoinMaxBits)
	own, ok2 := compactToBig(0x17021673)
	if !ok || !ok2 || new(big.Int).Lsh(own, 4).Cmp(limit) != 0 {
		t.Fatal("Bitcoin zorluk tabanı 976600. bloğun hedefinin 16 katı değil")
	}
	h, err := parseBitcoinHeader(btc976600, limit)
	if err != nil || h != (bitcoinHeader{btc976600Hash, btc976600Prev, btc976600Time}) {
		t.Fatalf("976600: %+v %v", h, err)
	}
	c, err := parseBitcoinHeader(btc976601, limit)
	if err != nil || c.hash != btc976601Hash || c.prev != btc976600Hash {
		t.Fatalf("976601: %+v %v", c, err)
	}
	// Tabanın tam sınırı: bloğun kendi hedefi kabul edilir, bir eksiği edilmez.
	if _, err := parseBitcoinHeader(btc976600, own); err != nil {
		t.Fatalf("kendi hedefi taban olan başlık reddedildi: %v", err)
	}
	if _, err := parseBitcoinHeader(btc976600, new(big.Int).Sub(own, big.NewInt(1))); err == nil {
		t.Fatal("tabandan kolay hedefli başlık kabul edildi")
	}
	// 0 ve 1. bloklar gerçek ve birbirine bağlıdır; yalnızca zorluk tabanı gevşetilince geçerler.
	loose, _ := compactToBig(0x1d00ffff)
	for _, s := range []string{btc0, btc1} {
		if _, err := parseBitcoinHeader(s, limit); err == nil {
			t.Fatal("zorluk 1 başlığı gerçek tabanla kabul edildi")
		}
	}
	g, err := parseBitcoinHeader(btc0, loose)
	if err != nil || g.hash != btc0Hash || g.time != 1231006505 {
		t.Fatalf("0. blok: %+v %v", g, err)
	}
	if b1, err := parseBitcoinHeader(btc1, loose); err != nil || b1.prev != btc0Hash {
		t.Fatalf("1. blok: %+v %v", b1, err)
	}
	bad := []struct{ name, header string }{
		{"boş", ""},
		{"kısa", btc976600[:158]},
		{"uzun", btc976600 + "00"},
		{"büyük harf", strings.ToUpper(btc976600)},
		{"boşluklu", btc976600[:80] + " " + btc976600[81:]},
		{"nonce'u değişmiş", btc976600[:159] + "b"},
		{"kanonik olmayan nBits", btc976600[:144] + "ff0f0017" + btc976600[152:]},
		{"işaretli nBits", btc976600[:144] + "00008017" + btc976600[152:]},
	}
	for _, c := range bad {
		if _, err := parseBitcoinHeader(c.header, limit); err == nil {
			t.Errorf("%s: kabul edildi", c.name)
		}
	}

	// Kanonik olmayan nBits iş ispatından bağımsız reddedilir. Yukarıdaki örnekte nBits değişince
	// özet de değişir ve iş ispatı da tutmaz; burada iki sahte başlık kendi hedefini karşılar.
	// 0x20007fff ile 0x1f7fff00 aynı hedeftir, yalnızca ikincisi kanoniktir.
	fakeHeader := func(bits uint32) string {
		target, ok := compactToBig(bits)
		if !ok {
			t.Fatalf("%08x çözülemedi", bits)
		}
		raw := make([]byte, bitcoinHeaderSize)
		binary.LittleEndian.PutUint32(raw[72:76], bits)
		for nonce := uint32(0); nonce < 1<<20; nonce++ {
			binary.LittleEndian.PutUint32(raw[76:80], nonce)
			digest := hashHeader(raw)
			reverseBytes(digest[:])
			if new(big.Int).SetBytes(digest[:]).Cmp(target) <= 0 {
				return hex.EncodeToString(raw)
			}
		}
		t.Fatalf("%08x için nonce bulunamadı", bits)
		return ""
	}
	wide := new(big.Int).Lsh(big.NewInt(1), 255)
	if _, err := parseBitcoinHeader(fakeHeader(0x1f7fff00), wide); err != nil {
		t.Fatalf("kanonik nBits'li sahte başlık reddedildi: %v", err)
	}
	if _, err := parseBitcoinHeader(fakeHeader(0x20007fff), wide); err == nil || !strings.Contains(err.Error(), "kanonik") {
		t.Fatalf("kanonik olmayan nBits'li, iş ispatı tutan başlık: %v", err)
	}
}
