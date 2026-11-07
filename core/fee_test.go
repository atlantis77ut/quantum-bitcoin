package core

import (
	"errors"
	"math"
	"testing"
)

// Ücret madenciye gider; göndericiden tutar + ücret düşülür; toplam arz korunur.
func TestFeeGoesToMiner(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	fee := 3 * Coin / 10 // 0.3 QBTC: alt birim kesirli değerleri taşıyabilmeli
	tx := signedTxFee(t, alice, bob.Address, 10*Coin, fee, 1)
	b, err := bc.AddBlock([]Transaction{tx}, miner.Address)
	if err != nil {
		t.Fatal(err)
	}
	if b.Transactions[0].Amount != BlockReward+fee {
		t.Fatalf("coinbase %d, beklenen ödül + ücret", b.Transactions[0].Amount)
	}
	if got := mustBalance(t, bc, alice.Address); got != BlockReward-10*Coin-fee {
		t.Fatalf("alice bakiyesi %d", got)
	}
	if got := mustBalance(t, bc, bob.Address); got != 10*Coin {
		t.Fatalf("bob bakiyesi %d", got)
	}
	if got := mustBalance(t, bc, miner.Address); got != BlockReward+fee {
		t.Fatalf("madenci bakiyesi %d", got)
	}
	total := mustBalance(t, bc, alice.Address) + mustBalance(t, bc, bob.Address) + mustBalance(t, bc, miner.Address)
	if total != 2*BlockReward {
		t.Fatalf("arz korunmadı: %d, beklenen %d", total, 2*BlockReward)
	}
}

// Ağdan gelen, ücretleri doğru talep eden blok kabul edilir.
func TestAcceptsBlockWithCorrectFeeClaim(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	tx := signedTxFee(t, alice, bob.Address, Coin, 5, 1)
	b := craftBlock(t, bc, []Transaction{coinbaseWithFees(miner.Address, tx), tx})
	if err := bc.AcceptBlock(b); err != nil {
		t.Fatalf("geçerli blok reddedildi: %v", err)
	}
	if got := mustBalance(t, bc, miner.Address); got != BlockReward+5 {
		t.Fatalf("madenci bakiyesi %d", got)
	}
}

func TestRejectsNegativeFee(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	for _, fee := range []int64{-1, math.MinInt64} {
		tx := signedTxFee(t, alice, bob.Address, Coin, fee, 1)
		if err := tx.VerifyStateless(testParams); !errors.Is(err, ErrNegativeFee) {
			t.Fatalf("ücret %d: hata %v", fee, err)
		}
		b := craftBlock(t, bc, []Transaction{coinbaseWithFees(miner.Address, tx), tx})
		if err := bc.AcceptBlock(b); !errors.Is(err, ErrNegativeFee) {
			t.Fatalf("ücret %d: blok hatası %v", fee, err)
		}
	}
	if mustBalance(t, bc, alice.Address) != BlockReward || mustBalance(t, bc, miner.Address) != 0 {
		t.Fatal("bakiyeler değişti")
	}
}

func TestRejectsCoinbaseFeeMisclaim(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tx := signedTxFee(t, alice, bob.Address, Coin, Coin, 1)

	over := coinbaseWithFees(miner.Address, tx)
	over.Amount++
	under := coinbaseTo(miner.Address) // ücreti talep etmiyor: tam eşitlik kuralı
	withFee := coinbaseWithFees(miner.Address, tx)
	withFee.Fee = 1 // coinbase ücret alanı taşıyamaz

	for name, cb := range map[string]Transaction{"fazla talep": over, "eksik talep": under, "coinbase ücreti": withFee} {
		t.Run(name, func(t *testing.T) {
			b := craftBlock(t, bc, []Transaction{cb, tx})
			if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadCoinbase) {
				t.Fatalf("hata %v", err)
			}
		})
	}
	if mustBalance(t, bc, miner.Address) != 0 {
		t.Fatal("madenci bakiyesi değişti")
	}
}

// Gönderici tutarı karşılayabiliyor ama tutar + ücreti karşılayamıyor.
func TestRejectsWhenFeeExceedsBalance(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	tx := signedTxFee(t, alice, bob.Address, BlockReward, 1, 1)
	b := craftBlock(t, bc, []Transaction{coinbaseWithFees(miner.Address, tx), tx})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("hata %v", err)
	}
	mined, err := bc.AddBlock([]Transaction{tx}, miner.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(mined.Transactions) != 1 || mined.Transactions[0].Amount != BlockReward {
		t.Fatal("karşılanamayan işlem yerel bloğa alındı")
	}
}

// Aynı blokta iki işlem tek tek karşılanabiliyor, ama ücretlerle birlikte bakiyeyi aşıyor.
func TestRejectsDoubleSpendPushedOverByFees(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, carol, miner := newKey(t), newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1) // 50 QBTC

	tx1 := signedTxFee(t, alice, bob.Address, 25*Coin, Coin, 1)   // 26
	tx2 := signedTxFee(t, alice, carol.Address, 24*Coin, Coin, 2) // 25 → toplam 51
	b := craftBlock(t, bc, []Transaction{coinbaseWithFees(miner.Address, tx1, tx2), tx1, tx2})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("hata %v", err)
	}
	if mustBalance(t, bc, alice.Address) != BlockReward || mustBalance(t, bc, bob.Address) != 0 {
		t.Fatal("bakiyeler değişti")
	}
}

// Ücret toplamı int64'ü aşarsa blok reddedilir (sarmalama ile para basılamaz).
func TestRejectsFeeSumOverflow(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	half := int64(math.MaxInt64 / 2)
	tx1 := signedTxFee(t, alice, bob.Address, 1, half, 1)
	tx2 := signedTxFee(t, alice, bob.Address, 1, half, 2)
	cb := coinbaseTo(miner.Address)
	cb.Amount = BlockReward // taşma kontrolü olmasa ErrBadCoinbase dönerdi; ErrOverflow beklenir
	b := craftBlock(t, bc, []Transaction{cb, tx1, tx2})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrOverflow) {
		t.Fatalf("hata %v", err)
	}
	if _, err := coinbaseValue([]Transaction{tx1, tx2}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("coinbaseValue: %v", err)
	}
}

func TestRejectsAmountPlusFeeOverflow(t *testing.T) {
	bc := newTestChain(t)
	v := newStateView(bc)
	v.balances["s"] = math.MaxInt64
	tx := &Transaction{Sender: "s", Receiver: "r", Amount: math.MaxInt64, Fee: 1, Nonce: 1}
	if err := v.apply(tx); !errors.Is(err, ErrOverflow) {
		t.Fatalf("hata %v", err)
	}
	if v.balances["s"] != math.MaxInt64 || v.balances["r"] != 0 {
		t.Fatal("başarısız apply durumu değiştirdi")
	}
}

func TestFeeIsSigned(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	tx := signedTxFee(t, alice, bob.Address, Coin, 10, 1)
	tx.Fee = 0 // imzadan sonra ücret düşürüldü
	if err := tx.VerifyStateless(testParams); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("hata %v", err)
	}
}

func TestFormatAmount(t *testing.T) {
	cases := []struct {
		v    int64
		want string
	}{
		{0, "0.00000000"},
		{1, "0.00000001"},
		{Coin, "1.00000000"},
		{BlockReward, "50.00000000"},
		{-Coin / 2, "-0.50000000"},
		{math.MaxInt64, "92233720368.54775807"},
		{math.MinInt64, "-92233720368.54775808"},
	}
	for _, c := range cases {
		if got := FormatAmount(c.v); got != c.want {
			t.Errorf("FormatAmount(%d) = %s, beklenen %s", c.v, got, c.want)
		}
	}
}
