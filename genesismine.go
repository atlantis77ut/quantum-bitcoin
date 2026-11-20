package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
	"quantum-bitcoin/gpu"
)

// Mainnet genesis kazım aracı ve ön-uçuşu (docs/LANSMAN.md §4.1): -mine-genesis, -launch-preflight.
// Çekirdek (lansman girdilerinin ve yerel saatin denetimi, T_g'yi tazeleyen arama, kurulan dosyanın
// yükleyiciyle doğrulanması) core/launchmine.go'dadır; burada ölçüm raporuyla öz-tutarlılık, aygıt
// seçimi, dosya yazımı ve lansman kaydı vardır. Araç yalnızca mainnet içindir ve kuralları
// gevşetecek bir bayrağı yoktur.

const launchRecordSchema = 1

// Ön-uçuştaki kısa hashrate ölçümü: ısınma ölçümü atılır, ölçüm rapordaki ortancanın ±%10'u içinde
// olmalıdır (docs/LANSMAN.md §3.4).
const (
	preflightWarmup    = 3 * time.Second
	preflightMeasure   = 15 * time.Second
	preflightTolerance = 0.10
)

// maxReportSize okunacak ölçüm raporunun bayt sınırıdır.
const maxReportSize = 1 << 20

// launchMaxRoundSeconds bir GPU turunun rapordaki ortanca hashrate'te en çok kaç saniye
// sürebileceğidir. Arama T_g'yi, saati ve durdurmayı tur aralarında denetler ve nonce bulunduğunda
// başlık 20 saniyeden eskiyse onu kullanmaz (core/launchmine.go genesisMaxAge): turu bundan uzun
// süren bir yapılandırma ön-uçuştan geçip kazımda hiç başarılı olamazdı. Otomatik seçilen tur
// ~100 ms sürer.
const launchMaxRoundSeconds = 1.0

// errLaunchRefused araç bir denetimden geçemedi ya da girdisi hatalı: genesis kazılmaz, dosya
// yazılmaz.
var errLaunchRefused = errors.New("lansman aracı durdu")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errLaunchRefused, fmt.Sprintf(format, args...))
}

// launchConfig aracın komut satırı girdileridir. Preflight ise denetimler ve kısa bir hashrate
// ölçümü yapılır; genesis kazılmaz ve dosya yazılmaz. Block genesis mesajına girecek, bir onay
// almış Bitcoin bloğunun, Confirmation onu onaylayan bloğun başlığıdır (hex). Report yayımlanan
// ölçüm raporu, Out lansman dosyası, Record lansman kaydıdır (boşsa ad üretilir).
type launchConfig struct {
	Preflight    bool
	Network      string
	Block        string
	Confirmation string
	Report       string
	Out          string
	Record       string
	Device       int
	Batch        uint64
}

// launchPlan core.LaunchPlan'in araçta kullanılan yüzüdür; testler sahtesini verir.
type launchPlan interface {
	Info() core.LaunchInfo
	CheckClock(now int64) error
	Mine(be core.MineBackend, clock func() int64, stop <-chan struct{}) (*core.MinedGenesis, error)
	Verify(raw []byte) (string, error)
}

// launchEnv aracın dış dünyaya dokunduğu yerlerdir; testler sahtelerini verir.
type launchEnv struct {
	now     func() time.Time
	vcs     func() (commit string, modified bool)
	newPlan func(block, confirmation string) (launchPlan, error)
	devices func() ([]gpu.Device, error)
	open    func(device int, batch uint64) (gpu.Backend, error)
	measure func(be core.MineBackend, d time.Duration) (float64, error)
	stop    <-chan struct{}
}

// measurementReport scripts/gpu-olcum.sh'in yazdığı birleşik ölçüm raporunun (docs/GPU-OLCUM.md)
// araçta kullanılan alanlarıdır.
type measurementReport struct {
	Schema      int          `json:"schema"`
	GPUHashrate bool         `json:"gpu_hashrate_mi"`
	CleanTree   bool         `json:"temiz_agac"`
	GPU         *benchReport `json:"gpu"`
}

// launchRecord bir genesis kazım denemesinin kaydıdır (docs/LANSMAN.md §3.4, §4.1). Araç bulduğu her
// genesis için ayrı bir kayıt yazar; atılan denemenin kaydı da saklanır ve imzalanıp duyurulan
// lansman kaydına eklenir. Kayıt kamuya açıklanabilir: makine adı, kullanıcı adı, yol ya da adres
// içermez. GenesisLagSeconds T_g − t_b'dir; SearchStarted aramanın başladığı an, SearchSeconds
// süresidir.
type launchRecord struct {
	Schema                  int        `json:"schema"`
	TimeUTC                 string     `json:"time_utc"`
	Network                 string     `json:"network"`
	GenesisHash             string     `json:"genesis_hash"`
	Timestamp               int64      `json:"timestamp"`
	TimestampUTC            string     `json:"timestamp_utc"`
	Nonce                   uint64     `json:"nonce"`
	Bits                    string     `json:"bits"`
	PowLimitBits            string     `json:"pow_limit_bits"`
	Message                 string     `json:"message"`
	BitcoinHash             string     `json:"bitcoin_hash"`
	BitcoinTime             int64      `json:"bitcoin_time"`
	BitcoinConfirmationHash string     `json:"bitcoin_confirmation_hash"`
	BitcoinConfirmationTime int64      `json:"bitcoin_confirmation_time"`
	GenesisLagSeconds       int64      `json:"genesis_lag_seconds"`
	SearchStarted           int64      `json:"search_started"`
	SearchSeconds           int64      `json:"search_seconds"`
	NoncesTried             uint64     `json:"nonces_tried"`
	HeadersTried            int        `json:"headers_tried"`
	Device                  gpu.Device `json:"device"`
	RoundSize               uint64     `json:"round_size"`
	KernelSHA256            string     `json:"kernel_sha256"`
	Commit                  string     `json:"commit"`
	GoVersion               string     `json:"go_version"`
	OS                      string     `json:"os"`
	Arch                    string     `json:"arch"`
	Liboqs                  string     `json:"liboqs"`
	ReportSHA256            string     `json:"report_sha256"`
	ReportMedian            float64    `json:"report_median_hash_per_sec"`
	LaunchFileSHA256        string     `json:"launch_file_sha256"`
}

// isCommitHash s'nin bir git commit kimliği (40 ya da 64 küçük harf hex) olup olmadığını söyler.
func isCommitHash(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// checkLaunchBuild ikilinin temiz bir commit'ten derlendiğini denetler: lansman kaydına giren commit
// çalışan kodu anlatmalıdır.
func checkLaunchBuild(commit string, modified bool) error {
	if !isCommitHash(commit) {
		return refuse("ikilinin commit'i bilinmiyor (%q): araç git deposunda \"go build\" ile derlenmeli", commit)
	}
	if modified {
		return refuse("ikili commit edilmemiş değişiklik içeren bir ağaçtan derlenmiş (commit %s); temiz ağaçta yeniden derlenmeli",
			commit)
	}
	return nil
}

// loadLaunchReport ölçüm raporunu okur; raporu ve dosyanın SHA-256'sını döndürür (özet lansman
// kaydına girer).
func loadLaunchReport(path string) (*measurementReport, string, error) {
	if path == "" {
		return nil, "", refuse("-launch-report verilmeli: yayımlanan ölçüm raporu (scripts/gpu-olcum.sh çıktısı)")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", refuse("ölçüm raporu okunamadı: %v", err)
	}
	defer f.Close()
	// Sınırın bir fazlası okunur: büyük ya da sonu gelmeyen bir dosya belleğe alınmadan reddedilir.
	raw, err := io.ReadAll(io.LimitReader(f, maxReportSize+1))
	if err != nil {
		return nil, "", refuse("ölçüm raporu okunamadı: %v", err)
	}
	if len(raw) > maxReportSize {
		return nil, "", refuse("ölçüm raporu %d bayttan büyük", maxReportSize)
	}
	var rep measurementReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, "", refuse("ölçüm raporu çözülemedi: %v", err)
	}
	sum := sha256.Sum256(raw)
	return &rep, hex.EncodeToString(sum[:]), nil
}

// checkLaunchReport ölçüm raporuyla öz-tutarlılığı denetler (docs/LANSMAN.md §4.1): rapor temiz bir
// commit'ten, gerçek bir GPU'da ve bu ikilinin çekirdeğiyle ölçülmüştür; mainnet başlangıç hedefi
// (bits) ve tabanı (limitBits) rapordaki ortanca hashrate'ten LaunchTargets ile hesaplanan
// değerlerdir. Bu denetim başlatıcıyı kendi hatasına karşı korur; "aynı donanım" kanıtı değildir
// (rapor da ikili de başlatıcının elindedir). Kamusal kanıt, başkalarının aynı modelde ölçümü
// yeniden üretmesidir.
func checkLaunchReport(rep *measurementReport, bits, limitBits uint32, kernel string) error {
	if rep == nil || rep.Schema != 1 || rep.GPU == nil || rep.GPU.Schema != benchReportSchema {
		return refuse("ölçüm raporu tanınmıyor: scripts/gpu-olcum.sh'in yazdığı birleşik rapor (şema 1) verilmeli")
	}
	g := rep.GPU
	if g.Backend != "gpu" || g.Device == nil {
		return refuse("ölçüm raporunda GPU ölçümü yok")
	}
	if !rep.GPUHashrate || g.NonGPUAllowed || g.Device.Type != gpu.TypeGPU {
		return refuse("ölçüm raporu gerçek bir GPU ölçümü değil (aygıt türü %s): sonucu GPU hashrate'i sayılmaz", g.Device.Type)
	}
	if !rep.CleanTree || g.Modified || !isCommitHash(g.Commit) {
		return refuse("ölçüm raporu temiz bir commit'ten değil (commit %q); temiz ağaçta yeniden ölçülmeli", g.Commit)
	}
	if g.KernelSHA256 != kernel {
		return refuse("ölçüm raporundaki çekirdek özeti %q, bu ikilinin çekirdeği %s: ölçülen kazıcı bu değil", g.KernelSHA256,
			kernel)
	}
	if g.RoundSize < gpu.MinBatch || g.RoundSize > gpu.MaxBatch {
		return refuse("ölçüm raporundaki tur boyu %d geçersiz", g.RoundSize)
	}
	if !(g.Median > 0) || math.IsInf(g.Median, 0) {
		return refuse("ölçüm raporundaki ortanca hashrate geçersiz: %v", g.Median)
	}
	if seconds := float64(g.RoundSize) / g.Median; seconds > launchMaxRoundSeconds {
		return refuse("ölçüm raporundaki tur boyu (%d) ortanca hashrate'te %.2f sn sürer, en çok %v sn olabilir: T_g ve durdurma tur aralarında denetlenir, uzun turda bulunan nonce'un başlığı bayat sayılır",
			g.RoundSize, seconds, launchMaxRoundSeconds)
	}
	start, floor, err := core.LaunchTargets(g.Median, core.MainnetParams.TargetSpacing)
	if err != nil {
		return refuse("ölçüm raporundaki ortanca hashrate'ten (%v hash/sn) hedef hesaplanamadı: %v", g.Median, err)
	}
	if g.StartBits != fmt.Sprintf("0x%08x", start) || g.FloorBits != fmt.Sprintf("0x%08x", floor) {
		return refuse("ölçüm raporunun önerdiği hedefler (%s, %s) ortanca hashrate'inden hesaplananlar (0x%08x, 0x%08x) değil",
			g.StartBits, g.FloorBits, start, floor)
	}
	if start != bits || floor != limitBits {
		return refuse("mainnet hedefleri (başlangıç %08x, taban %08x) rapordaki ortanca hashrate'ten (%v hash/sn) hesaplananlar (%08x, %08x) değil",
			bits, limitBits, g.Median, start, floor)
	}
	return nil
}

// launchDevice genesis'in kazılacağı aygıtı bulur: türü GPU olmalı, modeli ve sürücüsü ölçüm
// raporundaki aygıtınkiyle aynı olmalıdır (ölçümden sonra lansmana kadar sürücü güncellenmez).
func launchDevice(devs []gpu.Device, idx int, measured *gpu.Device) (gpu.Device, error) {
	d, err := benchDevice(devs, idx, false)
	if err != nil {
		return gpu.Device{}, refuse("genesis yalnızca GPU ile kazılır: %v", err)
	}
	if measured == nil {
		return gpu.Device{}, refuse("ölçüm raporunda aygıt bilgisi yok")
	}
	if d.Name != measured.Name || d.Driver != measured.Driver {
		return gpu.Device{}, refuse("OpenCL aygıtı %d (%s, sürücü %s) ölçüm raporundaki aygıt (%s, sürücü %s) değil", idx,
			d.Name, d.Driver, measured.Name, measured.Driver)
	}
	return d, nil
}

// checkHashrate ön-uçuştaki kısa ölçümün rapordaki ortancanın ±%10'u içinde olduğunu denetler.
func checkHashrate(measured, median float64) error {
	if !(measured > 0) || !(median > 0) || math.Abs(measured-median) > preflightTolerance*median {
		return refuse("ölçülen hashrate %.4g hash/sn, rapordaki ortanca %.4g hash/sn: fark ±%%%.0f sınırının dışında", measured,
			median, preflightTolerance*100)
	}
	return nil
}

// checkEmptyLaunchFile path'in boş lansman dosyası ("{}") olduğunu denetler: araç yalnızca onun
// üzerine yazar.
func checkEmptyLaunchFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return refuse("lansman dosyası okunamadı (araç depo kökünden çalıştırılmalı ya da -launch-out verilmeli): %v", err)
	}
	if !core.EmptyLaunchFile(raw) {
		return refuse("%s boş lansman dosyası ({}) değil: dolu bir lansman dosyasının üzerine yazılmaz", path)
	}
	return nil
}

// launchTempSuffix lansman dosyasının geçici kopyasının ekidir.
const launchTempSuffix = ".yeni"

// launchOutputs aracın arama başlamadan önce ayırdığı çıktılardır: kayıt dosyası ve lansman
// dosyasının geçici kopyası. İkisi de yalnızca yoksa yaratılır ve açık tutulur. Böylece kaydın zaten
// var olduğu, dizinlerin yazılamadığı ya da önceki bir çalıştırmadan geçici dosya kaldığı genesis
// aranmadan önce görülür; aynı lansman dosyasına yazacak ikinci bir çalıştırma da geçici dosyayı
// görüp durur. tmpLeft geçici dosyanın diskte durduğunu (yerine taşınmadığını), recordLeft kayıt
// dosyasının ayrıldığını ama içeriğinin yazılmadığını söyler. tmpInfo ve recordInfo ayrılan
// dosyaların kimliğidir: geri alma yalnızca hâlâ o dosyalarsa siler.
type launchOutputs struct {
	out        string
	tmp        string
	record     string
	tmpFile    *os.File
	recordFile *os.File
	tmpInfo    os.FileInfo
	recordInfo os.FileInfo
	tmpLeft    bool
	recordLeft bool
}

// reserveLaunchOutputs çıktıları ayırır: lansman dosyası boş olmalı, kayıt ve geçici dosya var
// olmamalı, ikisi de yaratılabilmelidir. Var olan hiçbir dosyaya dokunmaz.
func reserveLaunchOutputs(out, record string) (*launchOutputs, error) {
	if err := checkEmptyLaunchFile(out); err != nil {
		return nil, err
	}
	o := &launchOutputs{out: out, tmp: out + launchTempSuffix, record: record}
	var err error
	if o.recordFile, err = os.OpenFile(record, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, refuse("lansman kaydı %s zaten var: var olan kaydın üzerine yazılmaz (boşsa yarıda kesilmiş bir çalıştırmadan kalmıştır, silinebilir)",
				record)
		}
		return nil, refuse("lansman kaydı yaratılamadı (dizini var ve yazılabilir olmalı): %v", err)
	}
	o.recordLeft = true
	o.recordInfo, _ = o.recordFile.Stat()
	if o.tmpFile, err = os.OpenFile(o.tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err != nil {
		o.release()
		if errors.Is(err, os.ErrExist) {
			return nil, refuse("%s duruyor: yarıda kesilmiş bir çalıştırmadan kalmış ya da başka bir çalıştırma sürüyor; kalmışsa silinmeli",
				o.tmp)
		}
		return nil, refuse("lansman dosyasının geçici kopyası yaratılamadı (dizini yazılabilir olmalı): %v", err)
	}
	o.tmpLeft = true
	o.tmpInfo, _ = o.tmpFile.Stat()
	return o, nil
}

// removeIfSame path'i yalnızca hâlâ info'nun dosyasıysa siler: ayrılan dosya arama sırasında silinip
// yerine başkasının dosyası konmuşsa ona dokunulmaz. Kimlik okunamamışsa (info nil) dosya silinir.
func removeIfSame(path string, info os.FileInfo) {
	if info != nil {
		if now, err := os.Lstat(path); err != nil || !os.SameFile(info, now) {
			return
		}
	}
	os.Remove(path)
}

// release yazılmamış çıktıları geri alır: açık dosyaları kapatır, içeriği yazılmamış kaydı ve yerine
// taşınmamış geçici dosyayı siler. Yazılmış kayda ve lansman dosyasına dokunmaz; yinelenebilir.
func (o *launchOutputs) release() {
	if o.tmpFile != nil {
		o.tmpFile.Close()
		o.tmpFile = nil
	}
	if o.tmpLeft {
		removeIfSame(o.tmp, o.tmpInfo)
		o.tmpLeft = false
	}
	if o.recordFile != nil {
		o.recordFile.Close()
		o.recordFile = nil
	}
	if o.recordLeft {
		removeIfSame(o.record, o.recordInfo)
		o.recordLeft = false
	}
}

// writeAndClose data'yı f'ye yazar, diske indirir ve dosyayı kapatır. İçerik diske indirildikten
// sonraki kapatma hatası veriyi etkilemez ve yok sayılır: yazılmış dosya "yazılamadı" sayılıp
// silinmez.
func writeAndClose(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	return err
}

// syncParent path'in dizin girdisini (yeni dosya ya da ad değişikliği) diske indirmeyi dener.
// Başaramazsa yalnızca uyarır: dosyanın içeriği zaten diske indirilmiştir ve dizin eşitlemesini
// desteklemeyen dosya sistemleri vardır.
func syncParent(path string) {
	d, err := os.Open(filepath.Dir(path))
	if err == nil {
		err = d.Sync()
		if cerr := d.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		slog.Warn("dizin girdisi diske indirilemedi; dosyanın içeriği diskte", "dosya", path, "err", err)
	}
}

// writeRecord ayrılmış kayıt dosyasına içeriği yazar ve diske indirir. Yazılamazsa kayıt release
// ile silinir: yarım kayıt kalmaz. İçerik açık tutulan tanıtıcıdan yazılır; kaydın yolunun hâlâ o
// dosyayı gösterdiği geri okunarak görülür. Ayrılan boş dosya arama sırasında silinmiş ya da
// değiştirilmişse kayıt yazılmış sayılmaz ve yoldaki dosyaya dokunulmaz.
func (o *launchOutputs) writeRecord(data []byte) error {
	if o.recordFile == nil {
		return errors.New("kayıt dosyası ayrılmamış")
	}
	f := o.recordFile
	o.recordFile = nil
	if err := writeAndClose(f, data); err != nil {
		return err
	}
	o.recordLeft = false
	if back, err := os.ReadFile(o.record); err != nil || !bytes.Equal(back, data) {
		return fmt.Errorf("ayrılan kayıt dosyası arama sırasında silinmiş ya da değiştirilmiş (%v)", err)
	}
	syncParent(o.record)
	return nil
}

// commitLaunchFile lansman dosyasını yazar. İçerik geçici dosyaya yazılıp diske indirilir, diskten
// geri okunur ve verify ile doğrulanır; ancak ondan sonra, lansman dosyası hâlâ boşsa, geçici dosya
// onun yerine taşınır. Lansman dosyası böylece ya boş kalır ya da doğrulanmış içeriği taşır: yarım
// ya da doğrulanmamış içerik onun adına hiç yazılmaz. Hata durumunda geçici dosya release ile
// silinir.
func (o *launchOutputs) commitLaunchFile(raw []byte, verify func(back []byte) error) error {
	if o.tmpFile == nil {
		return errors.New("lansman dosyasının geçici kopyası ayrılmamış")
	}
	f := o.tmpFile
	o.tmpFile = nil
	if err := writeAndClose(f, raw); err != nil {
		return fmt.Errorf("lansman dosyası yazılamadı: %w", err)
	}
	back, err := os.ReadFile(o.tmp)
	if err != nil {
		return fmt.Errorf("yazılan lansman dosyası geri okunamadı: %w", err)
	}
	if !bytes.Equal(back, raw) {
		return errors.New("yazılan lansman dosyası diskten farklı okundu")
	}
	if err := verify(back); err != nil {
		return fmt.Errorf("yazılan lansman dosyası doğrulanamadı: %w", err)
	}
	if err := checkEmptyLaunchFile(o.out); err != nil {
		return err
	}
	if err := os.Rename(o.tmp, o.out); err != nil {
		return fmt.Errorf("lansman dosyası yerine taşınamadı: %w", err)
	}
	o.tmpLeft = false
	syncParent(o.out)
	return nil
}

// defaultRecordName -launch-record verilmediğinde kaydın çalışma dizinindeki adıdır.
func defaultRecordName(now time.Time) string {
	return "qbtc-genesis-" + now.UTC().Format("20060102T150405Z") + ".json"
}

// newLaunchRecord bulunan genesis'in kaydını kurar.
func newLaunchRecord(info core.LaunchInfo, g *core.MinedGenesis, dev gpu.Device, round uint64, commit, reportSum string,
	median float64, now time.Time) launchRecord {
	file := sha256.Sum256(g.File)
	return launchRecord{
		Schema: launchRecordSchema, TimeUTC: now.UTC().Format(time.RFC3339),
		Network: info.Network, GenesisHash: g.Hash,
		Timestamp: g.Timestamp, TimestampUTC: time.Unix(g.Timestamp, 0).UTC().Format(time.RFC3339),
		Nonce: g.Nonce, Bits: fmt.Sprintf("%08x", info.Bits), PowLimitBits: fmt.Sprintf("%08x", info.PowLimitBits),
		Message: info.Message, BitcoinHash: info.BitcoinHash, BitcoinTime: info.BitcoinTime,
		BitcoinConfirmationHash: info.BitcoinConfirmationHash, BitcoinConfirmationTime: info.BitcoinConfirmationTime,
		GenesisLagSeconds: g.Timestamp - info.BitcoinTime, SearchStarted: g.Started, SearchSeconds: g.Found - g.Started,
		NoncesTried: g.Tried, HeadersTried: g.Headers, Device: dev, RoundSize: round,
		KernelSHA256: gpu.KernelSHA256(), Commit: commit, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Liboqs: crypto.LibraryVersion(), ReportSHA256: reportSum, ReportMedian: median,
		LaunchFileSHA256: hex.EncodeToString(file[:]),
	}
}

// runLaunch genesis kazım aracıdır (cfg.Preflight ise ön-uçuşudur). Denetim sırası: ağ; lansman
// girdileri (genesis tanımsız, hedefler seçilmiş, Bitcoin başlıkları); yerel saat; ikilinin
// commit'i; ölçüm raporu; tur boyu; çıktılar (lansman dosyası boş, kayıt ve geçici dosya ayrılır);
// aygıt. Biri düşerse genesis aranmaz ve geride hiçbir dosya kalmaz. Kazım kipinde bulunan
// genesis'in kaydı önce yazılır; lansman dosyası geçici kopyası diskten geri okunup doğrulandıktan
// sonra yerine taşınır. Dönen kayıt nil değilse genesis bulunmuş ve kaydı yazılmıştır; hata da varsa
// lansman dosyası yazılamamış ya da doğrulanamamıştır ve boş kalmıştır.
func runLaunch(cfg launchConfig, env launchEnv) (*launchRecord, error) {
	if cfg.Network != "mainnet" {
		return nil, refuse("genesis yalnızca mainnet için kazılır: -network mainnet açıkça verilmeli (verilen %q)", cfg.Network)
	}
	plan, err := env.newPlan(cfg.Block, cfg.Confirmation)
	if err != nil {
		return nil, err
	}
	info := plan.Info()
	now := env.now()
	if err := plan.CheckClock(now.Unix()); err != nil {
		return nil, err
	}
	commit, modified := env.vcs()
	if err := checkLaunchBuild(commit, modified); err != nil {
		return nil, err
	}
	rep, reportSum, err := loadLaunchReport(cfg.Report)
	if err != nil {
		return nil, err
	}
	if err := checkLaunchReport(rep, info.Bits, info.PowLimitBits, gpu.KernelSHA256()); err != nil {
		return nil, err
	}
	median, round := rep.GPU.Median, rep.GPU.RoundSize
	if cfg.Batch != 0 && cfg.Batch != round {
		return nil, refuse("-gpu-batch %d, ölçüm raporundaki tur boyu %d: genesis ölçümle aynı tur boyuyla kazılır", cfg.Batch,
			round)
	}
	// Çıktılar arama başlamadan önce ayrılır: yazılamayacakları baştan belliyse genesis hiç aranmaz.
	// Ön-uçuş aynı denetimi yapar ve ayırdığını hemen geri alır.
	record := cfg.Record
	if record == "" {
		record = defaultRecordName(now)
	}
	outputs, err := reserveLaunchOutputs(cfg.Out, record)
	if err != nil {
		return nil, err
	}
	defer outputs.release()
	if cfg.Preflight {
		outputs.release()
	}
	devs, err := env.devices()
	if err != nil {
		return nil, refuse("OpenCL aygıtları listelenemedi: %v", err)
	}
	dev, err := launchDevice(devs, cfg.Device, rep.GPU.Device)
	if err != nil {
		return nil, err
	}
	be, err := env.open(cfg.Device, round)
	if err != nil {
		return nil, fmt.Errorf("GPU kazıcısı açılamadı: %w", err)
	}
	defer be.Close()
	slog.Info("lansman denetimleri geçti", "network", info.Network, "bitcoin_blogu", info.BitcoinHash, "t_b", info.BitcoinTime,
		"onaylayan_blok", info.BitcoinConfirmationHash, "yerel_saat_eksi_t_b_sn", now.Unix()-info.BitcoinTime,
		"baslangic_bits", fmt.Sprintf("%08x", info.Bits), "taban_bits", fmt.Sprintf("%08x", info.PowLimitBits),
		"aygit", dev.Name, "surucu", dev.Driver, "tur", round, "commit", commit, "rapor_sha256", reportSum)

	if cfg.Preflight {
		if _, err := env.measure(be, preflightWarmup); err != nil {
			return nil, fmt.Errorf("hashrate ölçülemedi: %w", err)
		}
		rate, err := env.measure(be, preflightMeasure)
		if err != nil {
			return nil, fmt.Errorf("hashrate ölçülemedi: %w", err)
		}
		if err := checkHashrate(rate, median); err != nil {
			return nil, err
		}
		slog.Info("ön-uçuş geçti: genesis kazılmadı, dosya yazılmadı", "olculen_hash_per_sn", rate,
			"rapor_ortanca_hash_per_sn", median, "fark_yuzde", (rate-median)/median*100)
		return nil, nil
	}

	slog.Info("genesis aranıyor", "mesaj", info.Message, "beklenen_sure_sn", core.MainnetParams.TargetSpacing)
	mined, err := plan.Mine(be, func() int64 { return env.now().Unix() }, env.stop)
	if err != nil {
		return nil, err
	}
	rec := newLaunchRecord(info, mined, dev, round, commit, reportSum, median, env.now())
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := outputs.writeRecord(append(data, '\n')); err != nil {
		return nil, fmt.Errorf("lansman kaydı yazılamadı, lansman dosyası da yazılmadı (bulunan genesis %s, T_g %d, nonce %d): %w",
			mined.Hash, mined.Timestamp, mined.Nonce, err)
	}
	slog.Info("lansman kaydı yazıldı", "kayit", record, "genesis", mined.Hash)
	verify := func(back []byte) error {
		hash, err := plan.Verify(back)
		if err != nil {
			return err
		}
		if hash != mined.Hash {
			return fmt.Errorf("dosyadaki genesis özeti %s, bulunan %s", hash, mined.Hash)
		}
		return nil
	}
	if err := outputs.commitLaunchFile(mined.File, verify); err != nil {
		return &rec, err
	}
	slog.Info("genesis kazıldı; lansman dosyası diskten doğrulandı ve yerine yazıldı", "genesis", rec.GenesisHash,
		"T_g", rec.Timestamp, "T_g_utc", rec.TimestampUTC, "nonce", rec.Nonce, "T_g_eksi_t_b_sn", rec.GenesisLagSeconds,
		"arama_sn", rec.SearchSeconds, "denenen_nonce", rec.NoncesTried, "denenen_baslik", rec.HeadersTried,
		"lansman_dosyasi", cfg.Out, "kayit", record,
		"siradaki_adim", "python3 -I scripts/genesis_vektor.py uret "+cfg.Out+" -o core/testdata/mainnet_vector.json")
	return &rec, nil
}

// mainnetLaunchPlan gerçek planı kurar ve arama ilerlemesini günlüğe bağlar.
func mainnetLaunchPlan(block, confirmation string) (launchPlan, error) {
	pl, err := core.NewMainnetLaunchPlan(block, confirmation)
	if err != nil {
		return nil, err
	}
	pl.Progress = func(timestamp int64, tried uint64) {
		slog.Info("genesis aranıyor: yeni başlık", "T_g", timestamp, "denenen_nonce", tried)
	}
	return pl, nil
}

// launchExitCode aracın çıkış kodudur: 0 tamam; 2 bir denetim geçmedi ya da girdi hatalı (genesis
// kazılmadı, hiçbir dosya yazılmadı); 1 çalışma hatası ya da genesis bulunduktan sonraki hata.
func launchExitCode(rec *launchRecord, err error) int {
	switch {
	case err == nil:
		return 0
	case rec != nil:
		return 1
	case errors.Is(err, errLaunchRefused), errors.Is(err, core.ErrLaunchCheck), errors.Is(err, core.ErrBadLaunchFile):
		return 2
	}
	return 1
}

// runLaunchTool aracı gerçek ortamla çalıştırır ve çıkış kodunu döndürür.
func runLaunchTool(cfg launchConfig) int {
	stop := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		// İlk sinyal aramayı tur arasında durdurur ve ayrılan çıktılar geri alınır; ikinci sinyal
		// süreci olağan yoldan sonlandırır (ör. aygıt bir turda takılmışsa).
		signal.Stop(sig)
		close(stop)
	}()
	env := launchEnv{
		now:     time.Now,
		vcs:     vcsInfo,
		newPlan: mainnetLaunchPlan,
		devices: gpu.Devices,
		open:    gpu.Open,
		measure: core.MeasureBackendHashrate,
		stop:    stop,
	}
	rec, err := runLaunch(cfg, env)
	code := launchExitCode(rec, err)
	switch {
	case err == nil:
	case rec != nil:
		slog.Error("genesis bulundu ve kaydı yazıldı, ama lansman dosyası yazılamadı ya da doğrulanamadı: deneme atılmış sayılır, kayıt saklanır",
			"err", err, "genesis", rec.GenesisHash)
	case code == 2:
		slog.Error("lansman aracı durdu: genesis kazılmadı, dosya yazılmadı", "err", err)
	default:
		slog.Error("lansman aracı hata verdi: lansman dosyası yazılmadı", "err", err)
	}
	return code
}
