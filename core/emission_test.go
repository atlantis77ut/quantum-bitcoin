package core

import (
	"errors"
	"testing"
)

// Değerler Go'dan bağımsız bir Python betiğiyle üretildi (tam sayı aritmetiği).
func TestBlockSubsidyKAT(t *testing.T) {
	mainCases := []struct{ h, want int64 }{
		{0, 0}, {-1, 0}, {-1 << 62, 0},
		{1, 25000}, {2, 50000}, {9_999, 249975000}, {10_000, 250000000},
		{19_999, 499975000}, {20_000, 500000000},
		{2_099_999, 500000000}, {2_100_000, 250000000}, {4_200_000, 125000000},
		{12_599_999, 15625000}, {12_600_000, 10000000},
		{1 << 62, 10000000}, {maxInt64, 10000000},
	}
	for _, c := range mainCases {
		if got := BlockSubsidy(c.h, MainnetParams); got != c.want {
			t.Errorf("mainnet h=%d: %d, beklenen %d", c.h, got, c.want)
		}
		if got := BlockSubsidy(c.h, TestnetParams); got != c.want {
			t.Errorf("testnet h=%d: %d, beklenen %d", c.h, got, c.want)
		}
	}
	reg := []struct{ h, want int64 }{
		{1, 5000000000}, {149, 5000000000}, {150, 2500000000}, {299, 2500000000},
		{300, 1250000000}, {1349, 19531250}, {1350, 10000000}, {1_000_000, 10000000},
	}
	for _, c := range reg {
		if got := BlockSubsidy(c.h, RegtestParams); got != c.want {
			t.Errorf("regtest h=%d: %d, beklenen %d", c.h, got, c.want)
		}
	}
}

func TestSupplyAtKAT(t *testing.T) {
	cases := []struct {
		p       *Params
		h, want int64
	}{
		{MainnetParams, 19_999, 4999750000000},
		{MainnetParams, 20_000, 5000250000000},
		{MainnetParams, 2_099_999, 1044999750000000},
		{MainnetParams, 12_599_999, 2062187250000000}, // yarılanma dönemi arzı: 20.621.872,5 QBTC
		{MainnetParams, 12_600_000, 2062187260000000}, // ilk kuyruk bloğu
		{MainnetParams, 100_000_000, 2936187260000000},
		{RegtestParams, 149, 745000000000},
		{RegtestParams, 150, 747500000000},
		{RegtestParams, 1349, 1492070312500},
		{RegtestParams, 1350, 1492080312500},
		{RegtestParams, 1500, 1493580312500},
	}
	for _, c := range cases {
		got, ok := SupplyAt(c.h, c.p)
		if !ok || got != c.want {
			t.Errorf("%s arz(%d) = %d (%v), beklenen %d", c.p.Name, c.h, got, ok, c.want)
		}
	}
	if _, ok := SupplyAt(maxInt64, MainnetParams); ok {
		t.Error("taşan arz ok döndü")
	}
}

// SupplyAt'in dönem bazlı toplamı, blok blok toplamla aynı olmalı (rampa ve kuyruk dahil).
func TestSupplyAtMatchesBruteForce(t *testing.T) {
	small := *RegtestParams
	small.HalvingInterval = 20
	small.SlowStartBlocks = 7
	small.InitialSubsidy = 3*Coin + 1 // tek sayı: rampa ve yarılanma yuvarlaması sınansın
	if err := small.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*Params{RegtestParams, &small} {
		var sum int64
		for h := int64(1); h <= 2_000; h++ {
			sum += BlockSubsidy(h, p)
			if got, ok := SupplyAt(h, p); !ok || got != sum {
				t.Fatalf("%s h=%d: SupplyAt %d, toplam %d", p.Name, h, got, sum)
			}
		}
	}
}

// Rampa sonrasında ödül hiç artmaz ve kuyruğun altına inmez; tüm dönem sınırlarında.
func TestSubsidyMonotonicWithTailFloor(t *testing.T) {
	for _, p := range []*Params{MainnetParams, RegtestParams} {
		prev := p.InitialSubsidy
		for k := int64(1); k <= 70; k++ {
			for _, h := range []int64{k*p.HalvingInterval - 1, k * p.HalvingInterval} {
				r := BlockSubsidy(h, p)
				if r > prev || r < p.TailSubsidy {
					t.Fatalf("%s h=%d: ödül %d (önceki %d, kuyruk %d)", p.Name, h, r, prev, p.TailSubsidy)
				}
				prev = r
			}
		}
		// Rampa artandır ve ilk dönem ödülüne ulaşır.
		for h := int64(1); h < p.SlowStartBlocks; h++ {
			if BlockSubsidy(h, p) > BlockSubsidy(h+1, p) {
				t.Fatalf("%s rampa h=%d'de azalıyor", p.Name, h)
			}
		}
	}
}

func TestEmissionParams(t *testing.T) {
	for _, p := range []*Params{MainnetParams, TestnetParams} {
		if p.TargetSpacing != 60 || p.InitialSubsidy != 5*Coin || p.TailSubsidy != Coin/10 ||
			p.HalvingInterval != 2_100_000 || p.SlowStartBlocks != 20_000 || p.CoinbaseMaturity != 240 {
			t.Errorf("%s emisyonu kararla uyuşmuyor: %+v", p.Name, p)
		}
	}
	for _, p := range []*Params{MainnetParams, TestnetParams, RegtestParams} {
		if err := p.Validate(); err != nil {
			t.Error(err)
		}
	}

	// 30 sn: ödüller yarıya, aralıklar iki katına; zamana göre eğri aynı kalır.
	p30 := withEmission(&Params{Name: "p30", TargetSpacing: 30})
	if p30.InitialSubsidy != 5*Coin/2 || p30.TailSubsidy != Coin/20 ||
		p30.HalvingInterval != 4_200_000 || p30.SlowStartBlocks != 40_000 || p30.CoinbaseMaturity != 480 {
		t.Errorf("30 sn türetimi: %+v", p30)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("tam bölünmeyen blok süresi (7 sn) kabul edildi")
			}
		}()
		withEmission(&Params{Name: "p7", TargetSpacing: 7})
	}()

	bad := []func(p *Params){
		func(p *Params) { p.HalvingInterval = 0 },
		func(p *Params) { p.TailSubsidy = 0 },
		func(p *Params) { p.TailSubsidy = p.InitialSubsidy + 1 },
		func(p *Params) { p.InitialSubsidy = 0 },
		func(p *Params) { p.InitialSubsidy = maxSubsidy + 1 },
		func(p *Params) { p.SlowStartBlocks = -1 },
		func(p *Params) { p.SlowStartBlocks = p.HalvingInterval },
		func(p *Params) { p.TargetSpacing = 0 },
		func(p *Params) { p.CoinbaseMaturity = 0 },
		func(p *Params) { p.HalfLife = minHalfLifePerFTL*MaxFutureDrift - 1 },
		// yeniden hedeflemesiz yalnızca regtest olabilir; başlangıç hedefi geçerli olmalı
		func(p *Params) { p.Name = "testnet" },
		func(p *Params) { p.Bits = 0x21010000 },
	}
	for i, mut := range bad {
		p := *RegtestParams
		mut(&p)
		if err := p.Validate(); !errors.Is(err, ErrBadParams) {
			t.Errorf("durum %d: %v", i, err)
		}
		if _, err := NewBlockchain(t.TempDir(), &p); !errors.Is(err, ErrBadParams) {
			t.Errorf("durum %d NewBlockchain: %v", i, err)
		}
	}
}

// Zincir üzerinde: AddBlock rampa ve yarılanma boyunca doğru ödülü basar ve toplam arz
// SupplyAt'e eşittir; yarılanmadan sonra eski ödülü isteyen blok reddedilir.
func TestChainFollowsSchedule(t *testing.T) {
	p := *RegtestParams
	p.HalvingInterval = 12
	p.SlowStartBlocks = 5
	bc, err := NewBlockchain(t.TempDir(), &p)
	if err != nil {
		t.Fatal(err)
	}
	clock := int64(1_900_000_000)
	bc.now = func() int64 { clock++; return clock }
	t.Cleanup(func() { bc.Close() })

	miner := newKey(t)
	for h := int64(1); h <= 30; h++ {
		b, err := bc.AddBlock(nil, miner.Address)
		if err != nil {
			t.Fatal(err)
		}
		if b.Transactions[0].Amount != BlockSubsidy(h, &p) {
			t.Fatalf("h=%d coinbase %d, beklenen %d", h, b.Transactions[0].Amount, BlockSubsidy(h, &p))
		}
	}
	// Harcanabilir toplam: olgunlaşmış bloklarınki (son CoinbaseMaturity blok henüz değil).
	want, _ := SupplyAt(30-p.CoinbaseMaturity, &p)
	if got := mustBalance(t, bc, miner.Address); got != want {
		t.Fatalf("olgun arz %d, beklenen %d", got, want)
	}

	// Yükseklik 31 üçüncü dönemde (ödül InitialSubsidy/4): ilk dönem ödülünü isteyen blok.
	other := newKey(t)
	greedy := Transaction{Sender: CoinbaseSender, Receiver: other.Address, Amount: p.InitialSubsidy, IsCoinbase: true}
	b := craftBlock(t, bc, []Transaction{greedy})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadCoinbase) {
		t.Fatalf("eski ödül: %v", err)
	}
	honest := Transaction{Sender: CoinbaseSender, Receiver: other.Address, Amount: BlockSubsidy(31, &p), IsCoinbase: true}
	if err := bc.AcceptBlock(craftBlock(t, bc, []Transaction{honest})); err != nil {
		t.Fatalf("doğru ödül reddedildi: %v", err)
	}
}

// Ağdan gelen negatif veya sıfır yükseklik panik yaratmaz; reddedilir.
func TestRejectsNonPositiveHeight(t *testing.T) {
	bc := newTestChain(t)
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []int64{0, -1, -1 << 40, -1 << 63} {
		cb := Transaction{Sender: CoinbaseSender, Receiver: newKey(t).Address, IsCoinbase: true, Nonce: uint64(idx)}
		b := &Block{Index: idx, Timestamp: bc.now(), Transactions: []Transaction{cb}, PrevHash: tip.Hash}
		mineAt(t, bc, b)
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadIndex) {
			t.Fatalf("yükseklik %d: %v", idx, err)
		}
	}
}
