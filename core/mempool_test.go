package core

import (
	"errors"
	"math"
	"testing"

	"quantum-bitcoin/crypto"
)

// mpTxExp havuz politikasına uygun (ücretli, sınırlı süreli) imzalı bir işlemdir.
func mpTxExp(t *testing.T, from *crypto.KeyPair, to string, amount, fee int64, nonce uint64, expiry int64) Transaction {
	t.Helper()
	tx := Transaction{Receiver: to, Amount: amount, Fee: fee, ExpiryHeight: expiry, Nonce: nonce}
	if err := tx.Sign(from, testParams); err != nil {
		t.Fatal(err)
	}
	return tx
}

// mpTx varsayılan süreli (uç + 1 gün) işlemdir.
func mpTx(t *testing.T, bc *Blockchain, from *crypto.KeyPair, to string, amount, fee int64, nonce uint64) Transaction {
	t.Helper()
	return mpTxExp(t, from, to, amount, fee, nonce, tipBlock(t, bc).Index+bc.params.DefaultTxLifetime())
}

func mustAdd(t *testing.T, m *Mempool, tx Transaction) {
	t.Helper()
	if _, err := m.Add(tx); err != nil {
		t.Fatalf("%s: %v", tx.String(), err)
	}
}

func poolLen(t *testing.T, m *Mempool) int {
	t.Helper()
	n, err := m.Len()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// mineFromPool havuzdan şablon kurar, kazar ve madencinin yolundan (SubmitBlock) ekler.
func mineFromPool(t *testing.T, bc *Blockchain, m *Mempool) *Block {
	t.Helper()
	txs, err := m.Select(MaxTxPerBlock)
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := bc.NewTemplate(txs, newKey(t).Address)
	if err != nil {
		t.Fatal(err)
	}
	b, err := bc.Mine(tpl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc.SubmitBlock(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// Geçersiz ve politikaya aykırı işlemler reddedilir; sınırlar her iki yanda denenir.
func TestMempoolAdmission(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1) // 50 QBTC
	tip := tipBlock(t, bc).Index
	exp := tip + bc.params.DefaultTxLifetime()
	margin, horizon := MempoolExpiryMargin, bc.params.MaxTxLifetime()

	badSig := mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 1)
	badSig.Amount = 2 * Coin
	for _, c := range []struct {
		name string
		tx   Transaction
		want error
	}{
		{"sıfır tutar", mpTxExp(t, alice, bob.Address, 0, MempoolMinFee, 1, exp), ErrNonPositiveAmount},
		{"negatif tutar", mpTxExp(t, alice, bob.Address, -1, MempoolMinFee, 1, exp), ErrNonPositiveAmount},
		{"negatif ücret", mpTxExp(t, alice, bob.Address, Coin, -1, 1, exp), ErrNegativeFee},
		{"düşük ücret", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee-1, 1, exp), ErrFeeTooLow},
		{"tutar + ücret taşar", mpTxExp(t, alice, bob.Address, math.MaxInt64, MempoolMinFee, 1, exp), ErrOverflow},
		{"yetersiz bakiye", mpTxExp(t, alice, bob.Address, 50*Coin, MempoolMinFee, 1, exp), ErrInsufficientBal},
		{"onaylı nonce (replay)", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 0, exp), ErrBadNonce},
		{"nonce boşluğu", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 2, exp), ErrNonceGap},
		{"süre K'nin altında", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, tip+margin-1), ErrExpiryTooSoon},
		{"süre ufkun ötesinde", mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, tip+horizon+1), ErrExpiryTooFar},
		{"geçersiz imza", badSig, ErrBadSignature},
	} {
		if _, err := m.Add(c.tx); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v, beklenen %v", c.name, err, c.want)
		}
	}
	if n := poolLen(t, m); n != 0 {
		t.Fatalf("reddedilen işlemler havuza girdi: %d", n)
	}

	first := mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, tip+margin) // K sınırı: kabul
	mustAdd(t, m, first)
	if _, err := m.Add(first); !errors.Is(err, ErrTxKnown) {
		t.Fatalf("yinelenen işlem: %v", err)
	}
	mustAdd(t, m, mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 2, tip+horizon)) // ufuk sınırı: kabul
	// Kümülatif bakiye: 2 QBTC + ücretler havuzda, 48 QBTC artık yetmez.
	if _, err := m.Add(mpTx(t, bc, alice, bob.Address, 48*Coin, MempoolMinFee, 3)); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("kümülatif bakiye: %v", err)
	}
	if n := poolLen(t, m); n != 2 {
		t.Fatalf("havuzda %d işlem, beklenen 2", n)
	}
	p, err := m.Policy(alice.Address)
	if err != nil || p.NextNonce != 3 || p.MinExpiryHeight != tip+margin || p.DefaultExpiryHeight != exp {
		t.Fatalf("politika %+v, %v", p, err)
	}
}

// Şablon gönderen başına nonce sırasını korur, başlar arasında yüksek ücret önce gelir; aynı
// nonce yalnızca yeterli ücret artışıyla değişir ve şablona tek kez girer. Kazılan blok
// madencinin yolundan kabul edilir ve havuz boşalır.
func TestMempoolTemplateOrderAndReplacement(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, carol, bob := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	fund(t, bc, carol, 1)

	a1 := mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 1)
	a2 := mpTx(t, bc, alice, bob.Address, Coin, 5000, 2)
	c1 := mpTx(t, bc, carol, bob.Address, Coin, 3000, 1)
	for _, tx := range []Transaction{a1, a2, c1} {
		mustAdd(t, m, tx)
	}
	if _, err := m.Add(mpTx(t, bc, alice, bob.Address, Coin, 1999, 1)); !errors.Is(err, ErrReplaceFee) {
		t.Fatalf("yetersiz artışla yer değiştirme: %v", err)
	}
	a1x := mpTx(t, bc, alice, bob.Address, Coin, 2000, 1)
	mustAdd(t, m, a1x)

	txs, err := m.Select(MaxTxPerBlock)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{c1.ID(), a1x.ID(), a2.ID()}
	if len(txs) != len(want) {
		t.Fatalf("şablonda %d işlem, beklenen %d", len(txs), len(want))
	}
	for i := range want {
		if txs[i].ID() != want[i] {
			t.Fatalf("şablon sırası %d: %s", i, txs[i].String())
		}
	}

	b := mineFromPool(t, bc, m)
	if len(b.Transactions) != 4 {
		t.Fatalf("blokta %d işlem, beklenen coinbase + 3", len(b.Transactions))
	}
	if got := mustBalance(t, bc, bob.Address); got != 3*Coin {
		t.Fatalf("alıcı bakiyesi %d", got)
	}
	if n := poolLen(t, m); n != 0 {
		t.Fatalf("kazılan işlemler havuzda kaldı: %d", n)
	}
}

// Yer değiştiren işlem bakiyeyi tüketirse gönderenin sonraki işlemleri düşer.
func TestMempoolReplacementDropsUnaffordable(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1) // 50 QBTC
	mustAdd(t, m, mpTx(t, bc, alice, bob.Address, 20*Coin, MempoolMinFee, 1))
	mustAdd(t, m, mpTx(t, bc, alice, bob.Address, 20*Coin, MempoolMinFee, 2))
	mustAdd(t, m, mpTx(t, bc, alice, bob.Address, 40*Coin, 2*MempoolMinFee, 1))
	if n := poolLen(t, m); n != 1 {
		t.Fatalf("havuzda %d işlem, beklenen 1", n)
	}
	if p, err := m.Policy(alice.Address); err != nil || p.NextNonce != 2 {
		t.Fatalf("politika %+v, %v", p, err)
	}
}

// Süresi dolan işlem uç+1'e göre düşer ve gönderenin sonraki işlemleri de onunla düşer.
func TestMempoolExpiryDropsDependents(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tip := tipBlock(t, bc).Index
	mustAdd(t, m, mpTxExp(t, alice, bob.Address, Coin, MempoolMinFee, 1, tip+MempoolExpiryMargin))
	mustAdd(t, m, mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 2))

	miner := newKey(t).Address
	mine(t, bc, int(MempoolExpiryMargin)-1, miner) // uç+1 = son kullanma yüksekliği: hâlâ geçerli
	if n := poolLen(t, m); n != 2 {
		t.Fatalf("süresi dolmamış işlemler düştü: %d", n)
	}
	mine(t, bc, 1, miner)
	if n := poolLen(t, m); n != 0 {
		t.Fatalf("süresi dolan işlem ve sonraki işlem havuzda kaldı: %d", n)
	}
}

// Reorg'da ana zincirden düşen bloğun işlemi havuza döner; işlemi içeren dal ana zincir
// olunca nonce'u onaylandığından yeniden düşer.
func TestMempoolReorgResurrects(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	parent := tipBlock(t, bc)
	tx := mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 1)
	mustAdd(t, m, tx)
	mineFromPool(t, bc, m)
	if n := poolLen(t, m); n != 0 {
		t.Fatalf("kazılan işlem havuzda: %d", n)
	}

	side := extend(t, bc, parent, 2, newKey(t).Address)
	mustAccept(t, bc, side...)
	if bc.TipHash() != side[1].Hash {
		t.Fatal("rakip dal benimsenmedi")
	}
	txs, err := m.Select(MaxTxPerBlock)
	if err != nil || len(txs) != 1 || txs[0].ID() != tx.ID() {
		t.Fatalf("düşen işlem havuza dönmedi: %d işlem, %v", len(txs), err)
	}

	withTx := blockOn(t, bc, parent, newKey(t).Address, tx)
	again := append([]*Block{withTx}, extend(t, bc, withTx, 2, newKey(t).Address)...)
	mustAccept(t, bc, again...)
	if bc.TipHash() != again[2].Hash {
		t.Fatal("işlemi içeren dal benimsenmedi")
	}
	if n := poolLen(t, m); n != 0 {
		t.Fatalf("yeni dalda onaylanan işlem havuzda kaldı: %d", n)
	}
}

// Havuz dolunca en düşük ücretli gönderen kuyruğu tahliye edilir; ücret yetmiyorsa yeni işlem
// reddedilir. Gönderenin kendi zinciri tahliye edilmez.
func TestMempoolEviction(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	m.maxTxs = 3
	alice, bob, carol, dest := newKey(t), newKey(t), newKey(t), newKey(t)
	for _, kp := range []*crypto.KeyPair{alice, bob, carol} {
		fund(t, bc, kp, 1)
	}
	mustAdd(t, m, mpTx(t, bc, alice, dest.Address, Coin, 2000, 1))
	a2 := mpTx(t, bc, alice, dest.Address, Coin, 1500, 2)
	mustAdd(t, m, a2)
	mustAdd(t, m, mpTx(t, bc, bob, dest.Address, Coin, 5000, 1))

	for _, fee := range []int64{MempoolMinFee, 1500} { // en düşük kuyruk 1500: eşit ücret yetmez
		if _, err := m.Add(mpTx(t, bc, carol, dest.Address, Coin, fee, 1)); !errors.Is(err, ErrMempoolFull) {
			t.Fatalf("ücret %d: %v, beklenen ErrMempoolFull", fee, err)
		}
	}
	mustAdd(t, m, mpTx(t, bc, carol, dest.Address, Coin, 1600, 1))
	if n := poolLen(t, m); n != 3 {
		t.Fatalf("havuzda %d işlem, beklenen 3", n)
	}
	txs, err := m.Select(MaxTxPerBlock)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range txs {
		if tx.ID() == a2.ID() {
			t.Fatal("en düşük ücretli kuyruk tahliye edilmedi")
		}
	}
	if _, err := m.Add(a2); !errors.Is(err, ErrMempoolFull) {
		t.Fatalf("tahliye edilen işlem geri döndü: %v", err)
	}
}
