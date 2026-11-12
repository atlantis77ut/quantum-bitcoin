package core

import (
	"errors"
	"testing"
)

func mustHistory(t *testing.T, bc *Blockchain, addr, before string, limit int) AddrHistory {
	t.Helper()
	h, err := bc.AddressHistory(addr, before, limit)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// allHistory adresin bütün geçmişini sayfa sayfa toplar (sayfa boyutu limit).
func allHistory(t *testing.T, bc *Blockchain, addr string, limit int) []AddrTx {
	t.Helper()
	var out []AddrTx
	before := ""
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("sayfalama bitmedi")
		}
		h := mustHistory(t, bc, addr, before, limit)
		if len(h.Txs) > limit {
			t.Fatalf("sayfada %d kayıt, limit %d", len(h.Txs), limit)
		}
		out = append(out, h.Txs...)
		if h.Next == "" {
			return out
		}
		if len(h.Txs) != limit {
			t.Fatalf("next var ama sayfa dolu değil: %d", len(h.Txs))
		}
		before = h.Next
	}
}

// Gönderen "out" (ücretle), alıcı "in" kaydı alır; coinbase alıcısı ödül + ücretle "in" alır;
// sıfır değerli genesis coinbase'i indekslenmez; sayfalama atlamasız ve tekrarsız, yeniden
// eskiye sıralıdır; havuzdaki işlemler görünmez.
func TestAddrHistoryEntriesAndPaging(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)

	g := genesisOf(t, bc)
	if h := mustHistory(t, bc, g.Transactions[0].Receiver, "", AddrHistoryMaxLimit); len(h.Txs) != 0 {
		t.Fatalf("genesis coinbase'i indekslendi: %+v", h.Txs)
	}

	var txs []Transaction
	for n := uint64(1); n <= 3; n++ {
		txs = append(txs, signedTxFee(t, alice, bob.Address, int64(n)*Coin, int64(n)*MempoolMinFee, n))
	}
	blk := blockOn(t, bc, pre[len(pre)-1], miner.Address, txs...)
	mustAccept(t, bc, blk)
	tx4 := signedTxFee(t, bob, alice.Address, Coin/2, MempoolMinFee, 1)
	blk2 := blockOn(t, bc, blk, miner.Address, tx4)
	mustAccept(t, bc, blk2)

	// alice: blk2'de gelen, blk'de üç giden (sıra 3,2,1), en altta h1 coinbase'i.
	all := allHistory(t, bc, alice.Address, 2)
	if len(all) != 5 {
		t.Fatalf("alice'te %d kayıt, beklenen 5: %+v", len(all), all)
	}
	want := []struct {
		height int64
		index  int
		dir    string
		amount int64
		fee    int64
		cb     bool
	}{
		{blk2.Index, 1, "in", Coin / 2, 0, false},
		{blk.Index, 3, "out", 3 * Coin, 3 * MempoolMinFee, false},
		{blk.Index, 2, "out", 2 * Coin, 2 * MempoolMinFee, false},
		{blk.Index, 1, "out", Coin, MempoolMinFee, false},
		{1, 0, "in", pre[0].Transactions[0].Amount, 0, true},
	}
	for i, w := range want {
		got := all[i]
		if got.Height != w.height || got.Index != w.index || got.Direction != w.dir || got.Amount != w.amount ||
			got.Fee != w.fee || got.Coinbase != w.cb {
			t.Fatalf("alice[%d] = %+v, beklenen %+v", i, got, w)
		}
	}
	if all[1].TxID != txs[2].ID() || all[1].Counterparty != bob.Address || all[1].BlockHash != blk.Hash ||
		all[1].Confirmations != 2 || all[0].Counterparty != bob.Address || all[0].TxID != tx4.ID() ||
		all[4].Counterparty != CoinbaseSender || all[4].BlockHash != pre[0].Hash {
		t.Fatalf("alice ayrıntıları: %+v", all)
	}
	// Tek sayfada da aynı sıra.
	one := mustHistory(t, bc, alice.Address, "", AddrHistoryMaxLimit)
	if len(one.Txs) != 5 || one.Next != "" || one.TipHash != blk2.Hash || one.TipHeight != blk2.Index {
		t.Fatalf("tek sayfa: %+v", one)
	}
	for i := range one.Txs {
		if one.Txs[i] != all[i] {
			t.Fatalf("sayfalı ve tek sayfa farklı: %d", i)
		}
	}

	// bob: blk2'de giden, blk'de üç gelen.
	bobAll := allHistory(t, bc, bob.Address, 3)
	if len(bobAll) != 4 || bobAll[0].Direction != "out" || bobAll[0].Fee != MempoolMinFee ||
		bobAll[3].Direction != "in" || bobAll[3].Amount != Coin || bobAll[3].Fee != 0 || bobAll[3].Counterparty != alice.Address {
		t.Fatalf("bob: %+v", bobAll)
	}

	// madenci: iki coinbase, ücretler dahil.
	mh := mustHistory(t, bc, miner.Address, "", 10)
	if len(mh.Txs) != 2 || !mh.Txs[0].Coinbase || mh.Txs[0].Amount != blk2.Transactions[0].Amount ||
		mh.Txs[1].Amount != BlockSubsidy(blk.Index, bc.params)+6*MempoolMinFee {
		t.Fatalf("madenci: %+v", mh.Txs)
	}

	// İmleç hariçtir: blk'nin 2. işleminden önceki tek kayıt sıra 1'dir.
	h := mustHistory(t, bc, alice.Address, FormatAddrCursor(blk.Index, 2), 1)
	if len(h.Txs) != 1 || h.Txs[0].Index != 1 || h.Txs[0].Height != blk.Index || h.Next == "" {
		t.Fatalf("imleç: %+v", h)
	}

	// Havuzdaki işlem görünmez.
	m := NewMempool(bc)
	mustAdd(t, m, mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, 4))
	if n := len(mustHistory(t, bc, alice.Address, "", AddrHistoryMaxLimit).Txs); n != 5 {
		t.Fatalf("havuzdaki işlem geçmişte: %d", n)
	}
}

// Reorg'da eski dalın kayıtları düşer, yeni dalınkiler girer; durum baştan uygulamayla aynıdır
// (chainDump adres geçmişini de kapsar).
func TestAddrHistoryReorg(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, minerA, minerB := newKey(t), newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)
	parent := pre[len(pre)-1]
	tx := signedTxFee(t, alice, bob.Address, Coin, MempoolMinFee, 1)

	a1 := blockOn(t, bc, parent, minerA.Address, tx)
	mustAccept(t, bc, a1)
	if h := mustHistory(t, bc, bob.Address, "", 10); len(h.Txs) != 1 || h.Txs[0].BlockHash != a1.Hash {
		t.Fatalf("A dalı bob: %+v", h.Txs)
	}

	side := extend(t, bc, parent, 2, minerB.Address)
	mustAccept(t, bc, side...)
	if tipHash(t, bc) != side[1].Hash {
		t.Fatal("B dalı benimsenmedi")
	}
	for _, addr := range []string{bob.Address, minerA.Address} {
		if h := mustHistory(t, bc, addr, "", 10); len(h.Txs) != 0 {
			t.Fatalf("eski dal kaydı kaldı (%s): %+v", addr, h.Txs)
		}
	}
	if h := mustHistory(t, bc, alice.Address, "", 10); len(h.Txs) != 1 || !h.Txs[0].Coinbase {
		t.Fatalf("alice yalnızca coinbase'ini görmeli: %+v", h.Txs)
	}
	if h := mustHistory(t, bc, minerB.Address, "", 10); len(h.Txs) != 2 {
		t.Fatalf("B madencisi: %+v", h.Txs)
	}
	assertSameState(t, bc, replay(t, bc, with(pre, side...)...))

	// İşlem C dalında yeniden kazılır: bob'un kaydı yeni bloğu gösterir.
	c1 := blockOn(t, bc, parent, minerB.Address)
	c2 := blockOn(t, bc, c1, minerA.Address, tx)
	c3 := blockOn(t, bc, c2, minerA.Address)
	mustAccept(t, bc, c1, c2, c3)
	if h := mustHistory(t, bc, bob.Address, "", 10); len(h.Txs) != 1 || h.Txs[0].BlockHash != c2.Hash || h.Txs[0].Confirmations != 2 {
		t.Fatalf("C dalı bob: %+v", h.Txs)
	}
	assertSameState(t, bc, replay(t, bc, with(pre, c1, c2, c3)...))
}

// Geçersiz imleç ve limit reddedilir; tutarsız kayıt bozuk durum olarak raporlanır.
func TestAddrHistoryBadInputAndCorrupt(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	b1, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"1", ":1", "1:", "-1:0", "1:-1", "a:0", "1:0:0", "1:99999999999", " 1:0", "1:+0", "1:1001"} {
		if _, err := bc.AddressHistory(miner, c, 10); !errors.Is(err, ErrBadCursor) {
			t.Fatalf("imleç %q: %v", c, err)
		}
	}
	for _, l := range []int{0, -1, AddrHistoryMaxLimit + 1} {
		if _, err := bc.AddressHistory(miner, "", l); !errors.Is(err, ErrBadCursor) {
			t.Fatalf("limit %d: %v", l, err)
		}
	}
	if _, err := bc.AddressHistory("lh", "", 10); err == nil {
		t.Fatal("geçersiz adres kabul edildi")
	}

	key := addrTxKey(miner, b1.Index, 0)
	good, err := bc.db.Get(key, nil)
	if err != nil {
		t.Fatal(err)
	}
	zero := "0000000000000000000000000000000000000000000000000000000000000000"
	id := b1.Transactions[0].ID()
	for _, v := range []string{
		`{bozuk`,
		`{"id":"` + id + `","b":"` + zero + `","d":"in","a":1,"c":"x","cb":true}`,
		`{"id":"` + id + `","b":"` + b1.Hash + `","d":"yan","a":1,"c":"x"}`,
		`{"id":"` + id + `","b":"` + b1.Hash + `","d":"in","a":0,"c":"x"}`,
		`{"id":"` + id + `","b":"` + b1.Hash + `","d":"in","a":1,"f":5,"c":"x"}`,
		`{"id":"lh","b":"` + b1.Hash + `","d":"in","a":1,"c":"x"}`,
	} {
		if err := bc.db.Put(key, []byte(v), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := bc.AddressHistory(miner, "", 10); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("%s: %v, beklenen ErrCorruptState", v, err)
		}
	}
	// Dolu bloğun son sırası (coinbase + MaxTxPerBlock işlem) geçerlidir; imleç olarak da kabul edilir.
	if err := bc.db.Put(key, good, nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.db.Put(addrTxKey(miner, b1.Index, MaxTxPerBlock), good, nil); err != nil {
		t.Fatal(err)
	}
	h := mustHistory(t, bc, miner, "", 1)
	if len(h.Txs) != 1 || h.Txs[0].Index != MaxTxPerBlock || h.Next != FormatAddrCursor(b1.Index, MaxTxPerBlock) {
		t.Fatalf("son sıra: %+v", h)
	}
	if h := mustHistory(t, bc, miner, h.Next, 10); len(h.Txs) != 1 || h.Txs[0].Index != 0 {
		t.Fatalf("son sıradan sonraki sayfa: %+v", h)
	}
	if err := bc.db.Delete(addrTxKey(miner, b1.Index, MaxTxPerBlock), nil); err != nil {
		t.Fatal(err)
	}
	// Uçtan yüksek bir yükseklikteki kayıt da bozuktur.
	if err := bc.db.Put(addrTxKey(miner, 99, 0), good, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddressHistory(miner, "", 10); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("uçtan yüksek kayıt: %v", err)
	}
}
