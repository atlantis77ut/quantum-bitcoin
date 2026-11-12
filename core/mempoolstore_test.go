package core

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/syndtr/goleveldb/leveldb/util"

	"quantum-bitcoin/crypto"
)

// openChainAt dir'deki zinciri (yoksa yeni) sabit artan saatle açar; kapatmak çağıranındır.
func openChainAt(t *testing.T, dir string, clockStart int64) *Blockchain {
	t.Helper()
	bc, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	clock := clockStart
	bc.now = func() int64 { clock++; return clock }
	t.Cleanup(func() { bc.Close() }) // zaten kapalıysa hata döner, zararsız
	return bc
}

// reopen zinciri kapatıp aynı dizinde yeniden açar ve boş bir havuz kurar.
func reopen(t *testing.T, bc *Blockchain, dir string, clockStart int64) (*Blockchain, *Mempool) {
	t.Helper()
	if err := bc.Close(); err != nil {
		t.Fatal(err)
	}
	nb := openChainAt(t, dir, clockStart)
	return nb, NewMempool(nb)
}

func mustLoad(t *testing.T, m *Mempool, wantRestored, wantSkipped int) {
	t.Helper()
	r, s, err := m.Load()
	if err != nil || r != wantRestored || s != wantSkipped {
		t.Fatalf("yükleme: geri %d (beklenen %d), atlanan %d (beklenen %d), err %v", r, wantRestored, s, wantSkipped, err)
	}
}

func inventory(t *testing.T, m *Mempool) []string {
	t.Helper()
	ids, err := m.Inventory(MempoolMaxTxs)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func snapshotKeys(t *testing.T, bc *Blockchain) int {
	t.Helper()
	it := bc.db.NewIterator(util.BytesPrefix([]byte(prefixMempoolTx)), nil)
	defer it.Release()
	n := 0
	for it.Next() {
		n++
	}
	return n
}

// fundedPool iki fonlanmış gönderenle (alice: 3 işlem, bob: 1 işlem) dolu bir havuz kurar.
func fundedPool(t *testing.T, bc *Blockchain) (*Mempool, *crypto.KeyPair, []Transaction) {
	t.Helper()
	m := NewMempool(bc)
	alice, bob, carol := newKey(t), newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	fund(t, bc, bob, 1)
	var txs []Transaction
	for n := uint64(1); n <= 3; n++ {
		txs = append(txs, mpTx(t, bc, alice, carol.Address, Coin, MempoolMinFee+int64(n), n))
	}
	txs = append(txs, mpTx(t, bc, bob, carol.Address, Coin, 5*MempoolMinFee, 1))
	for _, tx := range txs {
		mustAdd(t, m, tx)
	}
	return m, alice, txs
}

// Kayıt gerçek bir yeniden açılıştan sonra aynı işlemleri aynı sırayla geri getirir; değişiklik
// yoksa yeniden yazılmaz.
func TestMempoolSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	bc := openChainAt(t, dir, 1_900_000_000)
	m, _, _ := fundedPool(t, bc)
	want := inventory(t, m)
	if saved, err := m.Save(); err != nil || !saved {
		t.Fatalf("ilk kayıt: %v %v", saved, err)
	}
	if saved, err := m.Save(); err != nil || saved {
		t.Fatalf("değişmeyen havuz yeniden yazıldı: %v %v", saved, err)
	}
	if n := snapshotKeys(t, bc); n != len(want) {
		t.Fatalf("kayıtta %d işlem, beklenen %d", n, len(want))
	}

	bc2, m2 := reopen(t, bc, dir, 1_900_100_000)
	mustLoad(t, m2, len(want), 0)
	got := inventory(t, m2)
	if len(got) != len(want) {
		t.Fatalf("geri yüklenen %d, beklenen %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sıra %d: %s, beklenen %s", i, got[i], want[i])
		}
	}

	// Havuz boşalınca kayıt da boşalır.
	mineFromPool(t, bc2, m2)
	if poolLen(t, m2) != 0 {
		t.Fatal("havuz boşalmadı")
	}
	if saved, err := m2.Save(); err != nil || !saved {
		t.Fatalf("boş havuz kaydı: %v %v", saved, err)
	}
	if n := snapshotKeys(t, bc2); n != 0 {
		t.Fatalf("boş havuzdan sonra kayıtta %d işlem kaldı", n)
	}
}

// Kayıttan sonra onaylanan işlem yüklemede atlanır, gerisi gelir; atlama sonraki kaydı tetikler.
func TestMempoolLoadSkipsConfirmed(t *testing.T) {
	dir := t.TempDir()
	bc := openChainAt(t, dir, 1_900_000_000)
	m, _, txs := fundedPool(t, bc)
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := bc.AddBlock([]Transaction{txs[0]}, newKey(t).Address); err != nil {
		t.Fatal(err)
	}
	_, m2 := reopen(t, bc, dir, 1_900_100_000)
	mustLoad(t, m2, len(txs)-1, 1)
	if saved, err := m2.Save(); err != nil || !saved {
		t.Fatalf("atlamadan sonra kayıt yenilenmedi: %v %v", saved, err)
	}
}

// Kabul marjının içine düşmüş ama uç+1'de hâlâ geçerli işlem yüklemede kalır (havuzda kalma
// kuralı); aynı işlem yeni bir havuza Add ile alınmaz (kabul kuralı değişmedi).
func TestMempoolLoadKeepsInsideMargin(t *testing.T) {
	dir := t.TempDir()
	bc := openChainAt(t, dir, 1_900_000_000)
	m := NewMempool(bc)
	alice := newKey(t)
	fund(t, bc, alice, 1)
	tip := tipBlock(t, bc).Index
	tx := mpTxExp(t, alice, newKey(t).Address, Coin, MempoolMinFee, 1, tip+MempoolExpiryMargin)
	mustAdd(t, m, tx)
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // marjın içine düşer, süresi dolmaz
		if _, err := bc.AddBlock(nil, newKey(t).Address); err != nil {
			t.Fatal(err)
		}
	}
	bc2, m2 := reopen(t, bc, dir, 1_900_100_000)
	mustLoad(t, m2, 1, 0)
	if _, err := NewMempool(bc2).Add(tx); !errors.Is(err, ErrExpiryTooSoon) {
		t.Fatalf("taze kabul: %v, beklenen ErrExpiryTooSoon", err)
	}
}

// Bozuk, sahte imzalı ve başka ağa ait kayıtlar düğümü durdurmaz; yalnızca atlanır.
func TestMempoolLoadRejectsBadSnapshot(t *testing.T) {
	bc := newTestChain(t)
	m, _, txs := fundedPool(t, bc)
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	// Select sırası: 0 bob (en yüksek ücret), 1–3 alice. Yalnızca bob'un kaydı bozulur; alice'in
	// zinciri sağlam kaldığından tam olarak bir atlama beklenir.
	tampered := txs[3]
	tampered.Amount++ // imza artık tutmaz; başka her kural geçer
	data, err := json.Marshal(&tampered)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{data, []byte("{bozuk")} {
		if err := bc.db.Put(mempoolTxKey(0), bad, nil); err != nil {
			t.Fatal(err)
		}
		mustLoad(t, NewMempool(bc), len(txs)-1, 1)
	}

	if err := bc.db.Put([]byte(keyMempoolMeta), []byte(`{"version":1,"genesis":"baska"}`), nil); err != nil {
		t.Fatal(err)
	}
	mustLoad(t, NewMempool(bc), 0, len(txs))
	if err := bc.db.Put([]byte(keyMempoolMeta), []byte(`{"version":99,"genesis":"`+bc.params.GenesisHash+`"}`), nil); err != nil {
		t.Fatal(err)
	}
	mustLoad(t, NewMempool(bc), 0, len(txs))
}

// Düzenli kayıt eşzamanlı Add ile yarışmaz (-race) ve son kayıt havuzun son halini taşır.
func TestMempoolSaveConcurrentWithAdd(t *testing.T) {
	dir := t.TempDir()
	bc := openChainAt(t, dir, 1_900_000_000)
	m := NewMempool(bc)
	alice := newKey(t)
	fund(t, bc, alice, 1)
	txs := make([]Transaction, 10)
	for i := range txs {
		txs[i] = mpTx(t, bc, alice, newKey(t).Address, Coin/100, MempoolMinFee, uint64(i+1))
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for _, tx := range txs {
			if _, err := m.Add(tx); err != nil {
				t.Error(err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			if _, err := m.Save(); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if _, err := m.Save(); err != nil {
		t.Fatal(err)
	}
	_, m2 := reopen(t, bc, dir, 1_900_100_000)
	mustLoad(t, m2, len(txs), 0)
}
