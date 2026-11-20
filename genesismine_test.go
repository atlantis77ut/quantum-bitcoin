package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/gpu"
)

// Sahte planın anlattığı genesis: core/testdata/launch altındaki kolay hedefli örnek.
const (
	testBitcoinHash      = "00000000000000000001fbea30dad96a9449de2ac465309922f20cfe2b09eb86"
	testConfirmationHash = "00000000000000000001f07afd7e1e27a7d64535f3d62d4a98bf6156a7ad453d"
	testGenesisHash      = "074584d485fbb4d09d5c5c9dc97f30698d46e466baee649014be8556a38bce49"
	testBitcoinTime      = 1795102857
	testGenesisTime      = 1795103757
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

var errTestOpen = errors.New("sürücü açılamadı")

// fakePlan runLaunch'ın gördüğü planın sahtesidir; gerçek plan core paketinde sınanır
// (core/launchmine_test.go).
type fakePlan struct {
	info       core.LaunchInfo
	clockErr   error
	mined      *core.MinedGenesis
	mineErr    error
	verifyErr  error
	verifyHash string
	onMine     func()
	onVerify   func(raw []byte)
	clocks     []int64
	mines      int
	verified   [][]byte
}

func (f *fakePlan) Info() core.LaunchInfo { return f.info }

func (f *fakePlan) CheckClock(now int64) error {
	f.clocks = append(f.clocks, now)
	return f.clockErr
}

func (f *fakePlan) Mine(be core.MineBackend, clock func() int64, stop <-chan struct{}) (*core.MinedGenesis, error) {
	f.mines++
	if f.onMine != nil {
		f.onMine()
	}
	if f.mineErr != nil {
		return nil, f.mineErr
	}
	return f.mined, nil
}

func (f *fakePlan) Verify(raw []byte) (string, error) {
	f.verified = append(f.verified, raw)
	if f.onVerify != nil {
		f.onVerify(raw)
	}
	if f.verifyErr != nil {
		return "", f.verifyErr
	}
	if f.verifyHash != "" {
		return f.verifyHash, nil
	}
	return f.mined.Hash, nil
}

// readOr dosyanın içeriğidir; okunamazsa "<yok>".
func readOr(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "<yok>"
	}
	return string(raw)
}

// fakeGPU açılan aygıtın sahtesidir.
type fakeGPU struct{ closed int }

func (g *fakeGPU) Scan([core.HeaderSize]byte, [32]byte, uint64, uint64) (uint64, bool, error) {
	return 0, false, nil
}

func (g *fakeGPU) RoundSize() uint64 { return 1 << 24 }

func (g *fakeGPU) Name() string { return "sahte GPU" }

func (g *fakeGPU) Close() error {
	g.closed++
	return nil
}

// launchFixture geçerli bir araç çalıştırmasının girdilerini geçici bir dizinde kurar: temiz
// commit'ten derlenmiş ikili, geçerli ölçüm raporu, raporla aynı GPU, boş lansman dosyası. Testler
// tek bir şeyi bozar.
type launchFixture struct {
	cfg        launchConfig
	env        launchEnv
	dir        string
	plan       *fakePlan
	planErr    error
	backend    *fakeGPU
	openErr    error
	devices    []gpu.Device
	devicesErr error
	report     measurementReport
	commit     string
	modified   bool
	rate       float64
	measureErr error
	headers    []string
	opened     []uint64
	measured   []time.Duration
}

func newLaunchFixture(t *testing.T) *launchFixture {
	t.Helper()
	dir := t.TempDir()
	dev := gpu.Device{Index: 1, Name: "kart", Type: gpu.TypeGPU, Platform: "platform", Driver: "1.0"}
	bench, err := newBenchReport(core.TestnetParams, &dev, false, 0, 1<<24, time.Minute, []float64{3e9, 2.9e9, 3.1e9}, 1)
	if err != nil {
		t.Fatal(err)
	}
	bench.Commit, bench.Modified = testCommit, false
	start, floor, err := core.LaunchTargets(bench.Median, core.MainnetParams.TargetSpacing)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join("core", "testdata", "launch", "easy_mainnet.json"))
	if err != nil {
		t.Fatal(err)
	}
	file = bytes.ReplaceAll(file, []byte("\r\n"), []byte("\n"))
	f := &launchFixture{
		dir:     dir,
		backend: &fakeGPU{},
		devices: []gpu.Device{{Index: 0, Name: "pocl", Type: gpu.TypeCPU, Driver: "5.0"}, dev},
		report:  measurementReport{Schema: 1, GPUHashrate: true, CleanTree: true, GPU: &bench},
		commit:  testCommit,
		rate:    3e9,
	}
	f.plan = &fakePlan{
		info: core.LaunchInfo{Network: "mainnet", Bits: start, PowLimitBits: floor,
			Message: "Q-BTC mainnet genesis | BTC " + testBitcoinHash, BitcoinHash: testBitcoinHash, BitcoinTime: testBitcoinTime,
			BitcoinConfirmationHash: testConfirmationHash, BitcoinConfirmationTime: testBitcoinTime + 343},
		mined: &core.MinedGenesis{File: file, Hash: testGenesisHash, Timestamp: testGenesisTime, Nonce: 6, Tried: 7, Headers: 1,
			Started: testGenesisTime - 40, Found: testGenesisTime + 3},
	}
	f.cfg = launchConfig{Network: "mainnet", Block: "blok", Confirmation: "onay", Report: filepath.Join(dir, "olcum.json"),
		Out: filepath.Join(dir, "mainnet.json"), Record: filepath.Join(dir, "kayit.json"), Device: 1}
	f.env.now = func() time.Time { return time.Unix(testGenesisTime+5, 0) }
	f.env.vcs = func() (string, bool) { return f.commit, f.modified }
	f.env.newPlan = func(block, confirmation string) (launchPlan, error) {
		f.headers = []string{block, confirmation}
		if f.planErr != nil {
			return nil, f.planErr
		}
		return f.plan, nil
	}
	f.env.devices = func() ([]gpu.Device, error) { return f.devices, f.devicesErr }
	f.env.open = func(device int, batch uint64) (gpu.Backend, error) {
		if f.openErr != nil {
			return nil, f.openErr
		}
		f.opened = append(f.opened, batch)
		return f.backend, nil
	}
	f.env.measure = func(be core.MineBackend, d time.Duration) (float64, error) {
		f.measured = append(f.measured, d)
		return f.rate, f.measureErr
	}
	f.writeReport(t)
	if err := os.WriteFile(f.cfg.Out, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// writeReport ölçüm raporunu bugünkü hâliyle dosyaya yazar.
func (f *launchFixture) writeReport(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.report)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.cfg.Report, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// untouched aracın hiçbir dosya yazmadığını denetler: lansman dosyası boş kalmış, kayıt ve geçici
// dosya yaratılmamış, açılan aygıt kapatılmıştır.
func (f *launchFixture) untouched(t *testing.T, name string) {
	t.Helper()
	if raw, err := os.ReadFile(f.cfg.Out); err != nil || string(raw) != "{}\n" {
		t.Errorf("%s: lansman dosyası değişti: %q %v", name, raw, err)
	}
	for _, path := range []string{f.cfg.Record, f.cfg.Out + launchTempSuffix} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: %s geride kaldı (%v)", name, filepath.Base(path), err)
		}
	}
	if f.backend.closed != len(f.opened) {
		t.Errorf("%s: açılan aygıt kapatılmadı (açma %d, kapatma %d)", name, len(f.opened), f.backend.closed)
	}
}

// Araç bulduğu genesis'in kaydını ve lansman dosyasını yazar, dosyayı diskten geri okuyup doğrular.
func TestRunLaunchMine(t *testing.T) {
	f := newLaunchFixture(t)
	tmp := f.cfg.Out + launchTempSuffix
	var duringMine, duringVerify []string
	f.plan.onMine = func() {
		// Arama sürerken çıktılar ayrılmıştır: kayıt ve geçici dosya vardır ve boştur, lansman dosyası
		// boş kalmıştır.
		duringMine = []string{readOr(f.cfg.Out), readOr(f.cfg.Record), readOr(tmp)}
		// Aynı çıktılara yazacak ikinci bir çalıştırma genesis aramadan durur ve ilkinin ayırdıklarına
		// dokunmaz: önce kayıt, kayıt adı başkaysa geçici dosya onu durdurur.
		other := newLaunchFixture(t)
		other.cfg.Out, other.cfg.Record = f.cfg.Out, f.cfg.Record
		if rec, err := runLaunch(other.cfg, other.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), "zaten var") {
			t.Errorf("arama sürerken aynı kayıtla ikinci çalıştırma: %+v %v", rec, err)
		}
		other.cfg.Record = filepath.Join(f.dir, "ikinci-kayit.json")
		if rec, err := runLaunch(other.cfg, other.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), "duruyor") || other.plan.mines != 0 {
			t.Errorf("arama sürerken başka kayıtla ikinci çalıştırma: %+v %v", rec, err)
		}
		if got := readOr(other.cfg.Record); got != "<yok>" {
			t.Errorf("duran ikinci çalıştırma kaydını geride bıraktı: %q", got)
		}
	}
	f.plan.onVerify = func(raw []byte) {
		// Doğrulama geçici dosyanın diskten okunan içeriğiyle ve lansman dosyası değiştirilmeden önce
		// yapılır; kayıt o sırada yazılmıştır.
		duringVerify = []string{readOr(f.cfg.Out), readOr(tmp), string(raw)}
		if !strings.Contains(readOr(f.cfg.Record), testGenesisHash) {
			t.Errorf("kayıt lansman dosyasından önce yazılmadı: %q", readOr(f.cfg.Record))
		}
	}
	rec, err := runLaunch(f.cfg, f.env)
	if err != nil || rec == nil {
		t.Fatalf("kazım: %+v %v", rec, err)
	}
	info, mined := f.plan.info, f.plan.mined
	out, err := os.ReadFile(f.cfg.Out)
	if err != nil || !bytes.Equal(out, mined.File) {
		t.Fatalf("lansman dosyası bulunan genesis'in dosyası değil: %q %v", out, err)
	}
	if f.plan.mines != 1 || len(f.plan.verified) != 1 || !bytes.Equal(f.plan.verified[0], mined.File) {
		t.Fatalf("yazılan dosya diskten geri okunup doğrulanmadı: %d kazım, %d doğrulama", f.plan.mines, len(f.plan.verified))
	}
	if !reflect.DeepEqual(duringMine, []string{"{}\n", "", ""}) {
		t.Fatalf("arama sırasında çıktılar ayrılmamış (lansman dosyası, kayıt, geçici dosya): %q", duringMine)
	}
	if !reflect.DeepEqual(duringVerify, []string{"{}\n", string(mined.File), string(mined.File)}) {
		t.Fatalf("doğrulama lansman dosyası değiştirilmeden önce, diskteki geçici dosyayla yapılmadı: %q", duringVerify)
	}
	if !reflect.DeepEqual(f.headers, []string{"blok", "onay"}) || !reflect.DeepEqual(f.plan.clocks, []int64{testGenesisTime + 5}) ||
		!reflect.DeepEqual(f.opened, []uint64{1 << 24}) || f.backend.closed != 1 || len(f.measured) != 0 {
		t.Fatalf("akış: başlıklar %v, saat denetimleri %v, açılan tur boyları %v, kapatma %d, ölçüm %d", f.headers,
			f.plan.clocks, f.opened, f.backend.closed, len(f.measured))
	}
	if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("geçici lansman dosyası kaldı: %v", err)
	}

	reportRaw, err := os.ReadFile(f.cfg.Report)
	if err != nil {
		t.Fatal(err)
	}
	reportSum, fileSum := sha256.Sum256(reportRaw), sha256.Sum256(mined.File)
	want := launchRecord{
		Schema: 1, TimeUTC: "2026-11-19T15:56:02Z", Network: "mainnet", GenesisHash: testGenesisHash,
		Timestamp: testGenesisTime, TimestampUTC: "2026-11-19T15:55:57Z", Nonce: 6,
		Bits: fmt.Sprintf("%08x", info.Bits), PowLimitBits: fmt.Sprintf("%08x", info.PowLimitBits),
		Message: "Q-BTC mainnet genesis | BTC " + testBitcoinHash, BitcoinHash: testBitcoinHash, BitcoinTime: testBitcoinTime,
		BitcoinConfirmationHash: testConfirmationHash, BitcoinConfirmationTime: testBitcoinTime + 343,
		GenesisLagSeconds: 900, SearchStarted: testGenesisTime - 40, SearchSeconds: 43, NoncesTried: 7, HeadersTried: 1,
		Device: f.devices[1], RoundSize: 1 << 24, KernelSHA256: gpu.KernelSHA256(), Commit: testCommit,
		GoVersion: rec.GoVersion, OS: rec.OS, Arch: rec.Arch, Liboqs: rec.Liboqs,
		ReportSHA256: hex.EncodeToString(reportSum[:]), ReportMedian: 3e9, LaunchFileSHA256: hex.EncodeToString(fileSum[:]),
	}
	if *rec != want || rec.GoVersion == "" || rec.OS == "" || rec.Arch == "" || rec.Liboqs == "" {
		t.Fatalf("kayıt\n%+v\nbeklenen\n%+v", *rec, want)
	}
	data, err := os.ReadFile(f.cfg.Record)
	if err != nil {
		t.Fatal(err)
	}
	var back launchRecord
	if err := json.Unmarshal(data, &back); err != nil || back != *rec || !bytes.HasSuffix(data, []byte("}\n")) {
		t.Fatalf("kayıt dosyası dönen kayıt değil: %+v %v", back, err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil || len(keys) != 30 {
		t.Fatalf("kayıtta %d alan var: %v", len(keys), err)
	}
	for _, k := range []string{"genesis_hash", "timestamp", "nonce", "bitcoin_hash", "bitcoin_time", "genesis_lag_seconds",
		"search_seconds", "nonces_tried", "headers_tried", "device", "commit", "report_sha256", "launch_file_sha256"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("kayıtta %s yok", k)
		}
	}
	// Kayıt kamuya açıklanabilir: dosya yolu ya da makine adı içermez.
	for _, path := range []string{f.dir, "mainnet.json", "olcum.json", "kayit.json"} {
		if strings.Contains(string(data), path) {
			t.Fatalf("kayıt dosya yolu içeriyor: %s", path)
		}
	}
	if host, _ := os.Hostname(); len(host) >= 4 && strings.Contains(string(data), host) {
		t.Fatal("kayıt makine adını içeriyor")
	}

	// Lansman dosyası artık dolu: araç ikinci kez kazmaz; dosya ve kayıt değişmez.
	again, err := runLaunch(f.cfg, f.env)
	if again != nil || !errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "boş lansman dosyası") ||
		f.plan.mines != 1 || launchExitCode(again, err) != 2 {
		t.Fatalf("dolu lansman dosyasıyla ikinci çalıştırma: %+v %v", again, err)
	}
	if out2, err := os.ReadFile(f.cfg.Out); err != nil || !bytes.Equal(out2, mined.File) {
		t.Fatalf("ikinci çalıştırma lansman dosyasını değiştirdi: %v", err)
	}
	if data2, err := os.ReadFile(f.cfg.Record); err != nil || !bytes.Equal(data2, data) {
		t.Fatalf("ikinci çalıştırma kaydı değiştirdi: %v", err)
	}

	// -gpu-batch rapordaki tur boyuyla aynıysa kabul edilir.
	g := newLaunchFixture(t)
	g.cfg.Batch = 1 << 24
	if rec, err := runLaunch(g.cfg, g.env); err != nil || rec == nil || rec.RoundSize != 1<<24 {
		t.Fatalf("rapordaki tur boyuyla kazım: %+v %v", rec, err)
	}
}

// Ön-uçuş aynı denetimleri yapar, kısa bir hashrate ölçümünü rapordaki ortancayla karşılaştırır;
// genesis kazmaz ve dosya yazmaz.
func TestRunLaunchPreflight(t *testing.T) {
	cases := []struct {
		rate float64
		ok   bool
	}{{3e9, true}, {3.29e9, true}, {2.71e9, true}, {3.31e9, false}, {2.69e9, false}, {0, false}}
	for _, c := range cases {
		f := newLaunchFixture(t)
		f.cfg.Preflight, f.rate = true, c.rate
		rec, err := runLaunch(f.cfg, f.env)
		if rec != nil || (err == nil) != c.ok {
			t.Errorf("%v hash/sn: %+v %v", c.rate, rec, err)
		}
		if !c.ok && (!errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "sınırının dışında") ||
			launchExitCode(rec, err) != 2) {
			t.Errorf("%v hash/sn: %v", c.rate, err)
		}
		f.untouched(t, "ön-uçuş")
		if f.plan.mines != 0 || !reflect.DeepEqual(f.measured, []time.Duration{preflightWarmup, preflightMeasure}) {
			t.Errorf("%v hash/sn: %d kazım, ölçümler %v", c.rate, f.plan.mines, f.measured)
		}
	}

	f := newLaunchFixture(t)
	f.cfg.Preflight, f.measureErr = true, errors.New("ölçüm hatası")
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, f.measureErr) || launchExitCode(rec, err) != 1 {
		t.Errorf("ölçüm hatası: %+v %v", rec, err)
	}
	f.untouched(t, "ölçüm hatası")

	// Ön-uçuş kazım kipinin bütün denetimlerinden geçer: biri düşerse ön-uçuş da düşer.
	f = newLaunchFixture(t)
	f.cfg.Preflight, f.modified = true, true
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) || len(f.measured) != 0 {
		t.Errorf("kirli ağaçtan derlenmiş ikiliyle ön-uçuş: %+v %v", rec, err)
	}

	// Ön-uçuş çıktıların ayrılabildiğini de görür: kayıt zaten varsa ya da yarıda kesilmiş bir
	// çalıştırmadan geçici dosya kalmışsa düşer. Var olan dosyalara dokunmaz.
	f = newLaunchFixture(t)
	f.cfg.Preflight = true
	if err := os.WriteFile(f.cfg.Record, []byte("eski kayıt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "zaten var") || readOr(f.cfg.Record) != "eski kayıt\n" {
		t.Errorf("var olan kayıtla ön-uçuş: %+v %v", rec, err)
	}
	f = newLaunchFixture(t)
	f.cfg.Preflight = true
	if err := os.WriteFile(f.cfg.Out+launchTempSuffix, []byte("yarım"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "duruyor") || readOr(f.cfg.Out+launchTempSuffix) != "yarım" || readOr(f.cfg.Record) != "<yok>" {
		t.Errorf("kalmış geçici dosyayla ön-uçuş: %+v %v", rec, err)
	}
}

// Bir denetim düşerse ya da arama başarısız olursa genesis yazılmaz: lansman dosyası boş kalır,
// kayıt yaratılmaz.
func TestRunLaunchRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, f *launchFixture)
		is     error
		want   string
		mines  int
		code   int
	}{
		{"başka ağ", func(t *testing.T, f *launchFixture) { f.cfg.Network = "testnet" }, errLaunchRefused,
			"yalnızca mainnet", 0, 2},
		{"ağ verilmedi", func(t *testing.T, f *launchFixture) { f.cfg.Network = "" }, errLaunchRefused, "yalnızca mainnet", 0, 2},
		{"plan kurulamadı", func(t *testing.T, f *launchFixture) {
			f.planErr = fmt.Errorf("%w: hedefler seçilmedi", core.ErrLaunchCheck)
		}, core.ErrLaunchCheck, "hedefler seçilmedi", 0, 2},
		{"saat başlıklarla tutarsız", func(t *testing.T, f *launchFixture) {
			f.plan.clockErr = fmt.Errorf("%w: blok bayat", core.ErrLaunchCheck)
		}, core.ErrLaunchCheck, "blok bayat", 0, 2},
		{"ikilinin commit'i bilinmiyor", func(t *testing.T, f *launchFixture) { f.commit = "bilinmiyor" }, errLaunchRefused,
			"commit'i bilinmiyor", 0, 2},
		{"ikili kirli ağaçtan", func(t *testing.T, f *launchFixture) { f.modified = true }, errLaunchRefused,
			"commit edilmemiş", 0, 2},
		{"rapor verilmedi", func(t *testing.T, f *launchFixture) { f.cfg.Report = "" }, errLaunchRefused, "-launch-report", 0, 2},
		{"rapor dosyası yok", func(t *testing.T, f *launchFixture) { f.cfg.Report = filepath.Join(f.dir, "yok.json") },
			errLaunchRefused, "okunamadı", 0, 2},
		{"rapor JSON değil", func(t *testing.T, f *launchFixture) {
			if err := os.WriteFile(f.cfg.Report, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, errLaunchRefused, "çözülemedi", 0, 2},
		{"rapor başka çekirdekle ölçülmüş", func(t *testing.T, f *launchFixture) {
			f.report.GPU.KernelSHA256 = strings.Repeat("0", 64)
			f.writeReport(t)
		}, errLaunchRefused, "çekirdek özeti", 0, 2},
		{"mainnet hedefleri rapordan hesaplanan değil", func(t *testing.T, f *launchFixture) { f.plan.info.Bits = 0x1d00ffff },
			errLaunchRefused, "mainnet hedefleri", 0, 2},
		{"tur boyu rapordakinden farklı", func(t *testing.T, f *launchFixture) { f.cfg.Batch = 1 << 20 }, errLaunchRefused,
			"-gpu-batch", 0, 2},
		{"aygıtlar listelenemedi", func(t *testing.T, f *launchFixture) { f.devicesErr = gpu.ErrNoOpenCL }, errLaunchRefused,
			"listelenemedi", 0, 2},
		{"aygıt GPU değil", func(t *testing.T, f *launchFixture) { f.cfg.Device = 0 }, errLaunchRefused, "yalnızca GPU", 0, 2},
		{"aygıt yok", func(t *testing.T, f *launchFixture) { f.cfg.Device = 7 }, errLaunchRefused, "yalnızca GPU", 0, 2},
		{"aygıt modeli rapordaki değil", func(t *testing.T, f *launchFixture) { f.devices[1].Name = "başka kart" },
			errLaunchRefused, "ölçüm raporundaki aygıt", 0, 2},
		{"sürücü ölçümden sonra güncellenmiş", func(t *testing.T, f *launchFixture) { f.devices[1].Driver = "2.0" },
			errLaunchRefused, "ölçüm raporundaki aygıt", 0, 2},
		{"aygıt açılamadı", func(t *testing.T, f *launchFixture) { f.openErr = errTestOpen }, errTestOpen, "açılamadı", 0, 1},
		{"arama durduruldu", func(t *testing.T, f *launchFixture) { f.plan.mineErr = core.ErrMiningStopped },
			core.ErrMiningStopped, "", 1, 1},
		{"arama sırasında denetim düştü", func(t *testing.T, f *launchFixture) {
			f.plan.mineErr = fmt.Errorf("%w: yerel saat geri gitti", core.ErrLaunchCheck)
		}, core.ErrLaunchCheck, "geri gitti", 1, 2},
		{"kaydın yazılacağı dizin yok", func(t *testing.T, f *launchFixture) {
			f.cfg.Record = filepath.Join(f.dir, "yok", "kayit.json")
		}, errLaunchRefused, "lansman kaydı yaratılamadı", 0, 2},
	}
	for _, c := range cases {
		f := newLaunchFixture(t)
		c.mutate(t, f)
		rec, err := runLaunch(f.cfg, f.env)
		if rec != nil || !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: kayıt %+v, hata %v (beklenen parça %q)", c.name, rec, err, c.want)
		}
		if f.plan.mines != c.mines || launchExitCode(rec, err) != c.code {
			t.Errorf("%s: %d kazım (beklenen %d), çıkış kodu %d (beklenen %d)", c.name, f.plan.mines, c.mines,
				launchExitCode(rec, err), c.code)
		}
		f.untouched(t, c.name)
	}

	// Lansman dosyası boş değilse ya da yoksa araç kazmaz; var olan içerik değişmez.
	f := newLaunchFixture(t)
	filled := f.plan.mined.File
	if err := os.WriteFile(f.cfg.Out, filled, 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := runLaunch(f.cfg, f.env)
	if rec != nil || !errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "boş lansman dosyası") || f.plan.mines != 0 {
		t.Errorf("dolu lansman dosyası: %+v %v", rec, err)
	}
	if raw, err := os.ReadFile(f.cfg.Out); err != nil || !bytes.Equal(raw, filled) {
		t.Errorf("dolu lansman dosyası değişti: %v", err)
	}
	f = newLaunchFixture(t)
	if err := os.Remove(f.cfg.Out); err != nil {
		t.Fatal(err)
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "lansman dosyası okunamadı") || f.plan.mines != 0 {
		t.Errorf("lansman dosyası yok: %+v %v", rec, err)
	}
	if _, err := os.Lstat(f.cfg.Out); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("olmayan lansman dosyası yaratıldı: %v", err)
	}

	// Var olan kaydın üzerine yazılmaz: araç kazmadan durur.
	f = newLaunchFixture(t)
	if err := os.WriteFile(f.cfg.Record, []byte("eski kayıt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "zaten var") || f.plan.mines != 0 {
		t.Errorf("var olan kayıt: %+v %v", rec, err)
	}
	if raw, err := os.ReadFile(f.cfg.Record); err != nil || string(raw) != "eski kayıt\n" {
		t.Errorf("var olan kayıt değişti: %q %v", raw, err)
	}
	if raw, err := os.ReadFile(f.cfg.Out); err != nil || string(raw) != "{}\n" {
		t.Errorf("lansman dosyası değişti: %q %v", raw, err)
	}

	// Yarıda kesilmiş bir çalıştırmadan kalmış geçici dosya: araç kazmadan durur, onu ezmez ve
	// kendi kaydını geride bırakmaz.
	f = newLaunchFixture(t)
	leftover := f.cfg.Out + launchTempSuffix
	if err := os.WriteFile(leftover, []byte("yarım"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "duruyor") || f.plan.mines != 0 || launchExitCode(rec, err) != 2 {
		t.Errorf("kalmış geçici dosya: %+v %v", rec, err)
	}
	if got := []string{readOr(leftover), readOr(f.cfg.Out), readOr(f.cfg.Record)}; !reflect.DeepEqual(got,
		[]string{"yarım", "{}\n", "<yok>"}) {
		t.Errorf("kalmış geçici dosyayla çalıştırmadan sonra (geçici dosya, lansman dosyası, kayıt): %q", got)
	}

	// Ayrılan boş kayıt arama sırasında silinirse (ör. kalmış dosya sanılıp) bulunan genesis'in kaydı
	// yazılmış sayılmaz ve lansman dosyası da yazılmaz.
	f = newLaunchFixture(t)
	f.plan.onMine = func() {
		if err := os.Remove(f.cfg.Record); err != nil {
			t.Error(err)
		}
	}
	if rec, err := runLaunch(f.cfg, f.env); rec != nil || err == nil || !strings.Contains(err.Error(), "lansman kaydı yazılamadı") ||
		!strings.Contains(err.Error(), testGenesisHash) || f.plan.mines != 1 || launchExitCode(rec, err) != 1 {
		t.Errorf("arama sırasında silinen kayıt: %+v %v", rec, err)
	}
	f.untouched(t, "arama sırasında silinen kayıt")
}

// Denetimler belgelenen sırayla çalışır: hepsi birden bozukken ilk düşen ağdır, her adım
// düzeltilince sıradaki düşer. Sıra değişirse (ör. aygıt çıktılardan önce açılırsa) bu test
// kırmızıya döner. Hiçbir adımda genesis aranmaz ve geride dosya kalmaz.
func TestRunLaunchCheckOrder(t *testing.T) {
	f := newLaunchFixture(t)
	report := f.cfg.Report
	f.cfg.Network, f.cfg.Report, f.cfg.Batch, f.cfg.Device = "testnet", "", 1<<20, 0
	f.planErr = fmt.Errorf("%w: plan", core.ErrLaunchCheck)
	f.plan.clockErr = fmt.Errorf("%w: saat", core.ErrLaunchCheck)
	f.modified, f.devicesErr, f.openErr = true, gpu.ErrNoOpenCL, errTestOpen
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(f.cfg.Out, "dolu\n")
	write(f.cfg.Record, "eski kayıt\n")
	steps := []struct {
		want string
		fix  func()
	}{
		{"yalnızca mainnet", func() { f.cfg.Network = "mainnet" }},
		{": plan", func() { f.planErr = nil }},
		{": saat", func() { f.plan.clockErr = nil }},
		{"commit edilmemiş", func() { f.modified = false }},
		{"-launch-report", func() { f.cfg.Report = report }},
		{"-gpu-batch", func() { f.cfg.Batch = 0 }},
		{"boş lansman dosyası", func() { write(f.cfg.Out, "{}\n") }},
		{"zaten var", func() {
			if err := os.Remove(f.cfg.Record); err != nil {
				t.Fatal(err)
			}
		}},
		{"listelenemedi", func() { f.devicesErr = nil }},
		{"yalnızca GPU", func() { f.cfg.Device = 1 }},
		{"açılamadı", func() { f.openErr = nil }},
	}
	for i, s := range steps {
		rec, err := runLaunch(f.cfg, f.env)
		if rec != nil || err == nil || !strings.Contains(err.Error(), s.want) || f.plan.mines != 0 {
			t.Fatalf("adım %d: beklenen ilk hata %q, gelen %v (%d kazım)", i, s.want, err, f.plan.mines)
		}
		if got := readOr(f.cfg.Out + launchTempSuffix); got != "<yok>" {
			t.Fatalf("adım %d: geçici dosya geride kaldı: %q", i, got)
		}
		s.fix()
	}
	f.untouched(t, "son adımdan sonra")
	if rec, err := runLaunch(f.cfg, f.env); err != nil || rec == nil || f.plan.mines != 1 {
		t.Fatalf("bütün denetimler düzeltildikten sonra kazım: %+v %v", rec, err)
	}
}

// Genesis bulunup kaydı yazıldıktan sonra lansman dosyası doğrulanamazsa kayıt döner ve saklanır:
// deneme atılmış sayılır, çıkış kodu 1'dir.
func TestRunLaunchVerifyFailure(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(f *launchFixture)
		want   string
	}{
		{"yükleyici dosyayı reddediyor", func(f *launchFixture) {
			f.plan.verifyErr = fmt.Errorf("%w: özet tutmuyor", core.ErrBadLaunchFile)
		}, "özet tutmuyor"},
		{"dosyadaki özet bulunan genesis'in özeti değil", func(f *launchFixture) {
			f.plan.verifyHash = strings.Repeat("0", 64)
		}, "bulunan " + testGenesisHash},
	}
	for _, c := range cases {
		f := newLaunchFixture(t)
		c.mutate(f)
		rec, err := runLaunch(f.cfg, f.env)
		if rec == nil || err == nil || !strings.Contains(err.Error(), "doğrulanamadı") || !strings.Contains(err.Error(), c.want) ||
			launchExitCode(rec, err) != 1 {
			t.Fatalf("%s: %+v %v", c.name, rec, err)
		}
		if rec.GenesisHash != testGenesisHash || f.plan.mines != 1 || f.backend.closed != 1 {
			t.Fatalf("%s: kayıt %+v, %d kazım", c.name, rec, f.plan.mines)
		}
		// Doğrulanmamış içerik lansman dosyasının adına hiç yazılmaz; geçici dosya silinir.
		if got := []string{readOr(f.cfg.Out), readOr(f.cfg.Out + launchTempSuffix)}; !reflect.DeepEqual(got,
			[]string{"{}\n", "<yok>"}) {
			t.Fatalf("%s: lansman dosyası ve geçici dosya: %q", c.name, got)
		}
		var back launchRecord
		data, err := os.ReadFile(f.cfg.Record)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &back); err != nil || back != *rec {
			t.Fatalf("%s: atılan denemenin kaydı saklanmadı: %+v %v", c.name, back, err)
		}
	}
}

func TestLaunchExitCode(t *testing.T) {
	rec := &launchRecord{}
	cases := []struct {
		rec  *launchRecord
		err  error
		want int
	}{
		{rec, nil, 0},
		{nil, nil, 0},
		{nil, refuse("denetim"), 2},
		{nil, fmt.Errorf("sarılmış: %w", core.ErrLaunchCheck), 2},
		{nil, core.ErrBadLaunchFile, 2},
		{nil, errTestOpen, 1},
		{nil, core.ErrMiningStopped, 1},
		{rec, refuse("genesis bulunduktan sonra"), 1},
		{rec, core.ErrBadLaunchFile, 1},
	}
	for i, c := range cases {
		if got := launchExitCode(c.rec, c.err); got != c.want {
			t.Errorf("%d: çıkış kodu %d, beklenen %d (%v)", i, got, c.want, c.err)
		}
	}
}

// Ölçüm raporuyla öz-tutarlılık: her şartın bozulmuş hâli reddedilir ve hata o şartın denetiminden
// gelir.
func TestCheckLaunchReport(t *testing.T) {
	f := newLaunchFixture(t)
	info, kernel := f.plan.info, gpu.KernelSHA256()
	if err := checkLaunchReport(&f.report, info.Bits, info.PowLimitBits, kernel); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(r *measurementReport)
		want   string
	}{
		{"birleşik rapor değil", func(r *measurementReport) { r.Schema = 0 }, "tanınmıyor"},
		{"GPU bölümü yok", func(r *measurementReport) { r.GPU = nil }, "tanınmıyor"},
		{"GPU bölümünün şeması başka", func(r *measurementReport) { r.GPU.Schema = 2 }, "tanınmıyor"},
		{"CPU ölçümü", func(r *measurementReport) { r.GPU.Backend = "cpu" }, "GPU ölçümü yok"},
		{"aygıt bilgisi yok", func(r *measurementReport) { r.GPU.Device = nil }, "GPU ölçümü yok"},
		{"gerçek GPU ölçümü işareti yok", func(r *measurementReport) { r.GPUHashrate = false }, "gerçek bir GPU ölçümü değil"},
		{"GPU olmayan aygıta izin verilmiş", func(r *measurementReport) { r.GPU.NonGPUAllowed = true },
			"gerçek bir GPU ölçümü değil"},
		{"aygıt türü CPU", func(r *measurementReport) { r.GPU.Device.Type = gpu.TypeCPU }, "gerçek bir GPU ölçümü değil"},
		{"birleşik rapor kirli ağaçtan", func(r *measurementReport) { r.CleanTree = false }, "temiz bir commit"},
		{"GPU ölçümü kirli ağaçtan", func(r *measurementReport) { r.GPU.Modified = true }, "temiz bir commit"},
		{"commit bilinmiyor", func(r *measurementReport) { r.GPU.Commit = "bilinmiyor" }, "temiz bir commit"},
		{"başka çekirdek", func(r *measurementReport) { r.GPU.KernelSHA256 = strings.Repeat("0", 64) }, "çekirdek özeti"},
		{"çekirdek özeti yok", func(r *measurementReport) { r.GPU.KernelSHA256 = "" }, "çekirdek özeti"},
		{"tur boyu yok", func(r *measurementReport) { r.GPU.RoundSize = 0 }, "tur boyu"},
		{"tur boyu sınırın üstünde", func(r *measurementReport) { r.GPU.RoundSize = gpu.MaxBatch + 1 }, "tur boyu"},
		{"tur ortanca hashrate'te bir saniyeden uzun sürer", func(r *measurementReport) {
			r.GPU.RoundSize, r.GPU.Median = gpu.MaxBatch, 5e8
		}, "sn sürer"},
		{"ortanca sıfır", func(r *measurementReport) { r.GPU.Median = 0 }, "ortanca hashrate geçersiz"},
		{"ortanca negatif", func(r *measurementReport) { r.GPU.Median = -3e9 }, "ortanca hashrate geçersiz"},
		{"ortanca NaN", func(r *measurementReport) { r.GPU.Median = math.NaN() }, "ortanca hashrate geçersiz"},
		{"ortanca sonsuz", func(r *measurementReport) { r.GPU.Median = math.Inf(1) }, "ortanca hashrate geçersiz"},
		{"raporun önerdiği başlangıç hedefi ortancasıyla tutmuyor", func(r *measurementReport) { r.GPU.StartBits = "0x1d00ffff" },
			"önerdiği hedefler"},
		{"raporun önerdiği taban ortancasıyla tutmuyor", func(r *measurementReport) { r.GPU.FloorBits = "0x1c00ffff" },
			"önerdiği hedefler"},
		{"ortanca değişmiş, öneri eski", func(r *measurementReport) { r.GPU.Median = 6e9 }, "önerdiği hedefler"},
	}
	for _, c := range cases {
		// Her örnek raporun derin kopyasını bozar.
		rep, bench, dev := f.report, *f.report.GPU, *f.report.GPU.Device
		bench.Device, rep.GPU = &dev, &bench
		c.mutate(&rep)
		if err := checkLaunchReport(&rep, info.Bits, info.PowLimitBits, kernel); !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (beklenen parça %q)", c.name, err, c.want)
		}
	}
	if err := checkLaunchReport(nil, info.Bits, info.PowLimitBits, kernel); !errors.Is(err, errLaunchRefused) {
		t.Errorf("rapor yok: %v", err)
	}
	// Rapor kendi içinde tutarlı olsa da mainnet hedefleri ondan hesaplananlar değilse reddedilir:
	// bugünkü yer tutucu hedefler, bir bayt zor başlangıç hedefi, bir bayt kolay taban.
	for _, pair := range [][2]uint32{{0x1d00ffff, 0x1d00ffff}, {info.Bits - 1<<24, info.PowLimitBits},
		{info.Bits, info.PowLimitBits + 1<<24}} {
		if err := checkLaunchReport(&f.report, pair[0], pair[1], kernel); !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), "mainnet hedefleri") {
			t.Errorf("%08x/%08x: %v", pair[0], pair[1], err)
		}
	}
	// Başka bir ikilinin çekirdeği.
	if err := checkLaunchReport(&f.report, info.Bits, info.PowLimitBits, strings.Repeat("f", 64)); !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "çekirdek özeti") {
		t.Errorf("başka ikili: %v", err)
	}
}

// Araç birleşik ölçüm raporunu (scripts/olcum_birlestir.py biçimi) okur; kullanmadığı alanlar onu
// bozmaz ve dosyanın özeti kayda girecek değerdir.
func TestLoadLaunchReport(t *testing.T) {
	f := newLaunchFixture(t)
	bench, err := json.Marshal(f.report.GPU)
	if err != nil {
		t.Fatal(err)
	}
	text := `{"schema": 1, "not": "Q-BTC hashrate ölçümü", "gpu_hashrate_mi": true, "temiz_agac": true, "kernel": "Linux 6.8",` +
		` "liboqs": "0.15.0", "gpu": ` + string(bench) + `, "cpu": {"schema": 1, "backend": "cpu"},` +
		` "kernel_ab": {"ratio": 1.2}, "kernel_ab_karar": "açılmış çekirdek kalır"}` + "\n"
	path := filepath.Join(f.dir, "birlesik.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, sum, err := loadLaunchReport(path)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(text))
	if sum != hex.EncodeToString(want[:]) || rep.Schema != 1 || !rep.GPUHashrate || !rep.CleanTree || rep.GPU == nil ||
		rep.GPU.Median != 3e9 || rep.GPU.Device == nil || rep.GPU.Device.Name != "kart" || rep.GPU.RoundSize != 1<<24 {
		t.Fatalf("rapor %+v, özet %s", rep, sum)
	}
	if err := checkLaunchReport(rep, f.plan.info.Bits, f.plan.info.PowLimitBits, gpu.KernelSHA256()); err != nil {
		t.Fatalf("okunan rapor öz-tutarlılık denetiminden geçmedi: %v", err)
	}
	// Tek başına GPU ölçümü (-bench-out çıktısı) birleşik rapor değildir.
	if err := os.WriteFile(path, bench, 0o644); err != nil {
		t.Fatal(err)
	}
	rep, _, err = loadLaunchReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkLaunchReport(rep, f.plan.info.Bits, f.plan.info.PowLimitBits, gpu.KernelSHA256()); !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "tanınmıyor") {
		t.Fatalf("tek başına GPU ölçümü birleşik rapor sayıldı: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte(" "), maxReportSize+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadLaunchReport(path); !errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "büyük") {
		t.Fatalf("sınırı aşan rapor: %v", err)
	}
}

// Genesis yalnızca türü GPU olan ve ölçüm raporundaki model ve sürücüyle aynı aygıtta kazılır.
func TestLaunchDevice(t *testing.T) {
	measured := gpu.Device{Index: 0, Name: "kart", Type: gpu.TypeGPU, Driver: "1.0"}
	devs := []gpu.Device{{Index: 0, Name: "kart", Type: gpu.TypeCPU, Driver: "1.0"},
		{Index: 1, Name: "kart", Type: gpu.TypeGPU, Driver: "1.0", Platform: "p"},
		{Index: 2, Name: "kart", Type: gpu.TypeGPU, Driver: "1.1"}, {Index: 3, Name: "kart 2", Type: gpu.TypeGPU, Driver: "1.0"},
		{Index: 4, Name: "kart", Type: gpu.TypeAccelerator, Driver: "1.0"}}
	// Aygıt numarası ölçümdekinden farklı olabilir; model ve sürücü aynı olmalıdır.
	if d, err := launchDevice(devs, 1, &measured); err != nil || d != devs[1] {
		t.Fatalf("raporla aynı GPU: %+v %v", d, err)
	}
	cases := []struct {
		idx  int
		want string
	}{
		{0, "yalnızca GPU"}, {4, "yalnızca GPU"}, {-1, "yalnızca GPU"}, {5, "yalnızca GPU"},
		{2, "ölçüm raporundaki aygıt"}, {3, "ölçüm raporundaki aygıt"},
	}
	for _, c := range cases {
		if d, err := launchDevice(devs, c.idx, &measured); d != (gpu.Device{}) || !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("aygıt %d: %+v %v (beklenen parça %q)", c.idx, d, err, c.want)
		}
	}
	if _, err := launchDevice(devs, 1, nil); !errors.Is(err, errLaunchRefused) {
		t.Errorf("ölçülen aygıt bilgisi olmadan: %v", err)
	}
}

func TestCheckHashrate(t *testing.T) {
	for _, r := range []float64{3e9, 2.7e9 + 1, 3.3e9 - 1} {
		if err := checkHashrate(r, 3e9); err != nil {
			t.Errorf("%v: %v", r, err)
		}
	}
	for _, r := range []float64{2.69e9, 3.31e9, 0, -3e9, math.NaN(), math.Inf(1)} {
		if err := checkHashrate(r, 3e9); !errors.Is(err, errLaunchRefused) {
			t.Errorf("%v kabul edildi: %v", r, err)
		}
	}
	for _, m := range []float64{0, -1, math.NaN()} {
		if err := checkHashrate(3e9, m); !errors.Is(err, errLaunchRefused) {
			t.Errorf("ortanca %v kabul edildi: %v", m, err)
		}
	}
}

func TestCheckLaunchBuild(t *testing.T) {
	if err := checkLaunchBuild(testCommit, false); err != nil {
		t.Fatal(err)
	}
	if err := checkLaunchBuild(strings.Repeat("a", 64), false); err != nil {
		t.Fatalf("SHA-256 commit kimliği: %v", err)
	}
	if err := checkLaunchBuild(testCommit, true); !errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "commit edilmemiş") {
		t.Fatalf("kirli ağaç: %v", err)
	}
	for _, c := range []string{"", "bilinmiyor", testCommit[:39], testCommit + "0", strings.ToUpper(testCommit),
		strings.Repeat("g", 40)} {
		if err := checkLaunchBuild(c, false); !errors.Is(err, errLaunchRefused) || !strings.Contains(err.Error(), "bilinmiyor") {
			t.Errorf("%q: %v", c, err)
		}
	}
}

// Çıktılar arama başlamadan önce ayrılır ve var olan hiçbir dosyanın üzerine yazılmaz. Lansman dosyası
// ancak geçici kopyası diskten doğrulandıktan sonra ve hâlâ boşsa değiştirilir; yazılmamış çıktılar
// geri alınır.
func TestLaunchFiles(t *testing.T) {
	dir := t.TempDir()
	out, record := filepath.Join(dir, "mainnet.json"), filepath.Join(dir, "kayit.json")
	tmp := out + launchTempSuffix
	content := []byte("{\n  \"network\": \"mainnet\"\n}\n")
	state := func() []string { return []string{readOr(out), readOr(tmp), readOr(record)} }
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	accept := func(back []byte) error { return nil }

	// Lansman dosyası yoksa, doluysa ya da tanınmıyorsa hiçbir şey ayrılmaz.
	if o, err := reserveLaunchOutputs(out, record); o != nil || !errors.Is(err, errLaunchRefused) {
		t.Fatalf("olmayan lansman dosyası: %v", err)
	}
	for _, other := range []string{string(content), "{}", "", "{ }\n", "null\n"} {
		write(out, other)
		if o, err := reserveLaunchOutputs(out, record); o != nil || !errors.Is(err, errLaunchRefused) ||
			!strings.Contains(err.Error(), "boş lansman dosyası") {
			t.Errorf("%q ile çıktılar ayrıldı: %v", other, err)
		}
		if got := state(); !reflect.DeepEqual(got, []string{other, "<yok>", "<yok>"}) {
			t.Errorf("%q ile reddedilen ayırma iz bıraktı: %q", other, got)
		}
	}

	// Var olan kayıt, kalmış geçici dosya ve olmayan kayıt dizini: ayırma düşer, var olan içerik
	// değişmez, araç kendi yarattığını geri alır.
	write(out, "{}\n")
	write(record, "eski kayıt\n")
	if o, err := reserveLaunchOutputs(out, record); o != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "zaten var") {
		t.Fatalf("var olan kayıt: %v", err)
	}
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "<yok>", "eski kayıt\n"}) {
		t.Fatalf("var olan kayıtla reddedilen ayırma: %q", got)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	write(tmp, "yarım")
	if o, err := reserveLaunchOutputs(out, record); o != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "duruyor") {
		t.Fatalf("kalmış geçici dosya: %v", err)
	}
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "yarım", "<yok>"}) {
		t.Fatalf("kalmış geçici dosyayla reddedilen ayırma: %q", got)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if o, err := reserveLaunchOutputs(out, filepath.Join(dir, "yok", "kayit.json")); o != nil || !errors.Is(err, errLaunchRefused) ||
		!strings.Contains(err.Error(), "lansman kaydı yaratılamadı") {
		t.Fatalf("olmayan kayıt dizini: %v", err)
	}
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "<yok>", "<yok>"}) {
		t.Fatalf("olmayan kayıt diziniyle reddedilen ayırma: %q", got)
	}

	// Ayırma: kayıt ve geçici dosya boş olarak yaratılır; ikinci bir ayırma durur. Geri alma ikisini
	// de siler, yinelenebilir ve lansman dosyasına dokunmaz. Satır sonu CRLF olan boş dosya da boştur.
	for _, empty := range []string{"{}\n", "{}\r\n"} {
		write(out, empty)
		o, err := reserveLaunchOutputs(out, record)
		if err != nil {
			t.Fatalf("%q ile çıktılar ayrılamadı: %v", empty, err)
		}
		if got := state(); !reflect.DeepEqual(got, []string{empty, "", ""}) {
			t.Fatalf("ayrılan çıktılar: %q", got)
		}
		if second, err := reserveLaunchOutputs(out, record); second != nil || !errors.Is(err, errLaunchRefused) {
			t.Fatalf("ayrılmış çıktılar ikinci kez ayrıldı: %v", err)
		}
		o.release()
		o.release()
		if got := state(); !reflect.DeepEqual(got, []string{empty, "<yok>", "<yok>"}) {
			t.Fatalf("geri alınan çıktılar: %q", got)
		}
	}

	// Yazım: önce kayıt, sonra doğrulanan lansman dosyası. Sonraki geri alma yazılanlara dokunmaz.
	write(out, "{}\n")
	o, err := reserveLaunchOutputs(out, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.writeRecord([]byte("kayıt\n")); err != nil {
		t.Fatal(err)
	}
	if err := o.writeRecord([]byte("ikinci kez\n")); err == nil {
		t.Fatal("kayıt iki kez yazıldı")
	}
	var verified []string
	if err := o.commitLaunchFile(content, func(back []byte) error {
		verified = append(verified, string(back), readOr(out), readOr(tmp))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(verified, []string{string(content), "{}\n", string(content)}) {
		t.Fatalf("doğrulama diskteki geçici dosyayla ve lansman dosyası değişmeden önce yapılmadı: %q", verified)
	}
	if err := o.commitLaunchFile(content, accept); err == nil {
		t.Fatal("lansman dosyası iki kez yazıldı")
	}
	o.release()
	if got := state(); !reflect.DeepEqual(got, []string{string(content), "<yok>", "kayıt\n"}) {
		t.Fatalf("yazılan çıktılar: %q", got)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}

	// Doğrulama düşerse lansman dosyası boş kalır; geçici dosya geri almayla silinir, yazılmış kayıt
	// kalır.
	write(out, "{}\n")
	if o, err = reserveLaunchOutputs(out, record); err != nil {
		t.Fatal(err)
	}
	if err := o.writeRecord([]byte("kayıt\n")); err != nil {
		t.Fatal(err)
	}
	if err := o.commitLaunchFile(content, func([]byte) error { return errTestOpen }); !errors.Is(err, errTestOpen) {
		t.Fatalf("doğrulanamayan içerik: %v", err)
	}
	o.release()
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "<yok>", "kayıt\n"}) {
		t.Fatalf("doğrulanamayan içerikten sonra: %q", got)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}

	// Lansman dosyası arama sırasında doldurulmuşsa üzerine yazılmaz.
	if o, err = reserveLaunchOutputs(out, record); err != nil {
		t.Fatal(err)
	}
	write(out, "araya giren\n")
	if err := o.commitLaunchFile(content, accept); !errors.Is(err, errLaunchRefused) {
		t.Fatalf("arama sırasında doldurulan lansman dosyası: %v", err)
	}
	o.release()
	if got := state(); !reflect.DeepEqual(got, []string{"araya giren\n", "<yok>", "<yok>"}) {
		t.Fatalf("arama sırasında doldurulan lansman dosyasından sonra: %q", got)
	}

	// Ayrılan boş kayıt arama sırasında silinirse yazım açık tanıtıcıya gider ama yol boştur: araç
	// bunu geri okuyarak görür ve kaydı yazılmış saymaz.
	write(out, "{}\n")
	if o, err = reserveLaunchOutputs(out, record); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := o.writeRecord([]byte("kayıt\n")); err == nil || !strings.Contains(err.Error(), "silinmiş") {
		t.Fatalf("silinen kayıt dosyasına yazım: %v", err)
	}
	o.release()
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "<yok>", "<yok>"}) {
		t.Fatalf("silinen kayıt dosyasından sonra: %q", got)
	}

	// Ayrılan dosyalar silinip yerlerine başkasının dosyaları konmuşsa geri alma onlara dokunmaz.
	if o, err = reserveLaunchOutputs(out, record); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{record, tmp} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		write(path, "başkasının\n")
	}
	o.release()
	if got := state(); !reflect.DeepEqual(got, []string{"{}\n", "başkasının\n", "başkasının\n"}) {
		t.Fatalf("başkasının dosyaları geri almayla silindi: %q", got)
	}

	if got := defaultRecordName(time.Unix(testGenesisTime+5, 0)); got != "qbtc-genesis-20261119T155602Z.json" {
		t.Fatalf("varsayılan kayıt adı %q", got)
	}
}
