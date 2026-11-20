package core

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// Mainnet lansman dosyası (docs/LANSMAN.md §4.2). Lansman commit'i Go kaynağına dokunmaz: genesis
// bu dosyayla gelir ve ikiliye gömülür. Lansmandan önce dosya boş bir nesnedir ("{}") ve
// MainnetParams genesis'siz kalır. Dolu ama şartları tutmayan bir dosyayla derlenen ikili hiçbir
// ağda çalışmaz (paket başlatılırken panikler): yarım ya da bozuk bir lansman dosyası sessizce
// "genesis yok" diye okunamaz.
//
//go:embed mainnet.json
var mainnetLaunchJSON []byte

// ErrBadLaunchFile lansman dosyası çözülemedi, kanonik biçimde değil ya da lansman şartlarını
// tutmuyor.
var ErrBadLaunchFile = errors.New("geçersiz lansman dosyası")

// mainnetGenesisMessagePrefix mainnet genesis mesajının önekidir; ardından mesajdaki Bitcoin
// bloğunun özeti gelir (gezgin sitelerindeki gösterimle, 64 küçük harf hex). Blok yüksekliği
// mesaja girmez: başlıkta yükseklik yoktur ve elle girilen bir sayı doğrulanamaz.
const mainnetGenesisMessagePrefix = "Q-BTC mainnet genesis | BTC "

// mainnetFloorShift mainnet taban kuralının üssüdür: taban, hedef cinsinden tam olarak
// min(başlangıç hedefi · 2^16, mainnetMaxPowLimitBits) olmalıdır (kullanıcı kararı, lansman
// kontrol listesi madde 6a). Validate bu oranı denetlemez; lansman dosyası denetler.
const mainnetFloorShift = 16

// launchBitcoinMaxBits lansman dosyasındaki Bitcoin başlıklarının taşıyabileceği en kolay hedeftir:
// 976600. bloğun (2026-11-19, nBits 0x17021673, zorluk yaklaşık 1,35e14) hedefinin 16 katı. Pay
// geniştir: Bitcoin zorluğu iki haftada bir ayarlanır ve aşağı yönlü ayarlamalar gerçek başlıkları
// reddettirmemelidir. Tam provada yenilenir; bağımsız betikteki (scripts/genesis_vektor.py)
// BITCOIN_MAX_BITS ile birlikte.
const launchBitcoinMaxBits = 0x17216730

// bitcoinHeaderSize Bitcoin blok başlığının bayt uzunluğudur.
const bitcoinHeaderSize = 80

// launchRules lansman dosyasına uygulanan sınırlardır. Üretimde mainnetLaunchRules kullanılır;
// testler yalnızca tavanı gevşetir (gerçek tavanda nonce aramak testte yapılamaz).
type launchRules struct {
	targetsChosen   bool   // başlangıç hedefi ve taban seçildi mi; false ise dolu dosya reddedilir
	maxPowLimitBits uint32 // taban bundan kolay olamaz
	floorShift      uint   // taban = min(başlangıç hedefi << floorShift, tavan)
	bitcoinMaxBits  uint32 // Bitcoin başlıklarının taşıyabileceği en kolay hedef
}

var mainnetLaunchRules = launchRules{
	targetsChosen:   mainnetTargetsChosen,
	maxPowLimitBits: mainnetMaxPowLimitBits,
	floorShift:      mainnetFloorShift,
	bitcoinMaxBits:  launchBitcoinMaxBits,
}

// launchFile lansman dosyasının içeriğidir. Alanlar bağımsız betiğin (scripts/genesis_vektor.py)
// girdi biçimidir: betik dosyayı doğrudan okur, "hash" alanını kendi hesabıyla karşılaştırır ve
// vektörü (testdata/mainnet_vector.json) bundan üretir. BitcoinHeaders 80 baytlık iki başlıktır
// (hex): ilki mesajda özeti geçen blok, ikincisi onu onaylayan blok.
type launchFile struct {
	Network        string   `json:"network"`
	Timestamp      int64    `json:"timestamp"`
	Message        string   `json:"message"`
	Bits           string   `json:"bits"`
	PowLimitBits   string   `json:"pow_limit_bits"`
	Nonce          uint64   `json:"nonce"`
	Hash           string   `json:"hash"`
	BitcoinHeaders []string `json:"bitcoin_headers"`
}

// encode dosyanın kanonik baytlarını üretir: alanlar yukarıdaki sırayla, iki boşluk girinti, sonda
// satır sonu. Bağımsız betiğin yazdığı vektör dosyalarıyla aynı yazımdır.
func (lf *launchFile) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(lf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// parseLaunchFile dosyayı katı biçimde çözer. Boş nesne ("{}" ve satır sonu) lansman öncesidir:
// nil döner. Bunun dışında dosya eksiksiz ve kanonik olmalıdır: çözülen içerik yeniden
// kodlandığında girdiyle bayt bayt aynı çıkmalıdır (satır sonu CRLF olabilir). Böylece bilinmeyen
// ya da eksik alan, yinelenen anahtar, harf büyüklüğü farklı anahtar, kesirli sayı ve artık veri
// reddedilir.
func parseLaunchFile(raw []byte) (*launchFile, error) {
	text := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	if bytes.Equal(text, []byte("{}\n")) {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	lf := new(launchFile)
	if err := dec.Decode(lf); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadLaunchFile, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: nesneden sonra artık veri var", ErrBadLaunchFile)
	}
	canon, err := lf.encode()
	if err != nil || !bytes.Equal(canon, text) {
		return nil, fmt.Errorf("%w: dosya kanonik biçimde değil", ErrBadLaunchFile)
	}
	return lf, nil
}

// bitcoinHeader doğrulanmış bir Bitcoin başlığının lansman için gereken alanlarıdır. Özetler gezgin
// sitelerindeki gösterimledir (bayt sırası ters, küçük harf hex).
type bitcoinHeader struct {
	hash string
	prev string
	time int64
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func reverseBytes(b []byte) {
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
}

// parseBitcoinHeader 80 baytlık Bitcoin başlığını (160 küçük harf hex) doğrular: nBits kanonik
// olmalı, başlığın çift SHA-256 özeti küçük-endian tamsayı olarak kendi hedefini karşılamalı ve
// hedef maxTarget'tan kolay olmamalıdır. Başlığın en çok iş taşıyan Bitcoin zincirinde olduğu
// buradan bilinemez; özet herkesçe gezgin sitelerinden denetlenir.
func parseBitcoinHeader(s string, maxTarget *big.Int) (bitcoinHeader, error) {
	var h bitcoinHeader
	if len(s) != 2*bitcoinHeaderSize || !isLowerHex(s) {
		return h, errors.New("160 küçük harf hex karakter olmalı")
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return h, err
	}
	// Düzen (küçük-endian): sürüm 4, önceki özet 32, Merkle kökü 32, zaman 4, nBits 4, nonce 4.
	bits := binary.LittleEndian.Uint32(raw[72:76])
	target, ok := compactToBig(bits)
	if !ok || bigToCompact(target) != bits {
		return h, fmt.Errorf("hedef kodlaması %08x geçersiz ya da kanonik değil", bits)
	}
	first := sha256.Sum256(raw)
	digest := sha256.Sum256(first[:])
	reverseBytes(digest[:])
	if new(big.Int).SetBytes(digest[:]).Cmp(target) > 0 {
		return h, fmt.Errorf("iş ispatı kendi hedefini (%08x) karşılamıyor", bits)
	}
	if target.Cmp(maxTarget) > 0 {
		return h, fmt.Errorf("hedef %08x gerçek Bitcoin zorluğu ölçeğinde değil (en kolay %08x)", bits,
			bigToCompact(maxTarget))
	}
	prev := append([]byte(nil), raw[4:36]...)
	reverseBytes(prev)
	h.hash = hex.EncodeToString(digest[:])
	h.prev = hex.EncodeToString(prev)
	h.time = int64(binary.LittleEndian.Uint32(raw[68:72]))
	return h, nil
}

// checkTargets p'nin başlangıç hedefi ile tabanının lansman kuralını tuttuğunu denetler: başlangıç
// hedefi tabandan kolay değildir ve taban tam olarak min(başlangıç hedefi << floorShift, tavan)'dır
// (LaunchTargets'in ürettiği değer). Kanonik compact bir hedefin 2^16 katı da tam olarak kodlanır.
func (r launchRules) checkTargets(p *Params) error {
	start, err := TargetFromBits(p.Bits)
	if err != nil {
		return fmt.Errorf("başlangıç hedefi %08x: %w", p.Bits, err)
	}
	floor, err := TargetFromBits(p.PowLimitBits)
	if err != nil {
		return fmt.Errorf("taban %08x: %w", p.PowLimitBits, err)
	}
	ceiling, err := TargetFromBits(r.maxPowLimitBits)
	if err != nil {
		return fmt.Errorf("tavan %08x: %w", r.maxPowLimitBits, err)
	}
	if floor.Cmp(ceiling) > 0 {
		return fmt.Errorf("taban %08x tavandan (%08x) kolay", p.PowLimitBits, r.maxPowLimitBits)
	}
	if start.Cmp(floor) > 0 {
		return fmt.Errorf("başlangıç hedefi %08x tabandan (%08x) kolay", p.Bits, p.PowLimitBits)
	}
	want := new(big.Int).Lsh(start, r.floorShift)
	if want.Cmp(ceiling) > 0 {
		want = ceiling
	}
	if floor.Cmp(want) != 0 {
		return fmt.Errorf("taban %08x; min(başlangıç hedefi · 2^%d, %08x) = %08x olmalı", p.PowLimitBits,
			r.floorShift, r.maxPowLimitBits, bigToCompact(want))
	}
	return nil
}

// applyLaunch lansman dosyasının p ağının lansman şartlarını tuttuğunu denetler ve genesis
// alanlarını p'ye yazar; hata durumunda p değişmez. Şartlar (docs/MAINNET-ONCESI.md madde 6,
// docs/LANSMAN.md §4.2): dosya bu ağ içindir; ağın hedefleri seçilmiştir (yer tutucu değildir) ve
// dosyadaki hedefler p.Bits ile p.PowLimitBits'tir; hedefler taban kuralını tutar (checkTargets);
// iki Bitcoin başlığı kendi iş ispatını taşır, hedefleri gerçek Bitcoin zorluğu ölçeğindedir ve
// ikincisi ilkine bağlanır; mesaj tam olarak önek ile ilk başlığın özetidir; T_g, o bloğun
// zamanından (t_b) küçük değildir; bu alanlardan kurulan genesis'in özeti dosyadaki özettir ve
// başlangıç hedefini karşılar. "Yayın anı − T_g" şartı veriden denetlenemez. Yükleyici saate
// bakmaz: paket başlatılırken çalışır ve her makinede aynı sonucu vermelidir; gelecekteki bir T_g'yi
// TestMainnetLaunchFile ve bağımsız betiğin "uret" komutu yakalar.
func applyLaunch(p *Params, lf *launchFile, rules launchRules) error {
	if lf.Network != p.Name {
		return fmt.Errorf("%w: dosya %q ağı için, beklenen %q", ErrBadLaunchFile, lf.Network, p.Name)
	}
	if !rules.targetsChosen {
		return fmt.Errorf("%w: %s başlangıç hedefi ve tabanı henüz seçilmedi (yer tutucu); önce ölçülen hashrate'ten seçilip yazılmalı",
			ErrBadLaunchFile, p.Name)
	}
	if want := fmt.Sprintf("%08x", p.Bits); lf.Bits != want {
		return fmt.Errorf("%w: başlangıç hedefi %q, %s parametresi %s", ErrBadLaunchFile, lf.Bits, p.Name, want)
	}
	if want := fmt.Sprintf("%08x", p.PowLimitBits); lf.PowLimitBits != want {
		return fmt.Errorf("%w: taban %q, %s parametresi %s", ErrBadLaunchFile, lf.PowLimitBits, p.Name, want)
	}
	if err := rules.checkTargets(p); err != nil {
		return fmt.Errorf("%w: %v", ErrBadLaunchFile, err)
	}
	if lf.Timestamp < 1 {
		return fmt.Errorf("%w: zaman damgası %d", ErrBadLaunchFile, lf.Timestamp)
	}
	if len(lf.BitcoinHeaders) != 2 {
		return fmt.Errorf("%w: tam iki Bitcoin başlığı olmalı (mesajdaki blok ve onu onaylayan blok), %d var",
			ErrBadLaunchFile, len(lf.BitcoinHeaders))
	}
	bitcoinMax, ok := compactToBig(rules.bitcoinMaxBits)
	if !ok {
		return fmt.Errorf("%w: Bitcoin zorluk tabanı %08x çözülemedi", ErrBadLaunchFile, rules.bitcoinMaxBits)
	}
	block, err := parseBitcoinHeader(lf.BitcoinHeaders[0], bitcoinMax)
	if err != nil {
		return fmt.Errorf("%w: mesajdaki Bitcoin başlığı: %v", ErrBadLaunchFile, err)
	}
	confirmation, err := parseBitcoinHeader(lf.BitcoinHeaders[1], bitcoinMax)
	if err != nil {
		return fmt.Errorf("%w: onaylayan Bitcoin başlığı: %v", ErrBadLaunchFile, err)
	}
	if confirmation.prev != block.hash {
		return fmt.Errorf("%w: onaylayan Bitcoin başlığı mesajdaki bloğa bağlanmıyor", ErrBadLaunchFile)
	}
	if want := mainnetGenesisMessagePrefix + block.hash; lf.Message != want {
		return fmt.Errorf("%w: mesaj %q, beklenen %q", ErrBadLaunchFile, lf.Message, want)
	}
	if lf.Timestamp < block.time {
		return fmt.Errorf("%w: T_g (%d) mesajdaki Bitcoin bloğunun zamanından (t_b = %d) küçük", ErrBadLaunchFile,
			lf.Timestamp, block.time)
	}
	q := *p
	q.GenesisTimestamp, q.GenesisMessage, q.GenesisNonce, q.GenesisHash = lf.Timestamp, lf.Message, lf.Nonce, lf.Hash
	g, err := NewGenesisBlock(&q)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadLaunchFile, err)
	}
	if g.Hash != lf.Hash || !MeetsTarget(g.Hash, p.Bits) {
		return fmt.Errorf("%w: genesis özeti %s; dosyadaki %q ile aynı olmalı ve hedefi (%08x) karşılamalı",
			ErrBadLaunchFile, g.Hash, lf.Hash, p.Bits)
	}
	*p = q
	return nil
}

// mustLaunch gömülü lansman dosyasını p'ye uygular; dosya boş nesneyse p genesis'siz kalır. Dolu ama
// geçersiz dosya bir derleme girdisi hatasıdır ve withEmission'daki gibi paket başlatılırken
// panikler: böyle bir ikili hiçbir ağda çalışmamalıdır.
func mustLaunch(p *Params, raw []byte, rules launchRules) *Params {
	lf, err := parseLaunchFile(raw)
	if err == nil && lf != nil {
		err = applyLaunch(p, lf, rules)
	}
	if err != nil {
		panic(fmt.Sprintf("%s lansman dosyası (core/mainnet.json): %v", p.Name, err))
	}
	return p
}
