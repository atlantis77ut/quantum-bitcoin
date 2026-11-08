package core

import (
	"bufio"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// BCHN'nin yayımladığı aserti3-2d vektörleri (bkz. testdata/asert/README.md): tamsayı
// algoritması bağımsız bir uygulamayla bit bit aynı sonucu vermeli.
func TestASERTVectors(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "asert", "run*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("vektör dosyası yok: %v", err)
	}
	maxTarget, _ := compactToBig(0x1d00ffff)
	total := 0
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var anchorHeight, anchorParentTime int64
		var anchorTarget *big.Int
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			switch {
			case strings.HasPrefix(line, "##"):
				kv := strings.SplitN(strings.TrimPrefix(line, "##"), ":", 2)
				if len(kv) != 2 {
					continue
				}
				v := strings.TrimSpace(kv[1])
				switch strings.TrimSpace(kv[0]) {
				case "anchor height":
					anchorHeight = mustInt(t, v)
				case "anchor ancestor time", "anchor parent time":
					anchorParentTime = mustInt(t, v)
				case "anchor nBits":
					bits := uint32(mustInt(t, v))
					var ok bool
					if anchorTarget, ok = compactToBig(bits); !ok {
						t.Fatalf("%s: çapa hedefi %08x", path, bits)
					}
				}
			case line == "" || strings.HasPrefix(line, "#"):
			default:
				fs := strings.Fields(line)
				if len(fs) != 4 || anchorTarget == nil {
					t.Fatalf("%s: satır %q", path, line)
				}
				h, ts, want := mustInt(t, fs[1]), mustInt(t, fs[2]), uint32(mustInt(t, fs[3]))
				got := bigToCompact(asertTarget(anchorTarget, ts-anchorParentTime, h-anchorHeight, 600, 172800, maxTarget))
				if got != want {
					t.Fatalf("%s: yükseklik %d zaman %d: %08x, beklenen %08x", path, h, ts, got, want)
				}
				total++
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	if total < 4000 {
		t.Fatalf("yalnızca %d vektör sınandı", total)
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		t.Fatalf("sayı %q: %v", s, err)
	}
	return v
}

// Çapa 1. bloktur: 1. ve 2. blok başlangıç hedefini taşır; ideal takvimden bir yarı ömür geride
// kalan zincirin hedefi iki katına çıkar, önde olanınki yarıya iner; sınırlar kırpılır.
func TestASERTNextBits(t *testing.T) {
	p := TestnetParams
	anchor := int64(1_800_000_000)
	start, _ := TargetFromBits(p.Bits)
	target := func(bits uint32) *big.Int {
		x, err := TargetFromBits(bits)
		if err != nil {
			t.Fatalf("%08x: %v", bits, err)
		}
		return x
	}
	if p.nextBits(0, p.GenesisTimestamp, 0) != p.Bits || p.nextBits(1, anchor, anchor) != p.Bits {
		t.Fatal("1. ve 2. blok başlangıç hedefini taşımalı")
	}
	onTime := func(h int64) int64 { return anchor + p.TargetSpacing*(h-1) }
	if got := p.nextBits(500, onTime(500), anchor); got != p.Bits {
		t.Fatalf("takvimdeki zincir: %08x, beklenen %08x", got, p.Bits)
	}
	late := target(p.nextBits(500, onTime(500)+p.HalfLife, anchor))
	early := target(p.nextBits(500, onTime(500)-p.HalfLife, anchor))
	two := new(big.Int).Lsh(start, 1)
	half := new(big.Int).Rsh(start, 1)
	if ratio(late, two) > 1e-4 || ratio(early, half) > 1e-4 {
		t.Fatalf("yarı ömür: geç %s (beklenen ~%s), erken %s (beklenen ~%s)", late, two, early, half)
	}
	if got := target(p.nextBits(500, onTime(500)+1000*p.HalfLife, anchor)); got.Cmp(powLimit) > 0 {
		t.Fatal("hedef powLimit'i aştı")
	}
	if got := target(p.nextBits(500, onTime(500)-1000*p.HalfLife, anchor)); got.Sign() <= 0 {
		t.Fatal("hedef sıfıra indi")
	}
	if RegtestParams.nextBits(500, onTime(500)+p.HalfLife, anchor) != RegtestParams.Bits {
		t.Fatal("regtest yeniden hedeflememeli")
	}
}

func ratio(a, b *big.Int) float64 {
	fa, _ := new(big.Float).SetInt(a).Float64()
	fb, _ := new(big.Float).SetInt(b).Float64()
	return math.Abs(fa/fb - 1)
}

// asertParams yeniden hedeflemeli bir regtest kopyasıdır (aynı genesis).
func asertParams() *Params {
	p := *RegtestParams
	p.NoRetarget = false
	return &p
}

func newChainWith(t *testing.T, p *Params, clock *int64) *Blockchain {
	t.Helper()
	bc, err := NewBlockchain(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	bc.now = func() int64 { *clock++; return *clock }
	t.Cleanup(func() { bc.Close() })
	return bc
}

// Zincir üzerinde: hızlı kazılan zincirin hedefi zorlaşır; yanlış hedefli blok PoW ve gövde
// kontrolünden önce, önbelleğe alınmadan reddedilir; başlık senkronizasyonu (oturumdaki
// atalar dahil) aynı hedefleri hesaplar.
func TestASERTOnChain(t *testing.T) {
	p := asertParams()
	clock := int64(1_900_000_000)
	a := newChainWith(t, p, &clock)
	mine(t, a, 150, newKey(t).Address) // ~2 sn'de bir blok: takvimin çok önünde
	tip := tipBlock(t, a)
	start, _ := TargetFromBits(p.Bits)
	cur, _ := TargetFromBits(tip.Bits)
	if cur.Cmp(start) >= 0 {
		t.Fatalf("hızlı zincirin hedefi zorlaşmadı: %08x", tip.Bits)
	}
	if b1, _ := a.BlockByHash(a.main[1]); b1.Bits != p.Bits {
		t.Fatal("1. blok başlangıç hedefini taşımalı")
	}

	// Ucun beklenenden kolay hedefiyle kazılmış blok: ucuz ret, önbellek ve gövde kontrolü yok.
	cb := Transaction{Sender: CoinbaseSender, Receiver: newKey(t).Address, Amount: BlockSubsidy(tip.Index+1, p), IsCoinbase: true}
	easy := &Block{Index: tip.Index + 1, Timestamp: a.now(), Transactions: []Transaction{cb}, PrevHash: tip.Hash}
	easy.Transactions[0].Nonce = uint64(easy.Index)
	if err := easy.MineBlock(p.Bits); err != nil {
		t.Fatal(err)
	}
	checks := a.bodyChecks.Load()
	if err := a.AcceptBlock(easy); !errors.Is(err, ErrBadDifficulty) {
		t.Fatalf("kolay hedefli blok: %v, beklenen ErrBadDifficulty", err)
	}
	if a.bodyChecks.Load() != checks || a.rejected.has(easy.Hash) {
		t.Fatal("yanlış hedefli blok gövde kontrolüne girdi ya da önbelleğe alındı")
	}

	// Senkronizasyon: dst'nin indeksi yalnızca genesis'i içerir, hedefler oturum başlıklarından
	// (çapa zamanı dahil) hesaplanmalıdır.
	b := newChainWith(t, p, &clock)
	if err := syncFrom(t, b, a, 40); err != nil {
		t.Fatal(err)
	}
	if tipHash(t, b) != tipHash(t, a) {
		t.Fatal("ASERT zinciri senkronize edilemedi")
	}

	// Yeniden açılışta çapa zamanı diskten okunur: hedef aynı kalır.
	dir := t.TempDir()
	c, err := NewBlockchain(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	c.now = a.now
	if err := syncFrom(t, c, a, MaxHeadersPerMsg); err != nil {
		t.Fatal(err)
	}
	want := c.Bits()
	c.Close()
	c, err = NewBlockchain(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Bits() != want || want != a.Bits() {
		t.Fatalf("yeniden açılışta hedef %08x, beklenen %08x", c.Bits(), want)
	}
}

// Emisyonun takvimden sapma simülasyonu (CLAUDE.md madde 3). Üretimdeki nextBits, gerçek PoW
// olmadan üstel dağılımlı çözüm süreleriyle sürülür; zaman damgaları MTP ve FTL kurallarına
// uyar. ASERT çapaya göre mutlak olduğundan yükseklik ideal takvimden en fazla
// (yarı ömür/T)·|log2(hashrate oranı)| kadar sapar; sapma birikmez.
func TestASERTEmissionDrift(t *testing.T) {
	p := TestnetParams
	T := float64(p.TargetSpacing)
	hl := float64(p.HalfLife) / T // yarı ömür, blok
	startWork := workOf(t, p.Bits)
	h0 := startWork / T // başlangıç hedefini tam T'de çözen hashrate

	type scenario struct {
		name     string
		rate     func(h int, now float64) float64
		attacker float64 // saldırganın hashrate payı
		stamp    string  // "ftl" | "mtp": saldırganın zaman damgası stratejisi
		maxDev   float64 // |yükseklik − takvim| üst sınırı, blok
	}
	noise := 100.0 // ~5σ: yükseklik gürültüsü yarı ömür ölçeğinde sönümlenir (Python ön simülasyonunda |sapma| ≤ 25)
	scenarios := []scenario{
		{name: "sabit", rate: func(int, float64) float64 { return h0 }, maxDev: noise},
		{name: "1000 kat kolay başlangıç", rate: func(int, float64) float64 { return 1000 * h0 }, maxDev: hl*math.Log2(1000) + noise},
		{name: "1000 kat zor başlangıç", rate: func(int, float64) float64 { return h0 / 1000 }, maxDev: hl*math.Log2(1000) + noise},
		{name: "10 kat sıçrama ve düşüş", rate: func(h int, _ float64) float64 {
			if h >= 5000 && h < 10000 {
				return 10 * h0
			}
			return h0
		}, maxDev: hl*math.Log2(10) + noise},
		{name: "kiralık hashrate gel-git", rate: func(h int, _ float64) float64 {
			if (h/60)%2 == 1 {
				return 5 * h0
			}
			return h0
		}, maxDev: hl*math.Log2(5) + noise},
		{name: "yılda 2 kat büyüme", rate: func(_ int, now float64) float64 {
			return h0 * math.Pow(2, now/(365*24*3600))
		}, maxDev: hl*math.Log2(2) + noise},
		{name: "%30 saldırgan FTL damgası", rate: func(int, float64) float64 { return h0 }, attacker: 0.3, stamp: "ftl", maxDev: noise + 20},
		{name: "%30 saldırgan MTP+1 damgası", rate: func(int, float64) float64 { return h0 }, attacker: 0.3, stamp: "mtp", maxDev: noise + 20},
	}
	const blocks = 20_000
	for _, sc := range scenarios {
		for _, seed := range []int64{1, 2} {
			r := rand.New(rand.NewSource(seed))
			anchor := int64(1_900_000_000)
			now := float64(anchor)
			times := []int64{anchor} // son MedianTimeSpan zaman damgası (1. blok = çapa)
			parentTs := anchor
			attackerBlocks := 0
			for h := 2; h <= blocks; h++ {
				bits := p.nextBits(int64(h-1), parentTs, anchor)
				now += r.ExpFloat64() * workOf(t, bits) / sc.rate(h, now-float64(anchor))
				ts := int64(now)
				byAttacker := r.Float64() < sc.attacker
				if byAttacker {
					attackerBlocks++
					if sc.stamp == "ftl" {
						ts = int64(now) + MaxFutureDrift
					} else {
						ts = math.MinInt64
					}
				}
				if mtp := median(times); ts <= mtp {
					ts = mtp + 1
				}
				if ts > int64(now)+MaxFutureDrift {
					t.Fatalf("%s: simülasyon FTL'i aştı", sc.name)
				}
				parentTs = ts
				times = append(times, ts)
				if len(times) > MedianTimeSpan {
					times = times[1:]
				}
			}
			// Son sapma: takvimden kaçan blok sayısı. Kolay/zor başlangıçta geçiş sapması
			// kalıcıdır (ASERT yeni hashrate'te ideal takvime geri dönmez, ona paralel gider).
			final := float64(blocks-1) - (now-float64(anchor))/T
			if math.Abs(final) > sc.maxDev {
				t.Errorf("%s (tohum %d): takvimden sapma %.0f blok, sınır %.0f", sc.name, seed, final, sc.maxDev)
			}
			if sc.attacker > 0 {
				share := float64(attackerBlocks) / float64(blocks-1)
				if share > sc.attacker*1.05 {
					t.Errorf("%s (tohum %d): saldırgan payı %.3f, hashrate payı %.2f", sc.name, seed, share, sc.attacker)
				}
			}
		}
	}
}

func workOf(t *testing.T, bits uint32) float64 {
	t.Helper()
	w, err := blockWork(bits)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := new(big.Float).SetInt(w).Float64()
	return f
}

func median(ts []int64) int64 {
	c := append([]int64(nil), ts...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c[len(c)/2]
}

// Ebeveyni bilinmeyen blok ucun hedefinden 4 kattan fazla kolay hedef ilan edemez (bedava
// "yetim" bloklarla özet hesaplatıp senkronizasyon tetiklenemez); ucun hedefine yakın olanı
// her zamanki gibi ErrBadPrevHash alır. Oturumda yanlış hedefli başlık ve AddBlock'un
// izlediği precheck yolu da yanlış hedefi reddeder.
func TestASERTDifficultyGates(t *testing.T) {
	p := asertParams()
	clock := int64(1_900_000_000)
	a := newChainWith(t, p, &clock)
	mine(t, a, 400, newKey(t).Address) // takvimin çok önünde: hedef başlangıçtan çok zor
	tip := tipBlock(t, a)
	start, _ := TargetFromBits(p.Bits)
	next, _ := TargetFromBits(a.Bits())
	if new(big.Int).Lsh(next, orphanEasyShift).Cmp(start) >= 0 {
		t.Fatalf("hedef yeterince zorlaşmadı: %08x", a.Bits())
	}

	orphan := func(bits uint32) *Block {
		cb := Transaction{Sender: CoinbaseSender, Receiver: newKey(t).Address, Amount: 1, IsCoinbase: true, Nonce: uint64(tip.Index + 5)}
		b := &Block{Index: tip.Index + 5, Timestamp: a.now(), Transactions: []Transaction{cb},
			PrevHash: strings.Repeat("ab", 32)}
		if err := b.MineBlock(bits); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if err := a.AcceptBlock(orphan(p.Bits)); !errors.Is(err, ErrOrphanTooEasy) {
		t.Fatalf("kolay hedefli yetim: %v, beklenen ErrOrphanTooEasy", err)
	}
	if err := a.AcceptBlock(orphan(a.Bits())); !errors.Is(err, ErrBadPrevHash) {
		t.Fatalf("ucun hedefindeki yetim: %v, beklenen ErrBadPrevHash", err)
	}

	// Oturum: son başlığın hedefi değiştirilir (özet de yeniden kazılır); öncekiler kabul edilir.
	b := newChainWith(t, p, &clock)
	hs := a.HeadersAfter(b.Locator(), MaxHeadersPerMsg)
	last := &hs[len(hs)-1]
	forged := last.asBlock()
	forged.Bits = p.Bits
	regrind(forged)
	last.Bits, last.Nonce, last.Hash = forged.Bits, forged.Nonce, forged.Hash
	if n, err := b.NewHeaderSync().Add(hs); !errors.Is(err, ErrBadDifficulty) || n != len(hs)-1 {
		t.Fatalf("yanlış hedefli başlık: n=%d err=%v", n, err)
	}

	// AddBlock'un izlediği yol (AcceptBlock kapısı yok): precheck yanlış hedefi reddeder.
	bad := blockOn(t, a, tip, newKey(t).Address)
	bad.Bits = p.Bits
	regrind(bad)
	a.mu.Lock()
	_, err := a.precheckLocked(bad)
	a.mu.Unlock()
	if !errors.Is(err, ErrBadDifficulty) || a.rejected.has(bad.Hash) {
		t.Fatalf("precheck: %v (önbellekte: %v)", err, a.rejected.has(bad.Hash))
	}
}

// Çapa zamanı dala özgüdür: rakip bir 1. blok kendi zamanıyla çapadır ve onun üzerindeki
// bloklar o çapaya göre hedeflenir; yeniden açılışta korunur.
func TestASERTAnchorPerBranch(t *testing.T) {
	p := asertParams()
	clock := int64(1_900_000_000)
	a := newChainWith(t, p, &clock)
	mine(t, a, 3, newKey(t).Address)
	g, _ := a.BlockByHash(a.main[0])
	clock += 5000 // rakip 1. blok çok daha geç
	alt := blockOn(t, a, g, newKey(t).Address)
	mustAccept(t, a, alt)
	main1, _ := a.BlockByHash(a.main[1])
	a.mu.RLock()
	ea, em := a.index[alt.Hash], a.index[main1.Hash]
	a.mu.RUnlock()
	if ea.AnchorTime != alt.Timestamp || em.AnchorTime != main1.Timestamp || ea.AnchorTime == em.AnchorTime {
		t.Fatalf("çapa zamanları: rakip %d/%d, ana %d/%d", ea.AnchorTime, alt.Timestamp, em.AnchorTime, main1.Timestamp)
	}
	alt2 := blockOn(t, a, alt, newKey(t).Address)
	mustAccept(t, a, alt2)
	want := p.nextBits(alt2.Index, alt2.Timestamp, alt.Timestamp)
	if got, err := a.BitsAfter(alt2.Hash); err != nil || got != want {
		t.Fatalf("rakip dalın hedefi %08x (%v), beklenen %08x", got, err, want)
	}
}
