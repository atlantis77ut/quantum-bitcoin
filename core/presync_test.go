package core

import (
	"errors"
	"testing"
)

const (
	testWindow = 8 // oturum penceresi (kontrol noktası aralığı 2)
	testBatch  = 5 // başlık yanıtı boyutu
)

// presyncFrom ağ katmanı gibi src'nin başlıklarını dst'nin ön-senkronizasyonuna besler.
func presyncFrom(t *testing.T, dst, src *Blockchain) *Presync {
	t.Helper()
	p := dst.NewPresync(testWindow)
	loc := dst.Locator()
	for !p.Done() {
		hs := src.HeadersAfter(loc, testBatch)
		if len(hs) == 0 {
			break
		}
		if _, err := p.Add(hs); err != nil {
			t.Fatal(err)
		}
		loc = append([]string{p.Last()}, dst.Locator()...)
	}
	return p
}

// provenSync ön-senkronizasyonu src'den parça parça yeniden indirir (ağ katmanının aynısı).
func provenSync(t *testing.T, dst, src *Blockchain, p *Presync) error {
	t.Helper()
	resume := ""
	for round := 0; round < 100; round++ {
		s := dst.NewProvenSync(p, testWindow)
		loc := dst.Locator()
		if resume != "" {
			loc = append([]string{resume}, loc...)
		}
		for {
			hs := src.HeadersAfter(loc, testBatch)
			if len(hs) == 0 {
				break
			}
			if _, err := s.Add(hs); err != nil {
				if errors.Is(err, ErrSyncLimit) {
					break
				}
				return err
			}
			if len(hs) < testBatch {
				break
			}
			loc = append([]string{s.Last()}, dst.Locator()...)
		}
		if !s.MoreWork() || s.AuthorizedTip() == "" {
			return errors.New("yeniden indirme ilerlemedi")
		}
		for _, h := range s.Missing() {
			b, err := src.BlockByHash(h)
			if err != nil {
				return err
			}
			if err := dst.AcceptBlock(b); err != nil && !errors.Is(err, ErrKnownBlock) {
				return err
			}
		}
		resume = s.AuthorizedTip()
		if s.ReachedFinal() {
			return nil
		}
	}
	return errors.New("yeniden indirme bitmedi")
}

func indexLen(bc *Blockchain) int {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return len(bc.index)
}

// forked dst'nin kendi n bloklu zincirini ve src'nin m bloklu, genesis'ten ayrılan zincirini kurar.
func forked(t *testing.T, n, m int) (dst, src *Blockchain) {
	t.Helper()
	dst, src = newTestChain(t), newTestChain(t)
	mine(t, dst, n, newKey(t).Address)
	mine(t, src, m, newKey(t).Address)
	return dst, src
}

// Ucun işini ancak oturum penceresinden derinde geçen dal ön-senkronizasyonla sayılır,
// parça parça yeniden indirilir ve benimsenir; kalan iş olağan senkronizasyonla gelir.
func TestPresyncAdoptsDeepBranch(t *testing.T) {
	dst, src := forked(t, 25, 30)
	before := dst.TipHash()
	if err := syncLimited(t, dst, src, testBatch, testWindow); err != nil {
		t.Fatal(err)
	}
	if dst.TipHash() != before {
		t.Fatal("pencere ucun işini geçmeden gövde indirildi")
	}

	p := presyncFrom(t, dst, src)
	if !p.Done() || p.finalHeight != 26 {
		t.Fatalf("ön-senkronizasyon: tamam %v, son yükseklik %d (beklenen 26)", p.Done(), p.finalHeight)
	}
	if len(p.window) > presyncWindow || int64(len(p.checkpoints)) > p.finalHeight/p.every+1 {
		t.Fatalf("ön-senkronizasyon belleği sınırsız: pencere %d, kontrol noktası %d", len(p.window), len(p.checkpoints))
	}
	if err := provenSync(t, dst, src, p); err != nil {
		t.Fatal(err)
	}
	if dst.TipHash() != mainAt(t, src, 26).Hash {
		t.Fatal("derin dal son başlığa kadar benimsenmedi")
	}
	if err := syncFrom(t, dst, src, testBatch); err != nil {
		t.Fatal(err)
	}
	if dst.TipHash() != src.TipHash() {
		t.Fatal("kalan zincir olağan senkronizasyonla gelmedi")
	}
}

// Ucun işini hiç geçmeyen (düşük işli) başlık zinciri sabit bellekte sayılır ve hiçbir şey
// saklanmaz.
func TestPresyncSpamBounded(t *testing.T) {
	dst, src := forked(t, 25, 20)
	n := indexLen(dst)
	p := presyncFrom(t, dst, src)
	if p.Done() {
		t.Fatal("daha az işli zincir eşiği geçti")
	}
	if p.Count() != 20 {
		t.Fatalf("doğrulanan başlık %d, beklenen 20", p.Count())
	}
	if len(p.window) > presyncWindow || len(p.checkpoints) > 20/int(p.every)+1 {
		t.Fatalf("bellek sınırsız: pencere %d, kontrol noktası %d", len(p.window), len(p.checkpoints))
	}
	if indexLen(dst) != n {
		t.Fatal("ön-senkronizasyon indekse yazdı")
	}
}

// Eş ön-senkronizasyonda A zincirini gösterip yeniden indirmede B'yi veremez: ilk kontrol
// noktasında reddedilir ve B'nin hiçbir bloğu saklanmaz.
func TestPresyncRejectsSubstitution(t *testing.T) {
	dst, a := forked(t, 25, 30)
	b := newTestChain(t)
	mine(t, b, 30, newKey(t).Address)
	p := presyncFrom(t, dst, a)
	if !p.Done() {
		t.Fatal("ön-senkronizasyon tamamlanmadı")
	}
	n, tip := indexLen(dst), dst.TipHash()
	if err := provenSync(t, dst, b, p); !errors.Is(err, ErrBadHeaders) {
		t.Fatalf("başka zincir: %v, beklenen ErrBadHeaders", err)
	}
	if indexLen(dst) != n || dst.TipHash() != tip {
		t.Fatal("ikame zincirin blokları saklandı")
	}
}

// Uç ön-senkronizasyonun toplam işini geçerse yetki düşer: gövde indirilmez.
func TestPresyncProofRevokedWhenTipAdvances(t *testing.T) {
	dst, src := forked(t, 25, 30)
	p := presyncFrom(t, dst, src)
	if !p.Done() {
		t.Fatal("ön-senkronizasyon tamamlanmadı")
	}
	mine(t, dst, 2, newKey(t).Address) // uç 27 blok: ön-senkronizasyonun işini (26) geçti
	s := dst.NewProvenSync(p, testWindow)
	if _, err := s.Add(src.HeadersAfter(dst.Locator(), testBatch)); err != nil {
		t.Fatal(err)
	}
	if s.MoreWork() || len(s.Missing()) != 0 {
		t.Fatal("geçersizleşen ön-senkronizasyon gövde yetkilendirdi")
	}
}
