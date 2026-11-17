package core

import (
	"errors"
	"testing"
)

// Üretimde yeniden gönderim seyreltmesi blok aralığı kadardır (test yardımcıları kapatır).
func TestRetryDeferDefault(t *testing.T) {
	bc, err := NewBlockchain(t.TempDir(), testParams)
	if err != nil {
		t.Fatal(err)
	}
	defer bc.Close()
	if bc.retryDefer <= 0 || bc.retryDefer != testParams.TargetSpacing {
		t.Fatalf("seyreltme %d sn, beklenen TargetSpacing (%d sn)", bc.retryDefer, testParams.TargetSpacing)
	}
}

// Düğüm gerideyken saklı bloğun yeniden gönderimi, son başarısız denemeden sonra retryDefer
// saniye içinde geçişi yeniden denemez (dalın gövdeleri yeniden okunmaz): ErrRetryDeferred yerel
// hatadır, kayıt değişmez. Yeni saklanan blok her zaman denenir; süre dolunca ya da saat geri
// gidince yeniden gönderim de dener. Onarımdan sonra geçiş en geç süre dolunca olur.
func TestRetryDeferredWhileBehind(t *testing.T) {
	bc := newTestChain(t)
	side, m1, saved := corruptSide(t, bc, 4)
	clock := bc.now()
	bc.now = func() int64 { return clock }
	bc.retryDefer = bc.params.TargetSpacing

	// İki yeni blok aynı anda: ikisi de denenir (ertelenmez).
	acceptStuck(t, bc, side[1], side[2])
	if bc.behind != side[2].Hash || bc.behindTriedAt != clock {
		t.Fatalf("kayıt %s, deneme zamanı %d", bc.behind, bc.behindTriedAt)
	}

	resend := func(want string) {
		t.Helper()
		err := bc.AcceptBlock(side[2])
		deferred := errors.Is(err, ErrRetryDeferred)
		switch want {
		case "ertelendi":
			if !deferred || errors.Is(err, ErrBlockDataMissing) || !IsLocalDataErr(err) {
				t.Fatalf("yeniden gönderim: %v, beklenen erteleme", err)
			}
		case "denendi":
			if deferred || err == nil {
				t.Fatalf("yeniden gönderim: %v, beklenen başarısız deneme", err)
			}
		case "bağlandı":
			if err != nil {
				t.Fatalf("yeniden gönderim: %v, beklenen geçiş", err)
			}
		}
	}

	resend("ertelendi")
	clock += bc.retryDefer - 1
	resend("ertelendi")
	if bc.behind != side[2].Hash || tipHash(t, bc) != m1.Hash {
		t.Fatalf("erteleme kaydı ya da ucu değiştirdi: kayıt %s", bc.behind)
	}
	clock++
	resend("denendi")
	if bc.behindTriedAt != clock {
		t.Fatalf("deneme zamanı %d, beklenen %d", bc.behindTriedAt, clock)
	}

	// Saat geri gitti: geçen süre negatif, ertelenmez.
	clock -= 1000
	resend("denendi")

	// Onarımdan sonra süre içindeki yeniden gönderim ertelenir, süre dolunca geçiş olur.
	clock += 5000
	resend("denendi")
	if err := bc.db.Put([]byte(side[0].Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	resend("ertelendi")
	clock += bc.retryDefer
	resend("bağlandı")
	if tipHash(t, bc) != side[2].Hash || bc.behind != "" || bc.BehindBlocks() != 0 {
		t.Fatalf("uç %s, kayıt %s, açık %d", tipHash(t, bc), bc.behind, bc.BehindBlocks())
	}

	// Düğüm geride değilken seyreltme yoktur.
	mustAccept(t, bc, side[3])
	noInvalid(t, bc)
}

// Düğüm gerideyken geçiş kendiliğinden yeniden denenir (RetryBehind): kayıt yoksa bir şey
// yapılmaz; kayıttaki hedef retryDefer içinde denendiyse beklenir (deneme zamanı değişmez);
// süre dolunca denenir. Başka bir başarısız dalın denemesi kendiliğinden denemeyi erteletemez
// (yeniden gönderim seyreltmesi ortak kalır). Onarımdan sonra geçiş yeniden gönderim ya da yeni
// blok olmadan süre dolunca olur ve kayıt silinir.
func TestRetryBehindAutomatic(t *testing.T) {
	bc := newTestChain(t)
	side, m1, saved := corruptSide(t, bc, 4)
	g := genesisOf(t, bc)
	other := extend(t, bc, g, 2, newKey(t).Address) // ikinci başarısız dal: uçtan ağır, kayıttan hafif
	mustAccept(t, bc, other[0])
	rewriteBody(t, bc, other[0].Hash, func(b *Block) { b.Transactions[0].Amount++ })
	clock := bc.now()
	bc.now = func() int64 { return clock }
	bc.retryDefer = bc.params.TargetSpacing

	if err := bc.RetryBehind(); err != nil || bc.behindTriedAt != 0 || tipHash(t, bc) != m1.Hash {
		t.Fatalf("kayıt yokken: %v, deneme zamanı %d", err, bc.behindTriedAt)
	}

	acceptStuck(t, bc, side[1], side[2])
	auto := func(want error) {
		t.Helper()
		if err := bc.RetryBehind(); !errors.Is(err, want) {
			t.Fatalf("kendiliğinden deneme: %v, beklenen %v", err, want)
		}
	}
	auto(ErrRetryDeferred)
	clock += bc.retryDefer - 1
	auto(ErrRetryDeferred)
	if bc.behindTriedAt != clock-(bc.retryDefer-1) || bc.behindTriedFor != side[2].Hash {
		t.Fatalf("bekleme deneme zamanını değiştirdi: %d", bc.behindTriedAt)
	}
	clock++
	auto(ErrBlockDataMissing)
	if bc.behindTriedAt != clock {
		t.Fatalf("deneme zamanı %d, beklenen %d", bc.behindTriedAt, clock)
	}

	// Süre dolunca öteki dal denenir; kayıt değişmez, kendiliğinden deneme ertelenmez, yeniden
	// gönderim ertelenir.
	clock += bc.retryDefer
	acceptStuck(t, bc, other[1])
	if bc.behind != side[2].Hash || bc.behindTriedFor != other[1].Hash {
		t.Fatalf("kayıt %s, son deneme %s", bc.behind, bc.behindTriedFor)
	}
	if err := bc.AcceptBlock(side[2]); !errors.Is(err, ErrRetryDeferred) {
		t.Fatalf("yeniden gönderim: %v, beklenen erteleme", err)
	}
	auto(ErrBlockDataMissing)

	// Onarım: süre içinde beklenir, süre dolunca yeniden gönderimsiz geçiş olur.
	if err := bc.db.Put([]byte(side[0].Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	auto(ErrRetryDeferred)
	clock += bc.retryDefer
	if err := bc.RetryBehind(); err != nil {
		t.Fatalf("onarımdan sonra: %v", err)
	}
	if tipHash(t, bc) != side[2].Hash || bc.behind != "" || bc.BehindBlocks() != 0 {
		t.Fatalf("uç %s, kayıt %s, açık %d", tipHash(t, bc), bc.behind, bc.BehindBlocks())
	}
	if err := bc.RetryBehind(); err != nil {
		t.Fatalf("kayıt silindikten sonra: %v", err)
	}
	noInvalid(t, bc)
}
