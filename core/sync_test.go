package core

import (
	"errors"
	"testing"
)

// peerChain a ile aynı saati paylaşan ikinci bir düğümdür (eşin zinciri).
func peerChain(t *testing.T, a *Blockchain) *Blockchain {
	t.Helper()
	b := newTestChain(t)
	b.now = a.now
	return b
}

func mine(t *testing.T, bc *Blockchain, n int, miner string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := bc.AddBlock(nil, miner); err != nil {
			t.Fatal(err)
		}
	}
}

// syncFrom src'nin zincirini dst'ye başlık öncelikli olarak aktarır (ağ katmanının yaptığının
// aynısı): başlık yanıtları batch'lerle alınır, iş ucunkini aşarsa gövdeler indirilir.
func syncFrom(t *testing.T, dst, src *Blockchain, batch int) error {
	t.Helper()
	return syncLimited(t, dst, src, batch, MaxSyncHeaders)
}

func syncLimited(t *testing.T, dst, src *Blockchain, batch, limit int) error {
	t.Helper()
	s := dst.NewHeaderSync()
	s.limit = limit
	loc := dst.Locator()
	for {
		hs := src.HeadersAfter(loc, batch)
		if len(hs) == 0 {
			break
		}
		if _, err := s.Add(hs); err != nil {
			if errors.Is(err, ErrSyncLimit) && s.Full() {
				break // ağ katmanı gibi: eldekileri indir, kalanı sonraki tur
			}
			return err
		}
		if len(hs) < batch {
			break
		}
		loc = append([]string{s.Last()}, dst.Locator()...)
	}
	if !s.MoreWork() {
		return nil
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
	return nil
}

// Geride kalan düğüm eşinin zincirine yetişir; durum eşinkiyle aynıdır.
func TestHeaderSyncCatchesUp(t *testing.T) {
	a := newTestChain(t)
	b := peerChain(t, a)
	mine(t, b, 25, newKey(t).Address)
	if err := syncFrom(t, a, b, 7); err != nil { // birden çok başlık yanıtı
		t.Fatal(err)
	}
	if tipHash(t, a) != tipHash(t, b) {
		t.Fatal("düğüm eşine yetişmedi")
	}
	assertSameState(t, a, b)
	if err := syncFrom(t, a, b, 7); err != nil {
		t.Fatalf("senkron düğümde ikinci tur: %v", err)
	}
}

// MaxSideForkDepth'ten derin çatallanan, daha çok iş taşıyan zincir başlıklar üzerinden
// benimsenir: tek tek gönderilen bloklar yan zincir sınırına takılırdı.
func TestHeaderSyncAdoptsDeepFork(t *testing.T) {
	a := newTestChain(t)
	b := peerChain(t, a)
	mine(t, a, MaxSideForkDepth+2, newKey(t).Address)
	mine(t, b, MaxSideForkDepth+4, newKey(t).Address)

	first, err := b.BlockByHash(b.main[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := a.AcceptBlock(first); !errors.Is(err, ErrSideChainLimit) {
		t.Fatalf("başlıksız derin yan blok: %v, beklenen ErrSideChainLimit", err)
	}
	if err := syncFrom(t, a, b, MaxHeadersPerMsg); err != nil {
		t.Fatal(err)
	}
	if tipHash(t, a) != tipHash(t, b) || !a.DeepReorgAlarm() {
		t.Fatal("derin çatal benimsenmedi ya da alarm verilmedi")
	}
	assertSameState(t, a, b)
	if len(a.syncAllow) != 0 {
		t.Fatalf("indirilen bloklar izin kümesinde kaldı: %d", len(a.syncAllow))
	}
}

// Daha az ya da eşit iş taşıyan başlık zinciri indirme ve sınır izni üretmez.
func TestHeaderSyncLessWorkIgnored(t *testing.T) {
	a := newTestChain(t)
	b := peerChain(t, a)
	mine(t, a, 6, newKey(t).Address)
	mine(t, b, 6, newKey(t).Address)
	s := a.NewHeaderSync()
	if _, err := s.Add(b.HeadersAfter(a.Locator(), MaxHeadersPerMsg)); err != nil {
		t.Fatal(err)
	}
	if s.MoreWork() || s.Missing() != nil || len(a.syncAllow) != 0 {
		t.Fatal("eşit işli başlık zinciri indirme izni verdi")
	}
}

// Geçersiz başlıklar reddedilir; gelecek tarihli başlık geçicidir ve öncesi oturumda kalır.
func TestHeaderSyncRejects(t *testing.T) {
	a := newTestChain(t)
	b := peerChain(t, a)
	mine(t, b, 5, newKey(t).Address)
	good := b.HeadersAfter(a.Locator(), MaxHeadersPerMsg)

	mut := func(f func(hs []BlockHeader)) []BlockHeader {
		hs := append([]BlockHeader(nil), good...)
		f(hs)
		return hs
	}
	cases := map[string]struct {
		hs   []BlockHeader
		want error
	}{
		"boş":            {nil, ErrBadHeaders},
		"bilinmeyen ata": {good[1:], ErrBadPrevHash},
		"kesintili":      {mut(func(hs []BlockHeader) { hs[2] = hs[3] }), ErrBadHeaders},
		"özet uyuşmuyor": {mut(func(hs []BlockHeader) { hs[0].Nonce++ }), ErrBadHash},
		"kanonik değil":  {mut(func(hs []BlockHeader) { hs[0].MerkleRoot = "AB" }), ErrBadHash},
		"zorluk":         {mut(func(hs []BlockHeader) { hs[0].Bits = easierThanLimitBits() }), ErrBadDifficulty},
		"sürüm":          {mut(func(hs []BlockHeader) { hs[0].Version = 2 }), ErrBadVersion},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := a.NewHeaderSync().Add(c.hs); !errors.Is(err, c.want) {
				t.Fatalf("hata %v, beklenen %v", err, c.want)
			}
		})
	}

	// Saat geride: üçüncü başlık FTL'i aşıyor. İlk ikisi oturumda kalır, hiçbir şey
	// geçersiz sayılmaz; saat yetişince aynı başlıklar kabul edilir.
	clock := fixedClock(a, good[2].Timestamp-MaxFutureDrift-1)
	s := a.NewHeaderSync()
	n, err := s.Add(good)
	if !errors.Is(err, ErrTimestampFuture) || n != 2 || s.Len() != 2 {
		t.Fatalf("gelecek başlık: n=%d len=%d err=%v", n, s.Len(), err)
	}
	*clock = good[len(good)-1].Timestamp
	if _, err := a.NewHeaderSync().Add(good); err != nil {
		t.Fatalf("saat yetişince: %v", err)
	}
}

// Ağdan gelen özetle blok olmayan anahtarlar okunamaz; özet biçimi denetlenir.
func TestBlockByHashOnlyIndexedBlocks(t *testing.T) {
	bc := newTestChain(t)
	for _, k := range []string{keyLastHash, keySchema, keyGenesis, prefixBalance + "x", prefixIndex + bc.main[0], ""} {
		if _, err := bc.BlockByHash(k); !errors.Is(err, ErrUnknownBlock) {
			t.Fatalf("%q okundu: %v", k, err)
		}
	}
	if _, err := bc.BlockByHash(bc.main[0]); err != nil {
		t.Fatal(err)
	}
	for s, ok := range map[string]bool{bc.main[0]: true, "lh": false, "AB" + bc.main[0][2:]: false, bc.main[0] + "0": false} {
		if IsHashHex(s) != ok {
			t.Fatalf("IsHashHex(%q) != %v", s, ok)
		}
	}
}

func TestLocatorAndConfirmations(t *testing.T) {
	bc := newTestChain(t)
	mine(t, bc, 300, newKey(t).Address)
	loc := bc.Locator()
	if len(loc) > MaxLocatorHashes || loc[0] != bc.tip.Hash || loc[len(loc)-1] != bc.main[0] {
		t.Fatalf("konum belirleyici: %d özet", len(loc))
	}
	if hs := bc.HeadersAfter([]string{bc.main[290], "yok"}, MaxHeadersPerMsg); len(hs) != 10 || hs[0].Index != 291 {
		t.Fatalf("başlıklar: %d", len(hs))
	}
	if n, alarm := bc.Confirmations(bc.tip.Hash); n != 1 || alarm {
		t.Fatalf("uç onayı %d", n)
	}
	if n, _ := bc.Confirmations(bc.main[1]); n != 300 {
		t.Fatalf("ilk blok onayı %d", n)
	}
	side := blockOn(t, bc, bc.mustMainBlock(t, 299), newKey(t).Address)
	mustAccept(t, bc, side)
	if n, _ := bc.Confirmations(side.Hash); n != 0 {
		t.Fatalf("yan blok onayı %d", n)
	}
	if n, _ := bc.Confirmations("yok"); n != 0 {
		t.Fatal("bilinmeyen blok onaylı")
	}
}

// Oturum başlık sınırından daha geride olan düğüm turlar halinde yetişir: her tur eldeki
// başlıkların gövdelerini indirir, sonraki tur onların üzerinden devam eder.
func TestHeaderSyncLimitContinuesNextRound(t *testing.T) {
	a := newTestChain(t)
	b := peerChain(t, a)
	mine(t, b, 12, newKey(t).Address)
	for round := 0; round < 3; round++ {
		if err := syncLimited(t, a, b, 4, 5); err != nil {
			t.Fatalf("tur %d: %v", round, err)
		}
	}
	if tipHash(t, a) != tipHash(t, b) {
		t.Fatalf("üç turda yetişilmedi: yükseklik %d", a.tip.Height)
	}
	assertSameState(t, a, b)
}

// Bilinen özeti taşıyan ama indeks kaydıyla tutarsız başlık reddedilir; bilinen başlıklarla
// döngü kurulamaz (sonsuz senkronizasyon turu).
func TestHeaderSyncKnownHeaderMustMatchIndex(t *testing.T) {
	a := newTestChain(t)
	g := a.main[0]
	loop := make([]BlockHeader, 5)
	for i := range loop {
		loop[i] = BlockHeader{PrevHash: g, Hash: g, Index: 1}
	}
	if _, err := a.NewHeaderSync().Add(loop); !errors.Is(err, ErrBadHeaders) {
		t.Fatalf("genesis döngüsü: %v", err)
	}
	mine(t, a, 3, newKey(t).Address)
	known := a.HeadersAfter([]string{g}, MaxHeadersPerMsg)
	known[1].Index = 7 // özet aynı, yükseklik yalan
	if _, err := a.NewHeaderSync().Add(known); !errors.Is(err, ErrBadHeaders) {
		t.Fatalf("tutarsız bilinen başlık: %v", err)
	}
}
