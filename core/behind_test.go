package core

import (
	"errors"
	"testing"
	"time"
)

// corruptSide ana zinciri g-m1 olan düğümde g üzerine n blokluk bir yan dal kurar, ilk bloğunu
// saklayıp gövdesini bozar ve dalı, ana bloğu ve bozulan gövdenin eski baytlarını döndürür: dalın
// kalan blokları geldikçe zincire geçiş bozuk gövdede düşer.
func corruptSide(t *testing.T, bc *Blockchain, n int) (side []*Block, m1 *Block, saved []byte) {
	t.Helper()
	g := genesisOf(t, bc)
	m1 = blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, m1)
	side = extend(t, bc, g, n, newKey(t).Address)
	mustAccept(t, bc, side[0]) // eşit iş: yan blok
	saved = rewriteBody(t, bc, side[0].Hash, func(b *Block) { b.Transactions[0].Amount++ })
	return side, m1, saved
}

// acceptStuck b'nin kabulünün bozuk gövde yüzünden düştüğünü doğrular.
func acceptStuck(t *testing.T, bc *Blockchain, blocks ...*Block) {
	t.Helper()
	for _, b := range blocks {
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrBlockDataMissing) {
			t.Fatalf("blok %d: %v, beklenen bozuk gövde", b.Index, err)
		}
	}
}

// Uçtan ağır hedefe geçilemeyince geride kalma kaydı kurulur ve açık blok cinsinden ölçülür.
// Açık eşiğin (regtest: 2 blok) altındayken kazım sürer; eşiğe ulaşınca süren şablon bırakılır
// ve kazım durur. Uç yetiştikçe açık kapanır; eşit işte düğüm geride sayılmaz ama kayıt durur,
// uç kesin olarak ağır olunca silinir.
func TestBehindRecordAndMiningPause(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	side, m1, _ := corruptSide(t, bc, 4)
	if bc.BehindBlocks() != 0 || bc.MiningPaused() || bc.behind != "" {
		t.Fatal("geçiş denenmeden düğüm geride sayıldı")
	}

	// Bir blok geride: gösterge var, kazım sürer.
	acceptStuck(t, bc, side[1])
	if bc.BehindBlocks() != 1 || bc.MiningPaused() || bc.behind != side[1].Hash {
		t.Fatalf("1 blok geride: açık %d, duraklama %v, kayıt %s", bc.BehindBlocks(), bc.MiningPaused(), bc.behind)
	}
	m2, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatalf("eşiğin altında kazım durdu: %v", err)
	}
	// Eşit iş: düğüm geride değil, ama kayıt silinmez (öteki dal hâlâ geçilmemiş).
	if bc.BehindBlocks() != 0 || bc.behind != side[1].Hash {
		t.Fatalf("eşit işte: açık %d, kayıt %s", bc.BehindBlocks(), bc.behind)
	}

	// Kazım sürerken öteki dal iki blok öne geçer: süren şablon bırakılır.
	tmpl, err := bc.NewTemplate(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	fed := false
	bc.mineHook = func() {
		if !fed {
			fed = true
			acceptStuck(t, bc, side[2], side[3])
		}
	}
	_, err = bc.Mine(tmpl, nil)
	bc.mineHook = nil
	if !errors.Is(err, ErrMiningPaused) {
		t.Fatalf("eşikte süren kazım: %v, beklenen duraklama", err)
	}
	if bc.BehindBlocks() != 2 || !bc.MiningPaused() || bc.behind != side[3].Hash {
		t.Fatalf("2 blok geride: açık %d, duraklama %v, kayıt %s", bc.BehindBlocks(), bc.MiningPaused(), bc.behind)
	}
	if _, err := bc.AddBlock(nil, miner); !errors.Is(err, ErrMiningPaused) {
		t.Fatalf("duraklamada kazım: %v", err)
	}
	if tipHash(t, bc) != m2.Hash {
		t.Fatal("duraklamada uç değişti")
	}
	info, err := bc.MiningInfo(1)
	if err != nil || info.BehindBlocks != 2 || !info.MiningPaused {
		t.Fatalf("kazım özeti %+v, %v", info, err)
	}
	if c, err := bc.ConfirmationOf(m1.Hash); err != nil || c.BehindBlocks != 2 || c.Blocks != 2 {
		t.Fatalf("onay bilgisi %+v, %v", c, err)
	}

	// Ağdan kendi dalımıza gelen blok açığı eşiğin altına indirir: kazım sürer.
	m3 := blockOn(t, bc, m2, miner)
	mustAccept(t, bc, m3)
	if bc.BehindBlocks() != 1 || bc.MiningPaused() {
		t.Fatalf("açık kapanırken: açık %d, duraklama %v", bc.BehindBlocks(), bc.MiningPaused())
	}
	if _, err := bc.AddBlock(nil, miner); err != nil { // eşit iş
		t.Fatalf("eşiğin altında kazım durdu: %v", err)
	}
	if bc.BehindBlocks() != 0 || bc.behind != side[3].Hash {
		t.Fatalf("eşit işte: açık %d, kayıt %s", bc.BehindBlocks(), bc.behind)
	}
	if _, err := bc.AddBlock(nil, miner); err != nil { // uç kesin olarak ağır
		t.Fatal(err)
	}
	if bc.behind != "" || bc.BehindBlocks() != 0 {
		t.Fatalf("uç ağır olunca kayıt silinmedi: %s", bc.behind)
	}
	noInvalid(t, bc)
}

// Veri düzelip hedefe geçilince kayıt silinir ve kazım sürer.
func TestBehindClearedByRepair(t *testing.T) {
	bc := newTestChain(t)
	side, _, saved := corruptSide(t, bc, 3)
	acceptStuck(t, bc, side[1:]...)
	if bc.BehindBlocks() != 2 || !bc.MiningPaused() {
		t.Fatalf("açık %d, duraklama %v", bc.BehindBlocks(), bc.MiningPaused())
	}
	if err := bc.db.Put([]byte(side[0].Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.AcceptBlock(side[2]); err != nil {
		t.Fatalf("gövde düzeldikten sonra: %v", err)
	}
	if tipHash(t, bc) != side[2].Hash || bc.behind != "" || bc.BehindBlocks() != 0 || bc.MiningPaused() {
		t.Fatalf("hedefe geçilince kayıt silinmedi: kayıt %s, açık %d", bc.behind, bc.BehindBlocks())
	}
	if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
		t.Fatalf("duraklama bittikten sonra kazım: %v", err)
	}
}

// Kayıt nedeni ayırt etmez: indeks tutmazlığı (alarmsız bozuk durum) ve kalıcı olmayan kural
// hatası (saat geri gitti) da düğümü geride bırakır. Kalıcı kural ihlalinde hedef geçersiz
// işaretlendiğinden kayıt oluşmaz.
func TestBehindCauses(t *testing.T) {
	t.Run("indeks tutmazlığı", func(t *testing.T) {
		bc := newTestChain(t)
		g := genesisOf(t, bc)
		m1 := blockOn(t, bc, g, newKey(t).Address)
		mustAccept(t, bc, m1)
		s1 := blockOn(t, bc, g, newKey(t).Address)
		s2 := blockOn(t, bc, s1, newKey(t).Address)
		bc.mu.Lock()
		storeOnly(t, bc, s1)
		storeOnly(t, bc, s2)
		e := bc.index[s2.Hash]
		e.Prev = m1.Hash // yerel indeks bozulması: doğru yükseklikte yanlış ebeveyn
		err := bc.activateLocked(e)
		bc.mu.Unlock()
		if !errors.Is(err, ErrCorruptState) || errors.Is(err, ErrBlockDataMissing) {
			t.Fatalf("hata %v, beklenen bozuk durum", err)
		}
		if bc.BehindBlocks() != 1 || bc.behind != s2.Hash || bc.MissingDataAlarm() {
			t.Fatalf("açık %d, kayıt %s, alarm %v", bc.BehindBlocks(), bc.behind, bc.MissingDataAlarm())
		}
		bc.mu.Lock()
		e.Prev = s1.Hash
		err = bc.activateLocked(e)
		bc.mu.Unlock()
		if err != nil || tipHash(t, bc) != s2.Hash || bc.behind != "" {
			t.Fatalf("indeks düzeldikten sonra: %v, kayıt %s", err, bc.behind)
		}
	})

	t.Run("saat geri gitti", func(t *testing.T) {
		bc := newTestChain(t)
		g := genesisOf(t, bc)
		m1 := blockOn(t, bc, g, newKey(t).Address)
		mustAccept(t, bc, m1)
		s1 := blockOn(t, bc, g, newKey(t).Address)
		s2 := blockOn(t, bc, s1, newKey(t).Address)
		bc.mu.Lock()
		storeOnly(t, bc, s1)
		storeOnly(t, bc, s2)
		bc.mu.Unlock()
		clock := bc.now
		bc.now = func() int64 { return s1.Timestamp - MaxFutureDrift - 1 } // saklı bloklar artık gelecekte
		bc.mu.Lock()
		err := bc.activateLocked(bc.index[s2.Hash])
		bc.mu.Unlock()
		bc.now = clock
		if !errors.Is(err, ErrTimestampFuture) {
			t.Fatalf("hata %v, beklenen gelecek tarihli blok", err)
		}
		if tipHash(t, bc) != m1.Hash || bc.BehindBlocks() != 1 || bc.behind != s2.Hash {
			t.Fatalf("açık %d, kayıt %s", bc.BehindBlocks(), bc.behind)
		}
		noInvalid(t, bc)
		// Saat düzelince blok yeniden gelince geçiş olur.
		if err := bc.AcceptBlock(s2); err != nil || tipHash(t, bc) != s2.Hash || bc.behind != "" {
			t.Fatalf("saat düzeldikten sonra: %v, kayıt %s", err, bc.behind)
		}
	})

	t.Run("kalıcı kural ihlali", func(t *testing.T) {
		bc := newTestChain(t)
		g := genesisOf(t, bc)
		mustAccept(t, bc, blockOn(t, bc, g, newKey(t).Address))
		// Bakiyesi olmayan hesabın harcaması: durum kontrolünde kalıcı olarak düşer.
		s1 := blockOn(t, bc, g, newKey(t).Address, signedTx(t, newKey(t), newKey(t).Address, Coin, 1))
		mustAccept(t, bc, s1) // eşit iş: yan blok, durum kontrolü geçişte
		s2 := blockOn(t, bc, s1, newKey(t).Address)
		if err := bc.AcceptBlock(s2); !errors.Is(err, ErrInsufficientBal) {
			t.Fatalf("hata %v, beklenen yetersiz bakiye", err)
		}
		if bc.index[s2.Hash].Status != statusInvalid || bc.behind != "" || bc.BehindBlocks() != 0 {
			t.Fatalf("geçersiz hedef geride bıraktı: kayıt %s, açık %d", bc.behind, bc.BehindBlocks())
		}
	})
}

// Kayıtlı hedef sonradan geçersiz işaretlenirse kayıt silinir: düğüm geride değildir.
func TestBehindClearedWhenTargetInvalid(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	main := extend(t, bc, g, 2, newKey(t).Address)
	mustAccept(t, bc, main...)
	s1 := blockOn(t, bc, g, newKey(t).Address)
	s2 := blockOn(t, bc, s1, newKey(t).Address, signedTx(t, newKey(t), newKey(t).Address, Coin, 1)) // bakiyesiz harcama
	s3 := blockOn(t, bc, s2, newKey(t).Address)
	mustAccept(t, bc, s1, s2) // uçtan ağır değil: yan bloklar
	saved := rewriteBody(t, bc, s1.Hash, func(b *Block) { b.Transactions[0].Amount++ })
	acceptStuck(t, bc, s3)
	if bc.BehindBlocks() != 1 || bc.behind != s3.Hash {
		t.Fatalf("açık %d, kayıt %s", bc.BehindBlocks(), bc.behind)
	}

	// Gövde düzelince dal denenir: s2 kalıcı olarak düşer, s2 ve s3 geçersiz işaretlenir.
	if err := bc.db.Put([]byte(s1.Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.AcceptBlock(s3); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("hata %v, beklenen yetersiz bakiye", err)
	}
	if bc.index[s3.Hash].Status != statusInvalid || bc.behind != "" || bc.BehindBlocks() != 0 || bc.MiningPaused() {
		t.Fatalf("hedef geçersiz işaretlendi ama kayıt duruyor: %s", bc.behind)
	}
	if tipHash(t, bc) != main[1].Hash {
		t.Fatal("uç değişti")
	}
}

// Kayıt yalnızca bellektedir; açılıştaki kurtarma denemeleri onu en ağır hedefle yeniden kurar.
func TestBehindRederivedOnReopen(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	// Açılıştaki kurtarma gerçek saatle çalışır (FTL): bloklar gerçek saatin gerisinde olsun.
	clock := max(time.Now().Unix()-1000, genesisOf(t, bc).Timestamp)
	bc.now = func() int64 { clock++; return clock }
	side, m1, _ := corruptSide(t, bc, 3)
	acceptStuck(t, bc, side[1:]...)
	if !bc.MiningPaused() {
		t.Fatal("eşikte kazım duraklamadı")
	}
	bc.Close()

	bc2, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc2.Close() })
	if tipHash(t, bc2) != m1.Hash || bc2.behind != side[2].Hash || bc2.BehindBlocks() != 2 || !bc2.MiningPaused() {
		t.Fatalf("yeniden açılışta: kayıt %s, açık %d, duraklama %v", bc2.behind, bc2.BehindBlocks(), bc2.MiningPaused())
	}
}

// Operatör duraklamayı kapatınca (-mine-while-behind) eşikteki düğüm kendi ucunun üstüne kazar;
// kayıt ve açık aynen görünür, kazım özeti duraklamanın kapalı olduğunu söyler. Bayrak eşiğin
// altında bir şey değiştirmez.
func TestMineWhileBehind(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	side, m1, _ := corruptSide(t, bc, 3)
	acceptStuck(t, bc, side[1:]...)
	if !bc.MiningPaused() || bc.MiningPauseOverridden() {
		t.Fatal("eşikte kazım duraklamadı")
	}

	bc.SetMineWhileBehind(true)
	if bc.MiningPaused() || !bc.MiningPauseOverridden() || bc.BehindBlocks() != 2 || bc.behind != side[2].Hash {
		t.Fatalf("bayrakla: duraklama %v, aşıldı %v, açık %d, kayıt %s",
			bc.MiningPaused(), bc.MiningPauseOverridden(), bc.BehindBlocks(), bc.behind)
	}
	info, err := bc.MiningInfo(1)
	if err != nil || info.MiningPaused || !info.MiningPauseDisabled || info.BehindBlocks != 2 {
		t.Fatalf("kazım özeti %+v, %v", info, err)
	}
	b, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatalf("bayrakla kazım durdu: %v", err)
	}
	if b.PrevHash != m1.Hash || tipHash(t, bc) != b.Hash {
		t.Fatalf("kazılan blok %s (ebeveyn %s), uç %s", b.Hash, b.PrevHash, tipHash(t, bc))
	}

	// Açık 1'e indi: eşiğin altında bayrak bir şey değiştirmez.
	if bc.BehindBlocks() != 1 || bc.MiningPauseOverridden() {
		t.Fatalf("açık %d, aşıldı %v", bc.BehindBlocks(), bc.MiningPauseOverridden())
	}
	bc.SetMineWhileBehind(false)
	if bc.MiningPaused() {
		t.Fatal("eşiğin altında kazım duraklatıldı")
	}
	if info, err := bc.MiningInfo(1); err != nil || info.MiningPauseDisabled {
		t.Fatalf("bayrak kapatıldı ama kazım özeti %+v, %v", info, err)
	}
}
