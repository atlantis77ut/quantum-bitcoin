package network

import (
	"errors"
	"fmt"
	"testing"

	"quantum-bitcoin/core"
	"quantum-bitcoin/crypto"
)

// Yalnızca havuz içeriğinden bağımsız retler önbelleğe alınır.
func TestClassifyReject(t *testing.T) {
	cases := []struct {
		err  error
		want rejectClass
	}{
		{nil, rejectNone},
		{core.ErrBadSignature, rejectInvalid},
		{fmt.Errorf("%w: x", core.ErrBadSender), rejectInvalid},
		{core.ErrBadReceiver, rejectInvalid},
		{core.ErrSelfTransfer, rejectInvalid},
		{core.ErrNonPositiveAmount, rejectInvalid},
		{core.ErrNegativeFee, rejectInvalid},
		{core.ErrBadExpiry, rejectInvalid},
		{core.ErrUnexpectedCoinbase, rejectInvalid},
		{fmt.Errorf("%w: en az 1", core.ErrFeeTooLow), rejectPolicy},
		{fmt.Errorf("%w: 3 blok", core.ErrExpiryTooSoon), rejectTip},
		{fmt.Errorf("%w: nonce", core.ErrBadNonce), rejectTip},
		{core.ErrNonceGap, rejectNone},
		{core.ErrMempoolFull, rejectNone},
		{core.ErrReplaceFee, rejectNone},
		{core.ErrReplaceLimit, rejectNone},
		{core.ErrSenderLimit, rejectNone},
		{core.ErrTxKnown, rejectNone},
		{core.ErrExpiryTooFar, rejectNone},
		{core.ErrInsufficientBal, rejectNone},
		{core.ErrCorruptState, rejectNone},
		{errors.New("iç hata"), rejectNone},
	}
	for _, c := range cases {
		if got := classifyReject(c.err); got != c.want {
			t.Errorf("%v: %d, beklenen %d", c.err, got, c.want)
		}
	}
}

// Önbellek sınırlıdır; dolunca en eski kayıt düşer; var olan kayıt güncellenir.
func TestRejectCacheFIFO(t *testing.T) {
	c := newRejectCache(3)
	id := func(b byte) [32]byte { return [32]byte{b} }
	for b := byte(1); b <= 3; b++ {
		c.add(id(b), rejectEntry{class: rejectPolicy})
	}
	c.add(id(2), rejectEntry{class: rejectInvalid}) // güncelleme, düşürme yok
	if c.len() != 3 {
		t.Fatalf("güncelleme kayıt düşürdü: %d", c.len())
	}
	c.add(id(4), rejectEntry{class: rejectPolicy})
	if _, ok := c.get(id(1)); ok || c.len() != 3 {
		t.Fatal("en eski kayıt düşmedi")
	}
	if e, ok := c.get(id(2)); !ok || e.class != rejectInvalid {
		t.Fatalf("güncellenen kayıt: %+v %v", e, ok)
	}
	c.add(id(5), rejectEntry{class: rejectPolicy})
	if _, ok := c.get(id(2)); ok {
		t.Fatal("ikinci en eski kayıt düşmedi")
	}
	tip := rejectEntry{class: rejectTip, tip: "x"}
	c.add(id(6), tip)
	if got := c.check(id(6), func(string) bool { return false }); got != rejectNone {
		t.Fatal("ana zincirde olmayan uca dayanan ret geçerli sayıldı")
	}
	if got := c.check(id(6), func(string) bool { return true }); got != rejectTip {
		t.Fatal("ana zincirdeki uca dayanan ret yok sayıldı")
	}
}

func signedPeerTx(t *testing.T, from *crypto.KeyPair, to string, amount, fee int64, nonce uint64, expiry int64) core.Transaction {
	t.Helper()
	tx := core.Transaction{Receiver: to, Amount: amount, Fee: fee, ExpiryHeight: expiry, Nonce: nonce}
	if err := tx.Sign(from, core.RegtestParams); err != nil {
		t.Fatal(err)
	}
	return tx
}

// Bozuk kopya kendi kimliğiyle önbelleğe girer ve yeniden gönderen grup cezalanır; dürüst
// işlem etkilenmez. Düşük ücret önbelleğe girer, cezalanmaz. Nonce boşluğu önbelleğe girmez.
func TestRejectCacheHandleTxs(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	good := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce, p.DefaultExpiryHeight)
	bad := good
	bad.Amount = 2 * core.Coin // imza tutmaz, kimlik farklı

	ps.handleTxs("eş", pid("g1"), []core.Transaction{bad})
	if e, ok := ps.rejects.get(bad.IDBytes()); !ok || e.class != rejectInvalid {
		t.Fatalf("bozuk kopya önbellekte değil: %+v %v", e, ok)
	}
	ps.handleTxs("eş", pid("g2"), []core.Transaction{bad})
	if ps.txGate.allow(pid("g2"), 1) {
		t.Fatal("bilinen geçersiz işlemi gönderen grup cezalanmadı")
	}

	// Nonce boşluğu: sonraki işlem önce gelir, önbelleğe girmez; öncülü gelince kabul edilir.
	next := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+1, p.DefaultExpiryHeight)
	ps.handleTxs("eş", pid("g3"), []core.Transaction{next})
	if _, ok := ps.rejects.get(next.IDBytes()); ok {
		t.Fatal("nonce boşluğu önbelleğe alındı")
	}
	ps.handleTxs("eş", pid("g3"), []core.Transaction{good, next})
	if n := mempoolLen(t, ps.Mempool); n != 2 {
		t.Fatalf("dürüst işlemler kabul edilmedi: havuz %d", n)
	}

	// Düşük ücret: önbelleğe girer, ikinci gönderim Add'e gitmez ve cezalandırılmaz.
	low := signedPeerTx(t, alice, to, core.Coin, p.MinFee-1, p.NextNonce+2, p.DefaultExpiryHeight)
	ps.handleTxs("eş", pid("g4"), []core.Transaction{low})
	if e, ok := ps.rejects.get(low.IDBytes()); !ok || e.class != rejectPolicy {
		t.Fatalf("düşük ücret önbellekte değil: %+v %v", e, ok)
	}
	ps.handleTxs("eş", pid("g4"), []core.Transaction{low})
	if !ps.txGate.allow(pid("g4"), 1) {
		t.Fatal("düşük ücretli işlem cezalandırıldı")
	}
}

// Onaylanmış nonce'lu işlem uca bağlı olarak önbelleğe girer; dayandığı uç ana zincirden
// düşerse kayıt yok sayılır. Havuz eşitlemesi önbellekteki kimlikleri istemez.
func TestRejectCacheConfirmedNonceAndPullFilter(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	ps := NewP2PServer("127.0.0.1:0", bc)
	p, err := ps.Mempool.Policy(alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	to := minerAddr(t)
	mined := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce, p.DefaultExpiryHeight)
	if _, err := bc.AddBlock([]core.Transaction{mined}, minerAddr(t)); err != nil {
		t.Fatal(err)
	}
	stale := signedPeerTx(t, alice, to, 2*core.Coin, p.MinFee, p.NextNonce, p.DefaultExpiryHeight)
	ps.handleTxs("eş", pid("g1"), []core.Transaction{stale})
	e, ok := ps.rejects.get(stale.IDBytes())
	if !ok || e.class != rejectTip || e.tip != bc.TipHash() {
		t.Fatalf("onaylı nonce kaydı: %+v %v (uç %s)", e, ok, bc.TipHash())
	}
	if !ps.txGate.allow(pid("g1"), 1) {
		t.Fatal("durum retti cezalandırıldı")
	}

	fresh := signedPeerTx(t, alice, to, core.Coin, p.MinFee, p.NextNonce+1, p.DefaultExpiryHeight)
	ids := []string{stale.ID(), fresh.ID()}
	if got := ps.filterRejected(ids); len(got) != 1 || got[0] != fresh.ID() {
		t.Fatalf("süzülen kimlikler %v", got)
	}
	mineN(t, bc, 1) // uç ilerler: kayıt geçerli kalır
	if got := ps.filterRejected(ids); len(got) != 1 {
		t.Fatalf("uç ilerleyince kayıt düştü: %v", got)
	}

	// Reorg yerine: kaydın dayandığı uç ana zincirde değilmiş gibi.
	e.tip = "0000000000000000000000000000000000000000000000000000000000000000"
	ps.rejects.add(stale.IDBytes(), e)
	if got := ps.filterRejected(ids); len(got) != 2 {
		t.Fatalf("ana zincirde olmayan uca dayanan kayıt süzdü: %v", got)
	}
}
