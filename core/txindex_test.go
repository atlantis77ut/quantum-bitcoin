package core

import (
	"errors"
	"testing"
)

func mustTx(t *testing.T, bc *Blockchain, id string) (*Block, int, int64) {
	t.Helper()
	b, pos, conf, err := bc.TxByID(id)
	if err != nil {
		t.Fatalf("işlem %s: %v", id, err)
	}
	return b, pos, conf
}

func missingTx(t *testing.T, bc *Blockchain, id string) {
	t.Helper()
	if _, _, _, err := bc.TxByID(id); !errors.Is(err, ErrUnknownTx) {
		t.Fatalf("işlem %s: %v, beklenen ErrUnknownTx", id, err)
	}
}

// Bağlanan blokların işlemleri (coinbase ve genesis dahil) indekslenir; indeks yeniden açılışta
// kalır; onay sayısı uçla birlikte artar.
func TestTxIndexConnectAndReopen(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	alice, bob := newKey(t), newKey(t)
	pre := prefix(t, bc, alice)
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	blk := blockOn(t, bc, pre[len(pre)-1], newKey(t).Address, tx)
	mustAccept(t, bc, blk)

	if b, pos, conf := mustTx(t, bc, tx.ID()); b.Hash != blk.Hash || pos != 1 || conf != 1 {
		t.Fatalf("işlem: blok %s sıra %d onay %d", b.Hash, pos, conf)
	}
	if b, pos, _ := mustTx(t, bc, blk.Transactions[0].ID()); b.Hash != blk.Hash || pos != 0 {
		t.Fatalf("coinbase: blok %s sıra %d", b.Hash, pos)
	}
	g := genesisOf(t, bc)
	if b, pos, _ := mustTx(t, bc, g.Transactions[0].ID()); b.Index != 0 || pos != 0 {
		t.Fatalf("genesis coinbase: yükseklik %d sıra %d", b.Index, pos)
	}
	mustAccept(t, bc, extend(t, bc, blk, 2, newKey(t).Address)...)
	if _, _, conf := mustTx(t, bc, tx.ID()); conf != 3 {
		t.Fatalf("onay %d, beklenen 3", conf)
	}

	if err := bc.Close(); err != nil {
		t.Fatal(err)
	}
	re := openTestChain(t, dir)
	if b, pos, conf := mustTx(t, re, tx.ID()); b.Hash != blk.Hash || pos != 1 || conf != 3 {
		t.Fatalf("yeniden açılış: blok %s sıra %d onay %d", b.Hash, pos, conf)
	}
	missingTx(t, re, signedTx(t, alice, bob.Address, Coin, 2).ID())
	if _, _, _, err := re.TxByID("lh"); !errors.Is(err, ErrUnknownTx) {
		t.Fatalf("geçersiz kimlik: %v", err)
	}
}

// Reorg'da yalnızca eski dalda kalan işlem indeksten düşer, yalnızca yeni dalda olan eklenir,
// iki dalda da olan (aynı madencinin aynı yükseklikteki coinbase'i ve yeniden kazılan işlem)
// yeni dalın bloğunu gösterir. Durum, kazanan zincirin baştan uygulanmasıyla aynıdır (indeks
// dahil, chainDump).
func TestTxIndexReorg(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, miner := newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)
	parent := pre[len(pre)-1]
	// Ücretli işlem: onu taşıyan bloğun coinbase'i aynı yükseklikteki boş bloğunkinden farklıdır.
	tx := signedTxFee(t, alice, bob.Address, Coin, MempoolMinFee, 1)

	a1 := blockOn(t, bc, parent, miner.Address, tx)
	mustAccept(t, bc, a1)
	if b, _, _ := mustTx(t, bc, tx.ID()); b.Hash != a1.Hash {
		t.Fatal("işlem a1'de değil")
	}

	// B dalı: aynı madenci, aynı yükseklik, işlemsiz → coinbase kimliği a1'inkinden farklı
	// (a1'in coinbase'i işlem ücretini de taşır); işlem yalnızca eski dalda kalır.
	side := extend(t, bc, parent, 2, miner.Address)
	mustAccept(t, bc, side...)
	if tipHash(t, bc) != side[1].Hash {
		t.Fatal("B dalı benimsenmedi")
	}
	missingTx(t, bc, tx.ID())
	missingTx(t, bc, a1.Transactions[0].ID())
	mustTx(t, bc, side[0].Transactions[0].ID())
	assertSameState(t, bc, replay(t, bc, with(pre, side...)...))

	// C dalı: işlem yeniden kazılır; C'nin ilk bloğu B'nin ilk bloğuyla aynı coinbase'i taşır.
	c1 := blockOn(t, bc, parent, miner.Address)
	c2 := blockOn(t, bc, c1, miner.Address, tx)
	c3 := blockOn(t, bc, c2, miner.Address)
	if c1.Transactions[0].ID() != side[0].Transactions[0].ID() {
		t.Fatal("test kurgusu: c1 ve b1 coinbase'leri aynı olmalı")
	}
	mustAccept(t, bc, c1, c2, c3)
	if tipHash(t, bc) != c3.Hash {
		t.Fatal("C dalı benimsenmedi")
	}
	if b, pos, conf := mustTx(t, bc, tx.ID()); b.Hash != c2.Hash || pos != 1 || conf != 2 {
		t.Fatalf("yeniden kazılan işlem: blok %s sıra %d onay %d", b.Hash, pos, conf)
	}
	if b, _, _ := mustTx(t, bc, c1.Transactions[0].ID()); b.Hash != c1.Hash {
		t.Fatalf("iki dalda ortak coinbase %s bloğunu gösteriyor, beklenen c1", b.Hash)
	}
	missingTx(t, bc, side[1].Transactions[0].ID())
	assertSameState(t, bc, replay(t, bc, with(pre, c1, c2, c3)...))
}

// Geçiş olmayan başarısız dal indeksi değiştirmez; geçerli öneke geçişte yalnızca önek
// indekslenir.
func TestTxIndexFailedAndPrefixSwitch(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, dave := newKey(t), newKey(t), newKey(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, alice.Address)
	mustAccept(t, bc, a1)

	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	bad := signedTx(t, alice, bob.Address, Coin, 1) // B dalında alice'in bakiyesi yok
	b3 := blockOn(t, bc, b2, dave.Address, bad)
	bc.mu.Lock()
	for _, b := range []*Block{b1, b2, b3} {
		storeOnly(t, bc, b)
	}
	err := bc.activateLocked(bc.index[b3.Hash])
	bc.mu.Unlock()
	if err == nil || tipHash(t, bc) != b2.Hash {
		t.Fatalf("öneke geçilmedi: %v", err)
	}
	mustTx(t, bc, b1.Transactions[0].ID())
	mustTx(t, bc, b2.Transactions[0].ID())
	missingTx(t, bc, b3.Transactions[0].ID())
	missingTx(t, bc, bad.ID())
	missingTx(t, bc, a1.Transactions[0].ID())

	// Uçtan az iş taşıyan geçersiz dal: geçiş yok, indeks aynı kalır.
	before := chainDump(t, bc)
	c1 := blockOn(t, bc, g, dave.Address, signedTx(t, bob, alice.Address, Coin, 1)) // bob'un parası yok
	bc.mu.Lock()
	storeOnly(t, bc, c1)
	err = bc.activateLocked(bc.index[c1.Hash])
	bc.mu.Unlock()
	if err == nil {
		t.Fatal("geçersiz dal kabul edildi")
	}
	after := chainDump(t, bc)
	if len(before) != len(after) {
		t.Fatalf("başarısız geçiş durumu değiştirdi: %d → %d anahtar", len(before), len(after))
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("başarısız geçiş %s anahtarını değiştirdi", k)
		}
	}
}

// Tutarsız indeks kaydı (bozuk değer, yanlış sıra, ana zincirde olmayan blok) bozuk durum
// olarak raporlanır, "bulunamadı" diye gizlenmez.
func TestTxIndexCorruptEntries(t *testing.T) {
	bc := newTestChain(t)
	miner := newKey(t).Address
	b1, err := bc.AddBlock(nil, miner)
	if err != nil {
		t.Fatal(err)
	}
	id := b1.Transactions[0].ID()
	zero := "0000000000000000000000000000000000000000000000000000000000000000"
	for _, v := range []string{
		`{bozuk`,
		`{"b":"` + b1.Hash + `","p":1}`,
		`{"b":"` + b1.Hash + `","p":-1}`,
		`{"b":"` + zero + `","p":0}`,
		`{"b":"lh","p":0}`,
	} {
		if err := bc.db.Put(txKey(id), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := bc.TxByID(id); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("%s: %v, beklenen ErrCorruptState", v, err)
		}
	}
}
