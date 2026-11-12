package core

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestValidTransfer(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 2) // 100 QBTC

	b, err := bc.AddBlock([]Transaction{signedTx(t, alice, bob.Address, 30*Coin, 1)}, alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Transactions) != 2 {
		t.Fatalf("blokta %d işlem, beklenen 2", len(b.Transactions))
	}
	if got := mustBalance(t, bc, alice.Address); got != (100-30+50)*Coin {
		t.Fatalf("alice bakiyesi %d", got)
	}
	if got := mustBalance(t, bc, bob.Address); got != 30*Coin {
		t.Fatalf("bob bakiyesi %d", got)
	}
	if n, _ := bc.GetNonce(alice.Address); n != 1 {
		t.Fatalf("alice nonce %d", n)
	}
}

// Açık 1: başkasının parasını harcama / imza sahteciliği.
func TestRejectsForgedTransactions(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, mallory := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	tampered := signedTx(t, alice, bob.Address, 1, 1)
	tampered.Amount = 50 // imzadan sonra tutar değiştirildi

	stolen := signedTx(t, mallory, mallory.Address+"", 1, 1)
	stolen.Sender = alice.Address // mallory kendi anahtarıyla alice adına imzalıyor
	stolen.Receiver = bob.Address

	unsigned := Transaction{Sender: alice.Address, Receiver: bob.Address, Amount: 10, Nonce: 1, PublicKey: alice.PublicKey}

	redirected := signedTx(t, alice, bob.Address, 10, 1)
	redirected.Receiver = mallory.Address // alıcı imzadan sonra değiştirildi

	for name, tx := range map[string]Transaction{
		"tutar değiştirilmiş": tampered,
		"başkasının adresi":   stolen,
		"imzasız":             unsigned,
		"alıcı değiştirilmiş": redirected,
	} {
		t.Run(name, func(t *testing.T) {
			if err := tx.VerifyStateless(); err == nil {
				t.Fatal("sahte işlem doğrulandı")
			}
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address), tx})
			if err := bc.AcceptBlock(b); err == nil {
				t.Fatal("sahte işlem içeren blok kabul edildi")
			}
		})
	}
	if got := mustBalance(t, bc, alice.Address); got != BlockReward {
		t.Fatalf("alice bakiyesi değişti: %d", got)
	}
}

// Açık 2: P2P'den sahte coinbase ile para basma, PoW ve zorluk atlatma.
func TestRejectsInvalidBlocks(t *testing.T) {
	bc := newTestChain(t)
	mallory := newKey(t)

	bigCoinbase := coinbaseTo(mallory.Address)
	bigCoinbase.Amount = BlockReward + 1 // fazla talep (para basma)

	extraCoinbase := coinbaseTo(mallory.Address)

	type invalidCase struct {
		mk   func() *Block
		want error
	}
	cases := map[string]invalidCase{
		"şişirilmiş coinbase": {func() *Block { return craftBlock(t, bc, []Transaction{bigCoinbase}) }, ErrBadCoinbase},
		"ikinci coinbase": {func() *Block {
			return craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address), extraCoinbase})
		}, ErrUnexpectedCoinbase},
		"coinbase yok": {func() *Block { return craftBlock(t, bc, nil) }, ErrBadCoinbase},
		"gövde başlıkla uyuşmuyor": {func() *Block {
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address)})
			b.Transactions[0].Amount = 500
			return b
		}, ErrBadMerkleRoot},
		"PoW yok": {func() *Block {
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address)})
			for b.Nonce = 0; ; b.Nonce++ {
				b.Hash = b.CalculateHash()
				if !MeetsTarget(b.Hash, bc.Bits()) {
					return b
				}
			}
		}, ErrInsufficientPoW},
		"zorluk düşürülmüş": {func() *Block {
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address)})
			if err := b.MineBlock(easierThanLimitBits()); err != nil {
				t.Fatal(err)
			}
			return b
		}, ErrBadDifficulty},
		"yanlış sürüm": {func() *Block {
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address)})
			b.Version = 2
			regrind(b) // MineBlock sürümü ezer; geçerli PoW ile yalnızca sürüm hatalı kalsın
			return b
		}, ErrBadVersion},
		"coinbase nonce yükseklik değil": {func() *Block {
			cb := coinbaseTo(mallory.Address)
			cb.Nonce = 99
			return craftBlock(t, bc, []Transaction{cb})
		}, ErrBadCoinbase},
		"zaman geçmişte": {func() *Block {
			prev, _ := bc.GetLatestBlock()
			b := &Block{Index: prev.Index + 1, Timestamp: prev.Timestamp - 100,
				Transactions: []Transaction{coinbaseTo(mallory.Address)}, PrevHash: prev.Hash}
			mineAt(t, bc, b)
			return b
		}, ErrTimestampOld},
		"zaman çok ileride": {func() *Block {
			prev, _ := bc.GetLatestBlock()
			b := &Block{Index: prev.Index + 1, Timestamp: bc.now() + MaxFutureDrift + 1000,
				Transactions: []Transaction{coinbaseTo(mallory.Address)}, PrevHash: prev.Hash}
			mineAt(t, bc, b)
			return b
		}, ErrTimestampFuture},
		"yanlış önceki hash": {func() *Block {
			b := craftBlock(t, bc, []Transaction{coinbaseTo(mallory.Address)})
			b.PrevHash = strings.Repeat("ab", 32)
			mineAt(t, bc, b)
			return b
		}, ErrBadPrevHash},
	}
	before, _ := bc.GetLatestBlock()
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := bc.AcceptBlock(c.mk()); !errors.Is(err, c.want) {
				t.Fatalf("hata %v, beklenen %v", err, c.want)
			}
		})
	}
	after, _ := bc.GetLatestBlock()
	if after.Hash != before.Hash || mustBalance(t, bc, mallory.Address) != 0 {
		t.Fatal("reddedilen bloklar durumu değiştirdi")
	}
}

// Gelecek zamanlı blok kalıcı reddedilmemeli: düğümün saati yetişince aynı blok kabul edilir.
// Aksi halde saati birkaç saniye geride olan düğümler ağdan kalıcı olarak ayrılır.
func TestFutureBlockAcceptedWhenClockCatchesUp(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t)
	prev, _ := bc.GetLatestBlock()
	ts := bc.now() + MaxFutureDrift + 100
	b := &Block{Index: prev.Index + 1, Timestamp: ts,
		Transactions: []Transaction{coinbaseTo(miner.Address)}, PrevHash: prev.Hash}
	mineAt(t, bc, b)

	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("beklenen ErrTimestampFuture, gelen %v", err)
	}
	if bc.rejected.has(b.Hash) {
		t.Fatal("gelecek zamanlı blok kalıcı ret önbelleğine yazıldı")
	}

	bc.now = func() int64 { return ts - MaxFutureDrift }
	if err := bc.AcceptBlock(b); err != nil {
		t.Fatalf("saat yetişince blok kabul edilmeli: %v", err)
	}
}

func TestPermanentStatefulErrClassification(t *testing.T) {
	perm := []error{ErrTimestampOld, ErrInsufficientBal, ErrOverflow, ErrNonPositiveAmount,
		fmt.Errorf("işlem 1: %w", ErrBadNonce)}
	for _, err := range perm {
		if !isPermanentStatefulErr(err) {
			t.Errorf("%v kalıcı sayılmalı", err)
		}
	}
	transient := []error{ErrTimestampFuture, ErrCorruptState, errors.New("leveldb: okuma hatası")}
	for _, err := range transient {
		if isPermanentStatefulErr(err) {
			t.Errorf("%v kalıcı sayılmamalı", err)
		}
	}
}

// Açık 3: negatif / sıfır tutar.
func TestRejectsNonPositiveAmounts(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	for _, amt := range []int64{0, -1, math.MinInt64} {
		tx := signedTx(t, alice, bob.Address, amt, 1)
		if err := tx.VerifyStateless(); !errors.Is(err, ErrNonPositiveAmount) {
			t.Fatalf("tutar %d: hata %v", amt, err)
		}
		b := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx})
		if err := bc.AcceptBlock(b); err == nil {
			t.Fatalf("tutar %d kabul edildi", amt)
		}
	}
	if mustBalance(t, bc, alice.Address) != BlockReward || mustBalance(t, bc, bob.Address) != 0 {
		t.Fatal("bakiyeler değişti")
	}
}

// Açık 4: aynı blokta çift harcama.
func TestRejectsDoubleSpendInSameBlock(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, carol := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1) // 50

	tx1 := signedTx(t, alice, bob.Address, 40*Coin, 1)
	tx2 := signedTx(t, alice, carol.Address, 40*Coin, 2)

	// Ağdan gelen blok: tamamı reddedilmeli.
	b := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx1, tx2})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("çift harcamalı blok: hata %v", err)
	}

	// Yerel madencilik: ikinci işlem ayıklanmalı.
	mined, err := bc.AddBlock([]Transaction{tx1, tx2}, bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	if len(mined.Transactions) != 2 {
		t.Fatalf("blokta %d işlem, beklenen coinbase + 1", len(mined.Transactions))
	}
	if mustBalance(t, bc, alice.Address) != 10*Coin || mustBalance(t, bc, carol.Address) != 0 {
		t.Fatal("bakiyeler yanlış")
	}
}

// Açık 5: tekrar oynatma (replay).
func TestRejectsReplay(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 2)

	tx := signedTx(t, alice, bob.Address, 10*Coin, 1)
	if _, err := bc.AddBlock([]Transaction{tx}, bob.Address); err != nil {
		t.Fatal(err)
	}
	b := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("replay: hata %v", err)
	}
	skip := signedTx(t, alice, bob.Address, 10*Coin, 5)
	b = craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), skip})
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("atlanan nonce: hata %v", err)
	}
	if got := mustBalance(t, bc, bob.Address); got != BlockReward+10*Coin {
		t.Fatalf("bob bakiyesi %d", got)
	}
}

func TestRejectsTooManyTransactions(t *testing.T) {
	bc := newTestChain(t)
	m := newKey(t)
	txs := make([]Transaction, MaxTxPerBlock+2)
	txs[0] = coinbaseTo(m.Address)
	b := craftBlock(t, bc, txs)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrTooManyTx) {
		t.Fatalf("hata %v", err)
	}
}

// Açık 6: durum yazımı kalıcı ve tutarlı; yeniden açılışta korunur.
func TestStatePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	bc, err := NewBlockchain(dir, testDifficulty)
	if err != nil {
		t.Fatal(err)
	}
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	if _, err := bc.AddBlock([]Transaction{signedTx(t, alice, bob.Address, 20*Coin, 1)}, alice.Address); err != nil {
		t.Fatal(err)
	}
	tip, _ := bc.GetLatestBlock()
	bc.Close()

	bc2, err := NewBlockchain(dir, testDifficulty)
	if err != nil {
		t.Fatal(err)
	}
	defer bc2.Close()
	tip2, err := bc2.GetLatestBlock()
	if err != nil || tip2.Hash != tip.Hash {
		t.Fatalf("uç blok korunmadı: %v", err)
	}
	if mustBalance(t, bc2, alice.Address) != 80*Coin || mustBalance(t, bc2, bob.Address) != 20*Coin {
		t.Fatal("bakiyeler korunmadı")
	}
	if n, _ := bc2.GetNonce(alice.Address); n != 1 {
		t.Fatalf("nonce korunmadı: %d", n)
	}
}

// Reddedilen blok önbelleği: geçersiz blok tekrar gönderildiğinde pahalı doğrulama yapılmaz,
// ama doğrulanmamış bir özetle dürüst bir blok zehirlenemez.
func TestRejectCache(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)

	bad := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), signedTx(t, alice, bob.Address, 999*Coin, 1)})
	if err := bc.AcceptBlock(bad); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("ilk ret: %v", err)
	}
	if err := bc.AcceptBlock(bad); !errors.Is(err, ErrKnownInvalid) {
		t.Fatalf("tekrar gönderim önbellekten reddedilmeli: %v", err)
	}

	// Dürüst başlık + değiştirilmiş gövde: özet yalnızca başlığı kapsadığı için bu bloklar
	// dürüst bloğun özetini taşır. Hiçbiri önbelleğe girmemeli; dürüst blok sonra kabul edilmeli.
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	good := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx})
	extra := signedTx(t, alice, bob.Address, Coin, 2)
	manyTxs := make([]Transaction, MaxTxPerBlock+2)
	copy(manyTxs, good.Transactions)

	bodies := map[string][]Transaction{
		"farklı coinbase":    {coinbaseTo(alice.Address), tx},
		"fazladan işlem":     {good.Transactions[0], tx, extra},
		"yer değiştirmiş":    {tx, good.Transactions[0]},
		"çok fazla işlem":    manyTxs,
		"boş gövde":          nil,
		"son işlem kopyalı":  {good.Transactions[0], tx, tx},
		"imzası bozuk işlem": {good.Transactions[0], func() Transaction { x := tx; x.Signature = nil; return x }()},
	}
	for name, txs := range bodies {
		t.Run(name, func(t *testing.T) {
			poison := *good
			poison.Transactions = txs
			if err := bc.AcceptBlock(&poison); err == nil {
				t.Fatal("zehirli blok kabul edildi")
			}
			if bc.rejected.has(good.Hash) {
				t.Fatal("dürüst bloğun özeti zehirli gövdeyle önbelleğe alındı")
			}
		})
	}
	poison := *good
	poison.Bits = easierThanLimitBits()
	if err := bc.AcceptBlock(&poison); err == nil {
		t.Fatal("zehirli blok kabul edildi")
	}
	if err := bc.AcceptBlock(good); err != nil {
		t.Fatalf("dürüst blok zehirlenmiş önbellek yüzünden reddedildi: %v", err)
	}
}

func TestStateViewOverflow(t *testing.T) {
	bc := newTestChain(t)
	v := newStateView(bc)
	v.balances["r"] = math.MaxInt64 - 5
	v.balances["s"] = 100
	tx := &Transaction{Sender: "s", Receiver: "r", Amount: 10, Nonce: 1}
	if err := v.apply(tx); !errors.Is(err, ErrOverflow) {
		t.Fatalf("hata %v", err)
	}
	if v.balances["s"] != 100 {
		t.Fatal("başarısız apply durumu değiştirdi")
	}
	if err := v.credit("r", 10); !errors.Is(err, ErrOverflow) {
		t.Fatalf("credit taşması: %v", err)
	}
}

func TestBlockHashFieldSeparation(t *testing.T) {
	h := strings.Repeat("ab", 32)
	a := &Block{Index: 1, Timestamp: 23, PrevHash: h, MerkleRoot: h}
	b := &Block{Index: 12, Timestamp: 3, PrevHash: h, MerkleRoot: h}
	if a.CalculateHash() == "" || a.CalculateHash() == b.CalculateHash() {
		t.Fatal("farklı başlıklar aynı özeti üretti")
	}
}
