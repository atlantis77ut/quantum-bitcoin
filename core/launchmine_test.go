package core

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// stepClock her okunuşta step saniye ilerleyen sahte saattir (step 0: saat durur).
type stepClock struct{ now, step int64 }

func (c *stepClock) read() int64 {
	t := c.now
	c.now += c.step
	return t
}

// zeroRoundBackend tur boyu sıfır olan arka uçtur.
type zeroRoundBackend struct{ *fakeBackend }

func (zeroRoundBackend) RoundSize() uint64 { return 0 }

// neverFound hiçbir turda nonce bulmayan arka uç taramasıdır.
func neverFound([HeaderSize]byte, [32]byte, uint64, uint64) (uint64, bool, error) {
	return 0, false, nil
}

// easyPlan kolay hedefli "mainnet" kopyası için, 976600 ve 976601. bloklara bağlanan plandır.
func easyPlan(t *testing.T) *LaunchPlan {
	t.Helper()
	pl, err := newLaunchPlan(easyMainnet(), easyLaunchRules, btc976600, btc976601)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// Araç, kolay hedefli örnekte depodaki lansman dosyasını bayt bayt üretir: aynı Bitcoin başlıkları,
// aynı T_g ve aramanın bulduğu ilk nonce. O dosyanın vektörünü bağımsız betik üretmiştir
// (TestLaunchEasyTarget); böylece araç, dosya, yükleyici ve betik uçtan uca bağlanır. Gerçek
// kurallardan yalnızca tavan gevşetilmiştir.
func TestLaunchMineEasyTarget(t *testing.T) {
	p := easyMainnet()
	pl, err := newLaunchPlan(p, easyLaunchRules, btc976600, btc976601)
	if err != nil {
		t.Fatal(err)
	}
	if info := pl.Info(); info != (LaunchInfo{Network: "mainnet", Bits: 0x200fffff, PowLimitBits: 0x200fffff,
		Message: mainnetGenesisMessagePrefix + btc976600Hash, BitcoinHash: btc976600Hash, BitcoinTime: btc976600Time,
		BitcoinConfirmationHash: btc976601Hash, BitcoinConfirmationTime: btc976601Time}) {
		t.Fatalf("plan özeti %+v", info)
	}
	var stamps []int64
	pl.Progress = func(timestamp int64, tried uint64) { stamps = append(stamps, timestamp) }
	clock := &stepClock{now: btc976600Time + 900}
	fb := &fakeBackend{scan: cpuScan}
	got, err := pl.Mine(fb, clock.read, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fixture := readTestdata(t, "launch", "easy_mainnet.json"); !bytes.Equal(got.File, fixture) {
		t.Fatalf("aracın kurduğu dosya depodaki örnekle aynı değil:\n%s\nörnek:\n%s", got.File, fixture)
	}
	_, want := easyLaunch(t)
	if got.Hash != want.Hash || got.Nonce != want.Nonce || got.Timestamp != want.Timestamp || got.Tried != want.Nonce+1 ||
		got.Headers != 1 || got.Started != want.Timestamp || got.Found != want.Timestamp || fb.calls != 1 {
		t.Fatalf("bulunan genesis %+v, beklenen %+v (arka uç çağrısı %d)", got, want, fb.calls)
	}
	if len(stamps) != 1 || stamps[0] != want.Timestamp {
		t.Fatalf("ilerleme çağrıları %v", stamps)
	}
	if hash, err := pl.Verify(got.File); err != nil || hash != want.Hash {
		t.Fatalf("kurulan dosya yeniden doğrulanamadı: %q %v", hash, err)
	}
	// Kazım ne verilen parametreleri ne planın kopyasını değiştirir: genesis yalnızca dosyayla gelir.
	if hasGenesis(p) || hasGenesis(pl.params) {
		t.Fatalf("kazım parametreleri değiştirdi: %+v", pl.params)
	}
	if q := mustLaunch(easyMainnet(), got.File, easyLaunchRules); q.GenesisHash != want.Hash {
		t.Fatalf("yükleyici aracın dosyasını kabul etmedi: %+v", q)
	}
}

// Arama sürerken T_g yerel saate tazelenir: yeni başlıkta arama nonce 0'dan başlar ve bulunan
// genesis, tazelenen T_g'nin başlığındaki ilk geçerli nonce'tur. Eski başlıkta denenen nonce'lar
// yeni T_g ile yazılmaz.
func TestLaunchMineRefreshesTimestamp(t *testing.T) {
	pl := easyPlan(t)
	start := int64(btc976600Time + 900)
	clock := &stepClock{now: start, step: 4}
	var starts []uint64
	fb := &fakeBackend{}
	fb.scan = func(hdr [HeaderSize]byte, tgt [32]byte, from, count uint64) (uint64, bool, error) {
		starts = append(starts, from)
		if fb.calls <= 3 {
			return 0, false, nil // ilk başlıkta hiçbir şey bulunmaz
		}
		return cpuScan(hdr, tgt, from, count)
	}
	var stamps []int64
	pl.Progress = func(timestamp int64, tried uint64) { stamps = append(stamps, timestamp) }
	got, err := pl.Mine(fb, clock.read, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Saat okuma başına 4 sn ilerler: ilk üç tur ilk başlıktadır (T_g = başlangıç), dördüncü okumada
	// 12 sn geçmiştir ve başlık tazelenir.
	if len(stamps) < 2 || stamps[0] != start || stamps[1] != start+12 || got.Timestamp != stamps[len(stamps)-1] ||
		got.Headers != len(stamps) || got.Started != start || got.Found < got.Timestamp {
		t.Fatalf("T_g tazelenmedi: damgalar %v, sonuç %+v", stamps, got)
	}
	if len(starts) < 4 || starts[0] != 0 || starts[1] != 64 || starts[2] != 128 || starts[3] != 0 {
		t.Fatalf("yeni başlıkta arama nonce 0'dan başlamadı: %v", starts)
	}
	valid := minedLaunch(t, easyMainnet(), got.Timestamp, mainnetGenesisMessagePrefix+btc976600Hash, btc976600, btc976601)
	want, err := valid.encode()
	if err != nil {
		t.Fatal(err)
	}
	if got.Nonce != valid.Nonce || got.Hash != valid.Hash || !bytes.Equal(got.File, want) || got.Tried < 3*64+got.Nonce+1 {
		t.Fatalf("bulunan genesis tazelenen başlığın ilk geçerli nonce'u değil: %+v, beklenen %+v", got, valid)
	}
	if hash, err := pl.Verify(got.File); err != nil || hash != got.Hash {
		t.Fatalf("kurulan dosya yeniden doğrulanamadı: %q %v", hash, err)
	}
}

// Yerel saat Bitcoin başlıklarının zamanlarıyla tutarlı olmalıdır, çünkü T_g yerel saattir.
func TestLaunchClock(t *testing.T) {
	pl := easyPlan(t)
	const tb, tc = btc976600Time, btc976601Time
	if tc-tb != 343 {
		t.Fatalf("başlık zamanları beklenen gibi değil: fark %d", tc-tb)
	}
	// Kabul edilen aralığın iki ucu onaylayan bloğa göredir: blok saatten en çok MaxFutureDrift
	// ileride, en çok launchMaxTipAge geride.
	if launchMaxTipAge != 30*60 || tc+launchMaxTipAge > tb+launchMaxGenesisLag {
		t.Fatalf("örnek başlıklarla onaylayan blok sınırı veri sınırından önce gelmiyor: %d", launchMaxTipAge)
	}
	for _, now := range []int64{tc - MaxFutureDrift, tb + 900, tc + launchMaxTipAge} {
		if err := pl.CheckClock(now); err != nil {
			t.Errorf("yerel saat t_b + %d reddedildi: %v", now-tb, err)
		}
	}
	rejected := []struct {
		name string
		now  int64
		want string
	}{
		{"saat mesajdaki bloğun zamanından geride", tb - 1, "t_b = "},
		{"saat üç saat geride (saat dilimi)", tb - 3*60*60, "t_b = "},
		{"t_b anında onaylayan bloğun damgası henüz ileride", tb, "sn ileride"},
		{"onaylayan bloğun damgası FTL'den bir saniye fazla ileride", tc - MaxFutureDrift - 1, "sn ileride"},
		{"onaylayan blok sınırdan bir saniye eski", tc + launchMaxTipAge + 1, "sn geride"},
		// Veri kuralının (T_g − t_b ≤ 1 saat) kaçırdığı kaymayı aracın kendi sınırı yakalar.
		{"saat yarım saat ileride, uç blok 10 dakikalık", tc + 40*60, "sn geride"},
		{"başlıklar eski (ön-uçuştan kalmış) ya da blok bayat", tb + launchMaxGenesisLag + 1, "bayat"},
		{"saat bir saat ileride (saat dilimi), blok 20 dakikalık", tb + 60*60 + 20*60, "bayat"},
		{"saat üç saat ileride (saat dilimi)", tb + 3*60*60, "bayat"},
	}
	for _, c := range rejected {
		if err := pl.CheckClock(c.now); !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (beklenen parça %q)", c.name, err, c.want)
		}
	}
}

// Bir denetim düşerse ya da arka uç güvenilmez çıkarsa deneme biter ve dosya kurulmaz.
func TestLaunchMineFailures(t *testing.T) {
	const tb, tc = btc976600Time, btc976601Time
	start := int64(tb + 900)
	boom := errors.New("sürücü hatası")
	failing := func([HeaderSize]byte, [32]byte, uint64, uint64) (uint64, bool, error) { return 0, false, boom }
	// Hedefi karşılamayan bir nonce'u "buldum" diye döndüren arka uç.
	lying := func(hdr [HeaderSize]byte, tgt [32]byte, from, count uint64) (uint64, bool, error) {
		for n := from; ; n++ {
			if _, ok := checkNonce(hdr, n, &tgt); !ok {
				return n, true, nil
			}
		}
	}
	closed := make(chan struct{})
	close(closed)
	cases := []struct {
		name  string
		scan  func([HeaderSize]byte, [32]byte, uint64, uint64) (uint64, bool, error)
		clock stepClock
		stop  <-chan struct{}
		is    error
		want  string
		calls int
	}{
		{"durdurma", cpuScan, stepClock{now: start}, closed, ErrMiningStopped, "", 0},
		{"arka uç hatası", failing, stepClock{now: start}, nil, boom, "hata verdi", 1},
		{"doğrulanamayan nonce", lying, stepClock{now: start}, nil, ErrLaunchCheck, "CPU doğrulamasından geçmedi", 1},
		{"saat başlangıçta t_b'den geride", cpuScan, stepClock{now: tb - 1}, nil, ErrLaunchCheck, "t_b = ", 0},
		{"blok başlangıçta bayat", cpuScan, stepClock{now: tb + launchMaxGenesisLag + 1}, nil, ErrLaunchCheck, "bayat", 0},
		{"saat arama sırasında geri gitti", neverFound, stepClock{now: start, step: -1}, nil, ErrLaunchCheck, "geri gitti", 1},
		{"saat nonce bulunurken geri gitti", cpuScan, stepClock{now: start, step: -1}, nil, ErrLaunchCheck, "geri gitti", 1},
		// Saat okuma başına 4 sn ilerler: sınırın 5 sn öncesi, 1 sn öncesi ve 3 sn sonrası aynı
		// başlıktadır; dördüncü okumada (7 sn sonrası) başlık tazelenirken sınır aşılmıştır.
		{"onaylayan blok arama sırasında eskidi", neverFound, stepClock{now: tc + launchMaxTipAge - 5, step: 4}, nil,
			ErrLaunchCheck, "sn geride", 3},
	}
	for _, c := range cases {
		fb := &fakeBackend{scan: c.scan}
		clock := c.clock
		got, err := easyPlan(t).Mine(fb, clock.read, c.stop)
		if got != nil || !errors.Is(err, c.is) || !strings.Contains(err.Error(), c.want) || fb.calls != c.calls {
			t.Errorf("%s: sonuç %+v, hata %v, arka uç çağrısı %d (beklenen %d, parça %q)", c.name, got, err, fb.calls,
				c.calls, c.want)
		}
	}

	pl := easyPlan(t)
	if got, err := pl.Mine(zeroRoundBackend{&fakeBackend{scan: cpuScan}}, (&stepClock{now: start}).read, nil); got != nil ||
		!errors.Is(err, errZeroRound) {
		t.Errorf("tur boyu sıfır arka uç: %+v %v", got, err)
	}
	if got, err := pl.Mine(nil, (&stepClock{now: start}).read, nil); got != nil || !errors.Is(err, ErrLaunchCheck) {
		t.Errorf("arka uçsuz kazım: %+v %v", got, err)
	}
	if got, err := pl.Mine(&fakeBackend{scan: cpuScan}, nil, nil); got != nil || !errors.Is(err, ErrLaunchCheck) {
		t.Errorf("saatsiz kazım: %+v %v", got, err)
	}

	// Nonce'un bulunduğu turda saat ileri atlarsa (uyku, takılan sürücü, saat adımı) başlık bayattır:
	// T_g bulunma anından genesisMaxAge'den fazla geride kalamaz. Sınırın kendisi kabul edilir.
	jumped := &stepClock{now: start}
	fb := &fakeBackend{scan: func(hdr [HeaderSize]byte, tgt [32]byte, from, count uint64) (uint64, bool, error) {
		jumped.now += genesisMaxAge + 1
		return cpuScan(hdr, tgt, from, count)
	}}
	if got, err := pl.Mine(fb, jumped.read, nil); got != nil || !errors.Is(err, ErrLaunchCheck) ||
		!strings.Contains(err.Error(), "başlığı bayat") || fb.calls != 1 {
		t.Errorf("son turda ileri atlayan saat: %+v %v", got, err)
	}
	edge := &stepClock{now: start}
	fb = &fakeBackend{scan: func(hdr [HeaderSize]byte, tgt [32]byte, from, count uint64) (uint64, bool, error) {
		edge.now += genesisMaxAge
		return cpuScan(hdr, tgt, from, count)
	}}
	if got, err := pl.Mine(fb, edge.read, nil); err != nil || got.Timestamp != start || got.Found != start+genesisMaxAge {
		t.Errorf("son turu sınır kadar süren arama: %+v %v", got, err)
	}

	// Kurulan dosya yükleyiciden geçmezse Mine onu döndürmez. Planın başlıkları kurulduktan sonra
	// bozulursa (burada yer değiştirir) genesis bulunur, ama dosya kendi denetiminden geçmez. Bu bir
	// lansman denetimi değil, iç tutarsızlıktır: hata ne ErrLaunchCheck ne ErrBadLaunchFile'dır ve
	// bulunan genesis'i taşır.
	broken := easyPlan(t)
	broken.headers = []string{btc976601, btc976600}
	_, valid := easyLaunch(t)
	got, err := broken.Mine(&fakeBackend{scan: cpuScan}, (&stepClock{now: start}).read, nil)
	if got != nil || err == nil || errors.Is(err, ErrLaunchCheck) || errors.Is(err, ErrBadLaunchFile) ||
		!strings.Contains(err.Error(), "kendi denetiminden geçmedi") || !strings.Contains(err.Error(), valid.Hash) {
		t.Errorf("yükleyiciden geçmeyen dosya döndü: %+v %v", got, err)
	}
	// Başarısız denemeler planı bozmaz: aynı plan sonra geçerli genesis'i bulur.
	if got, err := pl.Mine(&fakeBackend{scan: cpuScan}, (&stepClock{now: start}).read, nil); err != nil || got.Timestamp != start {
		t.Errorf("başarısız denemelerden sonra kazım: %+v %v", got, err)
	}
}

// Plan yalnızca genesis'i tanımsız, hedefleri seçilmiş bir ağ ve doğrulanan iki Bitcoin başlığıyla
// kurulur.
func TestNewLaunchPlanRefused(t *testing.T) {
	launched, lf := easyLaunch(t)
	if err := applyLaunch(launched, lf, easyLaunchRules); err != nil {
		t.Fatal(err)
	}
	unchosen := easyLaunchRules
	unchosen.targetsChosen = false
	flat := easyMainnet()
	flat.Bits, flat.PowLimitBits = 0x1f00ffff, 0x1f00ffff
	cases := []struct {
		name    string
		p       *Params
		rules   launchRules
		headers [2]string
		want    string
	}{
		{"genesis zaten tanımlı", launched, easyLaunchRules, [2]string{btc976600, btc976601}, "zaten tanımlı"},
		{"hedefler seçilmemiş", easyMainnet(), unchosen, [2]string{btc976600, btc976601}, "henüz seçilmedi"},
		{"taban kuralı tutmuyor", flat, easyLaunchRules, [2]string{btc976600, btc976601}, "min(başlangıç hedefi"},
		{"yer değiştirmiş başlıklar", easyMainnet(), easyLaunchRules, [2]string{btc976601, btc976600}, "bağlanmıyor"},
		{"aynı başlık iki kez", easyMainnet(), easyLaunchRules, [2]string{btc976600, btc976600}, "bağlanmıyor"},
		{"ilk başlığın iş ispatı bozuk", easyMainnet(), easyLaunchRules, [2]string{btc976600[:159] + "b", btc976601},
			"mesajdaki Bitcoin başlığı: iş ispatı"},
		{"büyük harfli ikinci başlık", easyMainnet(), easyLaunchRules, [2]string{btc976600, strings.ToUpper(btc976601)},
			"onaylayan Bitcoin başlığı: 160 küçük harf"},
		{"zorluk tabanının dışındaki başlıklar", easyMainnet(), easyLaunchRules, [2]string{btc0, btc1},
			"gerçek Bitcoin zorluğu"},
		{"boş başlıklar", easyMainnet(), easyLaunchRules, [2]string{"", ""}, "mesajdaki Bitcoin başlığı: 160 küçük harf"},
	}
	for _, c := range cases {
		pl, err := newLaunchPlan(c.p, c.rules, c.headers[0], c.headers[1])
		if pl != nil || !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (beklenen parça %q)", c.name, err, c.want)
		}
	}
	if pl, err := newLaunchPlan(nil, easyLaunchRules, btc976600, btc976601); pl != nil || !errors.Is(err, ErrLaunchCheck) {
		t.Errorf("parametresiz plan: %v", err)
	}
	// Plan parametrelerin kopyasıyla çalışır: sonradan değişen ağ planı etkilemez.
	p := easyMainnet()
	pl, err := newLaunchPlan(p, easyLaunchRules, btc976600, btc976601)
	if err != nil {
		t.Fatal(err)
	}
	p.Bits = 0x1f00ffff
	if pl.Info().Bits != 0x200fffff {
		t.Errorf("plan verilen parametreleri paylaşıyor: %+v", pl.Info())
	}
}

// Gerçek mainnet kurallarıyla araç bugünkü duruma göre davranır: hedefler seçilmeden genesis
// kazılmaz; lansmandan sonra (genesis tanımlıyken) hiç kazılmaz.
func TestMainnetLaunchPlan(t *testing.T) {
	pl, err := NewMainnetLaunchPlan(btc976600, btc976601)
	switch {
	case MainnetParams.GenesisHash != "":
		if pl != nil || !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), "zaten tanımlı") {
			t.Fatalf("genesis'i tanımlı mainnet için plan kuruldu: %v", err)
		}
	case !mainnetLaunchRules.targetsChosen:
		if pl != nil || !errors.Is(err, ErrLaunchCheck) || !strings.Contains(err.Error(), "henüz seçilmedi") {
			t.Fatalf("hedefleri seçilmemiş mainnet için plan kuruldu: %v", err)
		}
	default:
		if err != nil || pl.Info().Message != mainnetGenesisMessagePrefix+btc976600Hash || pl.Info().Bits != MainnetParams.Bits {
			t.Fatalf("hedefleri seçilmiş, genesis'siz mainnet için plan kurulamadı: %v", err)
		}
	}
}

// Verify yazılan dosyayı baştan denetler: bozulmuş dosya, boş dosya ve başka başlıklara bağlanan
// geçerli dosya reddedilir.
func TestLaunchPlanVerify(t *testing.T) {
	pl := easyPlan(t)
	_, valid := easyLaunch(t)
	raw, err := valid.encode()
	if err != nil {
		t.Fatal(err)
	}
	if hash, err := pl.Verify(raw); err != nil || hash != valid.Hash {
		t.Fatalf("geçerli dosya: %q %v", hash, err)
	}
	if hash, err := pl.Verify([]byte(strings.ReplaceAll(string(raw), "\n", "\r\n"))); err != nil || hash != valid.Hash {
		t.Fatalf("CRLF satır sonlu dosya: %q %v", hash, err)
	}
	other := *valid
	other.Nonce++
	tampered, err := other.encode()
	if err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name string
		raw  []byte
		want string
	}{
		{"başka nonce", tampered, "genesis özeti"},
		{"boş lansman dosyası", []byte("{}\n"), "dosya boş"},
		{"kesik dosya", raw[:len(raw)-2], ""},
		{"boş içerik", nil, ""},
	}
	for _, c := range bad {
		if hash, err := pl.Verify(c.raw); hash != "" || !errors.Is(err, ErrBadLaunchFile) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q %v", c.name, hash, err)
		}
	}
	// Dosya geçerli olsa da bu denemenin başlıklarına bağlanmıyorsa reddedilir. 0 ve 1. bloklara
	// bağlanan plan yalnızca Bitcoin zorluk tabanı gevşetilince kurulur.
	loose := easyLaunchRules
	loose.bitcoinMaxBits = 0x1d00ffff
	early, err := newLaunchPlan(easyMainnet(), loose, btc0, btc1)
	if err != nil {
		t.Fatal(err)
	}
	if hash, err := early.Verify(raw); hash != "" || !errors.Is(err, ErrBadLaunchFile) ||
		!strings.Contains(err.Error(), "bu denemenin başlıkları") {
		t.Fatalf("başka başlıklara bağlanan dosya: %q %v", hash, err)
	}

	if !EmptyLaunchFile([]byte("{}\n")) || !EmptyLaunchFile([]byte("{}\r\n")) || EmptyLaunchFile([]byte("{}")) ||
		EmptyLaunchFile(nil) || EmptyLaunchFile(raw) {
		t.Fatal("boş lansman dosyası yanlış tanındı")
	}
}
