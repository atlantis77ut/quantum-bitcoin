package core

import (
	"errors"
	"math"
	"testing"
)

func mustBalanceInfo(t *testing.T, bc *Blockchain, addr string) BalanceInfo {
	t.Helper()
	info, err := bc.Balance(addr)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func mustPoolBalance(t *testing.T, m *Mempool, addr string) PoolBalance {
	t.Helper()
	pb, err := m.Balance(addr)
	if err != nil {
		t.Fatal(err)
	}
	return pb
}

func immatureHeights(info BalanceInfo) []int64 {
	var out []int64
	for _, c := range info.ImmatureCoinbases {
		out = append(out, c.Height)
	}
	return out
}

func sameHeights(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Coinbase H yüksekliğinde olgunlaşmamış görünür; uç H+M−1 iken havuzun harcanabilirine girer
// (uç+1'de olgunlaşır), uç H+M olunca onaylı bakiyeye geçer ve listeden çıkar. Genesis
// hiç listelenmez.
func TestBalanceImmatureBoundary(t *testing.T) {
	bc := newTestChain(t)
	m := NewMempool(bc)
	miner, burn := newKey(t), newKey(t)
	M := bc.params.CoinbaseMaturity

	g := genesisOf(t, bc)
	if info := mustBalanceInfo(t, bc, g.Transactions[0].Receiver); len(info.ImmatureCoinbases) != 0 || info.Immature != 0 {
		t.Fatalf("genesis listelendi: %+v", info)
	}

	b1, err := bc.AddBlock(nil, miner.Address)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := bc.AddBlock(nil, miner.Address)
	if err != nil {
		t.Fatal(err)
	}
	cb1, cb2 := b1.Transactions[0].Amount, b2.Transactions[0].Amount

	info := mustBalanceInfo(t, bc, miner.Address)
	if info.Balance != 0 || info.Immature != cb1+cb2 || !sameHeights(immatureHeights(info), []int64{1, 2}) ||
		info.TipHeight != 2 || info.TipHash != b2.Hash {
		t.Fatalf("uç 2: %+v", info)
	}
	if c := info.ImmatureCoinbases[0]; c.BlockHash != b1.Hash || c.Amount != cb1 || c.MaturesAt != 1+M {
		t.Fatalf("ilk kayıt %+v", c)
	}

	// Uç = 1+M−1: blok 1'in coinbase'i hâlâ olgunlaşmamış ama uç+1'de olgunlaştığından havuz
	// onu harcanabilir sayar.
	for bc.tip.Height < 1+M-1 {
		if _, err := bc.AddBlock(nil, burn.Address); err != nil {
			t.Fatal(err)
		}
	}
	pb := mustPoolBalance(t, m, miner.Address)
	if pb.Balance != 0 || !sameHeights(immatureHeights(pb.BalanceInfo), []int64{1, 2}) ||
		pb.ImmatureCoinbases[0].MaturesAt != pb.TipHeight+1 || pb.Spendable != cb1 || pb.PendingOutgoing != 0 {
		t.Fatalf("uç %d: %+v", pb.TipHeight, pb)
	}

	if _, err := bc.AddBlock(nil, burn.Address); err != nil {
		t.Fatal(err)
	}
	info = mustBalanceInfo(t, bc, miner.Address)
	if info.Balance != cb1 || info.Immature != cb2 || !sameHeights(immatureHeights(info), []int64{2}) {
		t.Fatalf("uç %d: %+v", info.TipHeight, info)
	}
	if pb := mustPoolBalance(t, m, miner.Address); pb.Spendable != cb1+cb2 {
		t.Fatalf("harcanabilir %d, beklenen %d", pb.Spendable, cb1+cb2)
	}

	if _, err := bc.AddBlock(nil, burn.Address); err != nil {
		t.Fatal(err)
	}
	info = mustBalanceInfo(t, bc, miner.Address)
	if info.Balance != cb1+cb2 || info.Immature != 0 || len(info.ImmatureCoinbases) != 0 {
		t.Fatalf("uç %d: %+v", info.TipHeight, info)
	}
	if got := mustBalance(t, bc, miner.Address); got != info.Balance {
		t.Fatalf("GetBalance %d, Balance %d", got, info.Balance)
	}
}

// Havuzdaki işlemler gönderenin bekleyen gidenini ve harcanabilirini, alıcının bekleyen gelenini
// değiştirir; kazıldıktan sonra bekleyenler sıfırlanır ve ücretler madencinin olgunlaşmamış
// coinbase'inde görünür.
func TestBalancePending(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	m := NewMempool(bc)

	tx1 := mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 1)
	tx2 := mpTx(t, bc, alice, bob.Address, 2*Coin, 2*MempoolMinFee, 2)
	mustAdd(t, m, tx1)
	mustAdd(t, m, tx2)
	out := 3*Coin + 3*MempoolMinFee

	pa := mustPoolBalance(t, m, alice.Address)
	if pa.Balance != testReward || pa.PendingOutgoing != out || pa.Spendable != testReward-out || pa.PendingIncoming != 0 {
		t.Fatalf("alice: %+v", pa)
	}
	pb := mustPoolBalance(t, m, bob.Address)
	if pb.Balance != 0 || pb.PendingIncoming != 3*Coin || pb.PendingOutgoing != 0 || pb.Spendable != 0 {
		t.Fatalf("bob: %+v", pb)
	}

	blk := mineFromPool(t, bc, m)
	if len(blk.Transactions) != 3 {
		t.Fatalf("blokta %d işlem", len(blk.Transactions))
	}
	pa = mustPoolBalance(t, m, alice.Address)
	if pa.Balance != testReward-out || pa.PendingOutgoing != 0 || pa.Spendable != pa.Balance {
		t.Fatalf("kazım sonrası alice: %+v", pa)
	}
	pb = mustPoolBalance(t, m, bob.Address)
	if pb.Balance != 3*Coin || pb.PendingIncoming != 0 || pb.Spendable != 3*Coin {
		t.Fatalf("kazım sonrası bob: %+v", pb)
	}
	cb := blk.Transactions[0]
	info := mustBalanceInfo(t, bc, cb.Receiver)
	if info.Immature != BlockSubsidy(blk.Index, bc.params)+3*MempoolMinFee || info.Immature != cb.Amount ||
		!sameHeights(immatureHeights(info), []int64{blk.Index}) {
		t.Fatalf("madenci: %+v, coinbase %d", info, cb.Amount)
	}
}

// Reorg'da eski dalın olgunlaşmamış coinbase'i listeden düşer, yeni dalınkiler girer.
func TestBalanceImmatureReorg(t *testing.T) {
	bc := newTestChain(t)
	alice, minerA, minerB := newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)
	parent := pre[len(pre)-1]

	a1 := blockOn(t, bc, parent, minerA.Address)
	mustAccept(t, bc, a1)
	if info := mustBalanceInfo(t, bc, minerA.Address); !sameHeights(immatureHeights(info), []int64{a1.Index}) {
		t.Fatalf("A dalı: %+v", info)
	}

	side := extend(t, bc, parent, 2, minerB.Address)
	mustAccept(t, bc, side...)
	if tipHash(t, bc) != side[1].Hash {
		t.Fatal("B dalı benimsenmedi")
	}
	if info := mustBalanceInfo(t, bc, minerA.Address); info.Immature != 0 || len(info.ImmatureCoinbases) != 0 {
		t.Fatalf("eski dal hâlâ listede: %+v", info)
	}
	info := mustBalanceInfo(t, bc, minerB.Address)
	if !sameHeights(immatureHeights(info), []int64{side[0].Index, side[1].Index}) ||
		info.ImmatureCoinbases[0].BlockHash != side[0].Hash || info.ImmatureCoinbases[1].BlockHash != side[1].Hash {
		t.Fatalf("B dalı: %+v", info)
	}
}

// Taşan toplam ve coinbase alıcısı olmayan ana zincir kaydı hata olur, sarmaz ya da atlanmaz.
func TestBalanceOverflowAndCorrupt(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	for i := 0; i < 2; i++ {
		if _, err := bc.AddBlock(nil, miner); err != nil {
			t.Fatal(err)
		}
	}
	e1, e2 := bc.index[bc.main[1]], bc.index[bc.main[2]]
	v1, v2 := e1.CbValue, e2.CbValue

	bc.mu.Lock()
	e1.CbValue, e2.CbValue = math.MaxInt64, math.MaxInt64
	bc.mu.Unlock()
	if _, err := bc.Balance(miner); !errors.Is(err, ErrOverflow) {
		t.Fatalf("taşma: %v", err)
	}

	bc.mu.Lock()
	e1.CbValue, e2.CbValue = v1, v2
	e2.CbTo = ""
	bc.mu.Unlock()
	if _, err := bc.Balance(miner); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("alıcısız kayıt: %v", err)
	}

	bc.mu.Lock()
	e2.CbTo = miner
	e1.CbValue = -1
	bc.mu.Unlock()
	if _, err := bc.Balance(newKey(t).Address); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("negatif coinbase: %v", err)
	}

	bc.mu.Lock()
	e1.CbValue = v1
	bc.mu.Unlock()
	if info := mustBalanceInfo(t, bc, miner); info.Immature != v1+v2 {
		t.Fatalf("geri yükleme: %+v", info)
	}
}
