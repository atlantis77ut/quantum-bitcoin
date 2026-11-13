package core

import (
	"errors"
	"math"
	"testing"
)

// CheckOrphan ucuz süzgeçleri imzadan önce uygular; yalnızca boşlukta, sınır içinde, fonlu ve
// imzası geçerli işlemi tamponlanabilir sayar; öncüller geldiyse ErrNotOrphan döner.
func TestMempoolCheckOrphan(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, bob, carol := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tip := tipBlock(t, bc).Index
	exp := tip + bc.params.DefaultTxLifetime()

	gap := mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 3, exp)
	if _, err := m.Add(gap); !errors.Is(err, ErrNonceGap) {
		t.Fatalf("Add: %v, beklenen ErrNonceGap", err)
	}
	if err := m.CheckOrphan(gap); err != nil {
		t.Fatalf("geçerli yetim: %v", err)
	}

	badSig := gap
	badSig.Amount = 2 * Coin
	for _, c := range []struct {
		name string
		tx   Transaction
		want error
	}{
		{"sahte imza", badSig, ErrBadSignature},
		{"fonsuz gönderen", mpTxExp(t, carol, bob.Address, Coin, MempoolMinFee, 3, exp), ErrInsufficientBal},
		{"tutar + ücret taşar", mpTxExp(t, alice, bob.Address, math.MaxInt64, MempoolMinFee, 3, exp), ErrInsufficientBal},
		{"düşük ücret", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee-1, 3, exp), ErrFeeTooLow},
		{"gönderen sınırının ötesi", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, MempoolMaxPerSender+1, exp), ErrNonceGap},
		{"onaylı nonce", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 0, exp), ErrBadNonce},
		{"sıradaki nonce", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, exp), ErrNotOrphan},
		{"süre K'nin altında", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 3, tip+MempoolExpiryMargin-1), ErrExpiryTooSoon},
		{"süre ufkun ötesinde", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 3, tip+bc.params.MaxTxLifetime()+1), ErrExpiryTooFar},
	} {
		if err := m.CheckOrphan(c.tx); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, beklenen %v", c.name, err, c.want)
		}
	}

	// Öncüller havuza girince işlem artık yetim değildir; sınır havuzdaki zincire göre değil
	// onaylı nonce'a göredir.
	mustAdd(t, m, mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, exp))
	mustAdd(t, m, mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 2, exp))
	if err := m.CheckOrphan(gap); !errors.Is(err, ErrNotOrphan) {
		t.Fatalf("öncüller havuzdayken: %v", err)
	}
	mustAdd(t, m, gap)
	if err := m.CheckOrphan(gap); !errors.Is(err, ErrTxKnown) {
		t.Fatalf("havuzdaki işlem: %v", err)
	}
}
