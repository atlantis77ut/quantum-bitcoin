package core

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"

	"quantum-bitcoin/crypto"
)

// openTestChain dir'de, newTestChain ile aynı saat düzeniyle bir zincir açar.
func openTestChain(t *testing.T, dir string) *Blockchain {
	t.Helper()
	bc, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	clock := int64(1_900_000_000)
	bc.now = func() int64 { clock++; return clock }
	t.Cleanup(func() { bc.Close() })
	return bc
}

// blockOn parent'ın üzerine coinbase'i miner'a giden (ödül + ücretler) geçerli bir blok kazar.
func blockOn(t *testing.T, bc *Blockchain, parent *Block, miner string, txs ...Transaction) *Block {
	t.Helper()
	h := parent.Index + 1
	cb := Transaction{Sender: CoinbaseSender, Receiver: miner, Amount: BlockSubsidy(h, bc.params), IsCoinbase: true}
	for _, tx := range txs {
		cb.Amount += tx.Fee
	}
	b := &Block{Index: h, Timestamp: bc.now(), Transactions: append([]Transaction{cb}, txs...), PrevHash: parent.Hash}
	mineAt(t, bc, b)
	return b
}

// extend parent'ın üzerine n boş blok kazar (kabul etmez).
func extend(t *testing.T, bc *Blockchain, parent *Block, n int, miner string) []*Block {
	t.Helper()
	var out []*Block
	for i := 0; i < n; i++ {
		parent = blockOn(t, bc, parent, miner)
		out = append(out, parent)
	}
	return out
}

// prefix genesis üzerine kp'ye bir blok, ardından olgunluk süresince başka adrese boş bloklar
// kazıp kabul eder: kp'nin ödülü harcanabilir olur. Bloklar replay için döndürülür.
func prefix(t *testing.T, bc *Blockchain, kp *crypto.KeyPair) []*Block {
	t.Helper()
	first := blockOn(t, bc, genesisOf(t, bc), kp.Address)
	blocks := append([]*Block{first}, extend(t, bc, first, int(bc.params.CoinbaseMaturity), newKey(t).Address)...)
	mustAccept(t, bc, blocks...)
	return blocks
}

func with(pre []*Block, more ...*Block) []*Block {
	return append(append([]*Block{}, pre...), more...)
}

func mustAccept(t *testing.T, bc *Blockchain, blocks ...*Block) {
	t.Helper()
	for _, b := range blocks {
		if err := bc.AcceptBlock(b); err != nil {
			t.Fatalf("blok %d (%s): %v", b.Index, b.Hash, err)
		}
	}
}

func genesisOf(t *testing.T, bc *Blockchain) *Block {
	t.Helper()
	g, err := bc.getBlock(bc.main[0])
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func tipHash(t *testing.T, bc *Blockchain) string {
	t.Helper()
	tip, err := bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	return tip.Hash
}

// chainDump uzlaşma durumunu oluşturan bütün anahtarları döndürür: bakiyeler, nonce'lar,
// ana zincir yükseklik tablosu, undo kayıtları, işlem indeksi ve uç.
func chainDump(t *testing.T, bc *Blockchain) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range []string{prefixBalance, prefixNonce, prefixHeight, prefixUndo, prefixTx, prefixAddrTx, keyLastHash} {
		it := bc.db.NewIterator(util.BytesPrefix([]byte(p)), nil)
		for it.Next() {
			out[string(it.Key())] = string(it.Value())
		}
		it.Release()
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// replay kazanan zinciri boş bir düğüme baştan uygular (reorg'suz başvuru durumu).
func replay(t *testing.T, src *Blockchain, blocks ...*Block) *Blockchain {
	t.Helper()
	fresh := newTestChain(t)
	fresh.now = src.now
	mustAccept(t, fresh, blocks...)
	return fresh
}

// assertSameState reorg sonrası diskteki durumun, kazanan zincirin baştan uygulanmasıyla
// bayt bayt aynı olduğunu doğrular.
func assertSameState(t *testing.T, got, want *Blockchain) {
	t.Helper()
	g, w := chainDump(t, got), chainDump(t, want)
	var diff []string
	for k, v := range w {
		if g[k] != v {
			diff = append(diff, k)
		}
	}
	for k := range g {
		if _, ok := w[k]; !ok {
			diff = append(diff, k)
		}
	}
	sort.Strings(diff)
	if len(diff) > 0 {
		t.Fatalf("reorg sonrası durum baştan uygulamayla aynı değil; farklı anahtarlar: %v", diff)
	}
	if got.tip.Hash != want.tip.Hash || got.tip.work.Cmp(want.tip.work) != 0 || len(got.main) != len(want.main) {
		t.Fatal("uç ya da ana zincir tablosu farklı")
	}
}

func absent(t *testing.T, bc *Blockchain, key string) bool {
	t.Helper()
	_, ok, err := bc.getRaw(key)
	if err != nil {
		t.Fatal(err)
	}
	return !ok
}

// A→B→A reorg'u: her geçişten sonra durum, kazanan zincirin baştan uygulanmasıyla aynıdır;
// yalnızca kaybeden dalda görünen adresler iz bırakmaz; yeniden açılış aynı durumu verir.
func TestReorgMatchesReplay(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	alice, bob, carol, dave := newKey(t), newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)

	a1 := blockOn(t, bc, pre[len(pre)-1], alice.Address)
	mustAccept(t, bc, a1)
	a2 := blockOn(t, bc, a1, alice.Address, signedTxFee(t, alice, bob.Address, 2*Coin, 1, 1))
	a3 := blockOn(t, bc, a2, carol.Address)
	mustAccept(t, bc, a2, a3)

	b2 := blockOn(t, bc, a1, dave.Address, signedTx(t, alice, dave.Address, 3*Coin, 1))
	b3 := blockOn(t, bc, b2, dave.Address)
	b4 := blockOn(t, bc, b3, dave.Address)
	mustAccept(t, bc, b2)
	if tipHash(t, bc) != a3.Hash {
		t.Fatal("daha az iş taşıyan yan blok uca geçti")
	}
	// Eşit iş: ilk görülen kalır, yan blok yalnızca saklanır.
	mustAccept(t, bc, b3)
	if tipHash(t, bc) != a3.Hash {
		t.Fatal("eşit işte uç değişti (ilk görülen kalmalı)")
	}
	if e, ok := bc.index[b3.Hash]; !ok || e.Status != statusStored || bc.isMain(e) {
		t.Fatal("eşit işli blok saklanmadı ya da ana zincire alındı")
	}
	mustAccept(t, bc, b4)
	if tipHash(t, bc) != b4.Hash {
		t.Fatal("daha çok iş taşıyan zincire geçilmedi")
	}
	if !absent(t, bc, prefixBalance+bob.Address) || !absent(t, bc, prefixBalance+carol.Address) {
		t.Fatal("kaybeden dalda görünen adres reorg sonrası durumda kaldı")
	}
	// dave'in coinbase'leri henüz olgunlaşmadı; yalnızca aldığı işlem harcanabilir.
	if n, _ := bc.GetNonce(alice.Address); n != 1 || mustBalance(t, bc, dave.Address) != 3*Coin {
		t.Fatalf("kazanan dal doğru uygulanmadı (nonce %d)", n)
	}
	assertSameState(t, bc, replay(t, bc, with(pre, a1, b2, b3, b4)...))
	if bc.DeepReorgAlarm() {
		t.Fatal("sığ reorg alarm verdi")
	}

	// Geri dönüş: A uzar ve yeniden kazanır.
	a4 := blockOn(t, bc, a3, carol.Address)
	a5 := blockOn(t, bc, a4, carol.Address)
	mustAccept(t, bc, a4)
	if tipHash(t, bc) != b4.Hash {
		t.Fatal("eşit işte uç değişti")
	}
	mustAccept(t, bc, a5)
	if tipHash(t, bc) != a5.Hash || !absent(t, bc, prefixBalance+dave.Address) {
		t.Fatal("A'ya geri dönülmedi ya da B'nin izi kaldı")
	}
	want := replay(t, bc, with(pre, a1, a2, a3, a4, a5)...)
	assertSameState(t, bc, want)

	// Yeniden açılış aynı indeksi ve durumu verir.
	nIndex := len(bc.index)
	now := bc.now
	bc.Close()
	bc2, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc2.Close() })
	bc2.now = now
	assertSameState(t, bc2, want)
	if len(bc2.index) != nIndex || bc2.index[b4.Hash].Status != statusValid || bc2.isMain(bc2.index[b4.Hash]) {
		t.Fatal("indeks yeniden açılışta korunmadı")
	}
	if e := bc2.index[a1.Hash]; e.CbTo != alice.Address || e.CbValue != BlockSubsidy(a1.Index, bc2.params) {
		t.Fatalf("coinbase bilgisi yeniden açılışta korunmadı: %+v", e)
	}
	// Yeniden açılan düğüm zinciri uzatmaya devam edebilir.
	mustAccept(t, bc2, blockOn(t, bc2, a5, carol.Address))
}

// Kaybeden daldaki işlem kazanan dala yeniden alınabilir ama iki kez uygulanamaz.
func TestLosingBranchTxReincludedOnce(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, carol, dave := newKey(t), newKey(t), newKey(t), newKey(t)
	pre := prefix(t, bc, alice)
	a1 := pre[len(pre)-1]
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	a2 := blockOn(t, bc, a1, carol.Address, tx)
	mustAccept(t, bc, a2)

	b2 := blockOn(t, bc, a1, dave.Address, tx)
	b3 := blockOn(t, bc, b2, dave.Address)
	mustAccept(t, bc, b2, b3)
	if tipHash(t, bc) != b3.Hash {
		t.Fatal("reorg olmadı")
	}
	if mustBalance(t, bc, bob.Address) != Coin || mustBalance(t, bc, alice.Address) != testReward-Coin {
		t.Fatal("işlem kazanan dalda bir kez uygulanmadı")
	}
	b4 := blockOn(t, bc, b3, dave.Address, tx)
	if err := bc.AcceptBlock(b4); !errors.Is(err, ErrBadNonce) {
		t.Fatalf("aynı işlem ikinci kez: %v, beklenen ErrBadNonce", err)
	}
	if tipHash(t, bc) != b3.Hash || mustBalance(t, bc, bob.Address) != Coin {
		t.Fatal("tekrarlanan işlem durumu değiştirdi")
	}
	assertSameState(t, bc, replay(t, bc, with(pre, b2, b3)...))
}

// Yeni dalın ortasındaki blok durum kuralını ihlal ederse o blok ve torunları geçersiz
// işaretlenir; geçerli önek uçtan fazla iş taşımıyorsa uç değişmez. Torunlar imza
// doğrulamasına girmeden reddedilir; dalın dürüst uzantısı sonra kazanabilir.
func TestInvalidBlockInBranchKeepsTip(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, carol, dave := newKey(t), newKey(t), newKey(t), newKey(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, alice.Address)
	a2 := blockOn(t, bc, a1, carol.Address)
	mustAccept(t, bc, a1, a2)

	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	// B dalında alice'in bakiyesi yok: gövde geçerli, durum geçersiz.
	b3 := blockOn(t, bc, b2, dave.Address, signedTx(t, alice, bob.Address, Coin, 1))
	b4 := blockOn(t, bc, b3, dave.Address)
	mustAccept(t, bc, b1, b2)
	if err := bc.AcceptBlock(b3); !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("geçersiz dal bloğu: %v", err)
	}
	if tipHash(t, bc) != a2.Hash || bc.index[b3.Hash].Status != statusInvalid {
		t.Fatal("geçersiz dal ucu değiştirdi ya da işaretlenmedi")
	}
	if bc.isMain(bc.index[b1.Hash]) || mustBalance(t, bc, dave.Address) != 0 {
		t.Fatal("başarısız geçişten durum sızdı")
	}

	checks := bc.bodyChecks.Load()
	if err := bc.AcceptBlock(b4); !errors.Is(err, ErrKnownInvalid) {
		t.Fatalf("geçersiz bloğun çocuğu: %v", err)
	}
	if err := bc.AcceptBlock(b3); !errors.Is(err, ErrKnownInvalid) {
		t.Fatalf("geçersiz blok tekrar: %v", err)
	}
	if bc.bodyChecks.Load() != checks {
		t.Fatal("geçersiz dal yeniden doğrulandı")
	}
	if _, ok := bc.index[b4.Hash]; ok {
		t.Fatal("geçersiz bloğun çocuğu saklandı")
	}

	b3ok := blockOn(t, bc, b2, dave.Address)
	mustAccept(t, bc, b3ok)
	if tipHash(t, bc) != b3ok.Hash {
		t.Fatal("dalın dürüst uzantısı kazanmadı")
	}
	assertSameState(t, bc, replay(t, bc, b1, b2, b3ok))
}

// storeOnly bloğu zincire geçmeden indekse ve diske yazar (çökme ya da toplu varış taklidi).
func storeOnly(t *testing.T, bc *Blockchain, b *Block) {
	t.Helper()
	parent, err := bc.precheckLocked(b)
	if err != nil {
		t.Fatal(err)
	}
	e, err := newIndexEntry(b, parent)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	batch := new(leveldb.Batch)
	batch.Put([]byte(b.Hash), data)
	if err := bc.putEntry(batch, e); err != nil {
		t.Fatal(err)
	}
	if err := bc.db.Write(batch, nil); err != nil {
		t.Fatal(err)
	}
	bc.addToIndex(e)
}

// Geçerli önek uçtan fazla iş taşıyorsa zincir öneke geçer; geçersiz blok işaretlenir.
func TestInvalidBlockInBranchPrefixWins(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, dave := newKey(t), newKey(t), newKey(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, alice.Address)
	mustAccept(t, bc, a1)

	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	b3 := blockOn(t, bc, b2, dave.Address, signedTx(t, alice, bob.Address, Coin, 1))
	bc.mu.Lock()
	for _, b := range []*Block{b1, b2, b3} {
		storeOnly(t, bc, b)
	}
	err := bc.activateLocked(bc.index[b3.Hash])
	bc.mu.Unlock()
	if !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("hata %v, beklenen ErrInsufficientBal", err)
	}
	if tipHash(t, bc) != b2.Hash || bc.index[b3.Hash].Status != statusInvalid {
		t.Fatal("geçerli öneke geçilmedi ya da geçersiz blok işaretlenmedi")
	}
	assertSameState(t, bc, replay(t, bc, b1, b2))
}

// Blok saklanıp zincire geçilmeden düğüm çökerse açılışta en çok iş taşıyan geçerli zincire
// geçilir.
func TestRecoverBestOnReopen(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	// Açılıştaki kurtarma gerçek saatle çalışır (FTL): bloklar gerçek saatin gerisinde olsun.
	g := genesisOf(t, bc)
	clock := max(time.Now().Unix()-1000, g.Timestamp)
	bc.now = func() int64 { clock++; return clock }
	alice, dave := newKey(t), newKey(t)
	mustAccept(t, bc, blockOn(t, bc, g, alice.Address))
	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	bc.mu.Lock()
	storeOnly(t, bc, b1)
	storeOnly(t, bc, b2)
	bc.mu.Unlock()
	now := bc.now
	bc.Close()

	bc2, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc2.Close() })
	bc2.now = now
	if tipHash(t, bc2) != b2.Hash {
		t.Fatal("açılışta en çok iş taşıyan zincire geçilmedi")
	}
	assertSameState(t, bc2, replay(t, bc2, b1, b2))
}

// Bilinen yan blok yeniden gönderilince ve yan zincir sınırına takılan blok imza
// doğrulamasına girmez; sınıra takılan blok önbelleğe alınmaz.
func TestSideBlocksNotReverified(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	mustAccept(t, bc, blockOn(t, bc, g, newKey(t).Address))
	side := blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, side)

	checks := bc.bodyChecks.Load()
	if err := bc.AcceptBlock(side); !errors.Is(err, ErrKnownBlock) {
		t.Fatalf("bilinen yan blok: %v", err)
	}
	if bc.bodyChecks.Load() != checks {
		t.Fatal("bilinen yan blok yeniden doğrulandı")
	}
	for i := 2; i < MaxBlocksPerHeight; i++ {
		mustAccept(t, bc, blockOn(t, bc, g, newKey(t).Address))
	}
	checks = bc.bodyChecks.Load()
	over := blockOn(t, bc, g, newKey(t).Address)
	if err := bc.AcceptBlock(over); !errors.Is(err, ErrSideChainLimit) {
		t.Fatalf("yükseklik sınırı: %v", err)
	}
	if bc.bodyChecks.Load() != checks || bc.rejected.has(over.Hash) {
		t.Fatal("sınıra takılan blok doğrulandı ya da önbelleğe alındı")
	}
	if _, ok := bc.index[over.Hash]; ok {
		t.Fatal("sınıra takılan blok saklandı")
	}
}

// Uçtan MaxSideForkDepth'ten derin çatallanan ve fazla iş taşımayan yan blok saklanmaz.
func TestDeepSideForkNotStored(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	miner := newKey(t)
	for i := 0; i < MaxSideForkDepth+1; i++ {
		if _, err := bc.AddBlock(nil, miner.Address); err != nil {
			t.Fatal(err)
		}
	}
	checks := bc.bodyChecks.Load()
	deep := blockOn(t, bc, g, newKey(t).Address)
	if err := bc.AcceptBlock(deep); !errors.Is(err, ErrSideChainLimit) {
		t.Fatalf("derin yan blok: %v", err)
	}
	if bc.bodyChecks.Load() != checks {
		t.Fatal("derin yan blok doğrulandı")
	}
	// Sınırın içindeki çatal saklanır.
	near := blockOn(t, bc, bc.mustMainBlock(t, 1), newKey(t).Address)
	mustAccept(t, bc, near)
}

func (bc *Blockchain) mustMainBlock(t *testing.T, h int64) *Block {
	t.Helper()
	b, err := bc.getBlock(bc.main[h])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Ebeveyni bilinmeyen blok önbelleğe alınmaz; ebeveyn gelince kabul edilir.
func TestOrphanNotCached(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, newKey(t).Address)
	a2 := blockOn(t, bc, a1, newKey(t).Address)
	if err := bc.AcceptBlock(a2); !errors.Is(err, ErrBadPrevHash) {
		t.Fatalf("yetim blok: %v", err)
	}
	if bc.rejected.has(a2.Hash) {
		t.Fatal("yetim blok ret önbelleğine yazıldı")
	}
	mustAccept(t, bc, a1, a2)
	if tipHash(t, bc) != a2.Hash {
		t.Fatal("ebeveyn gelince yetim blok kabul edilmedi")
	}
}

// Params.ReorgAlarmDepth'ten derin reorg zinciri değiştirir (uzlaşma sınırı yok) ama alarm verir;
// tam eşikteki reorg alarm vermez.
func TestDeepReorgAlarm(t *testing.T) {
	for _, tc := range []struct {
		depth int
		alarm bool
	}{{int(testParams.ReorgAlarmDepth), false}, {int(testParams.ReorgAlarmDepth) + 1, true}} {
		bc := newTestChain(t)
		g := genesisOf(t, bc)
		main := extend(t, bc, g, tc.depth, newKey(t).Address)
		mustAccept(t, bc, main...)
		side := extend(t, bc, g, tc.depth+1, newKey(t).Address)
		mustAccept(t, bc, side[:tc.depth]...)
		if bc.DeepReorgAlarm() || tipHash(t, bc) != main[len(main)-1].Hash {
			t.Fatal("reorg öncesi uç değişti ya da alarm verildi")
		}
		mustAccept(t, bc, side[tc.depth])
		if tipHash(t, bc) != side[tc.depth].Hash {
			t.Fatalf("derinlik %d: daha çok iş taşıyan zincire geçilmedi", tc.depth)
		}
		if bc.DeepReorgAlarm() != tc.alarm {
			t.Fatalf("derinlik %d: alarm %v, beklenen %v", tc.depth, bc.DeepReorgAlarm(), tc.alarm)
		}
		assertSameState(t, bc, replay(t, bc, side...))
	}
}

// Geçersizlik dalın bütün saklı torunlarına yayılır ve yükseklik yuvalarını serbest bırakır.
func TestInvalidMarkPropagatesToDescendants(t *testing.T) {
	bc := newTestChain(t)
	alice, bob, dave := newKey(t), newKey(t), newKey(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, alice.Address)
	a2 := blockOn(t, bc, a1, alice.Address)
	mustAccept(t, bc, a1, a2)

	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	b3 := blockOn(t, bc, b2, dave.Address, signedTx(t, alice, bob.Address, Coin, 1))
	b4 := blockOn(t, bc, b3, dave.Address)
	b5 := blockOn(t, bc, b4, dave.Address)
	bc.mu.Lock()
	for _, b := range []*Block{b1, b2, b3, b4, b5} {
		storeOnly(t, bc, b)
	}
	err := bc.activateLocked(bc.index[b5.Hash])
	bc.mu.Unlock()
	if !errors.Is(err, ErrInsufficientBal) {
		t.Fatalf("hata %v, beklenen ErrInsufficientBal", err)
	}
	for _, b := range []*Block{b3, b4, b5} {
		if bc.index[b.Hash].Status != statusInvalid || !bc.rejected.has(b.Hash) {
			t.Fatalf("blok %d geçersiz işaretlenmedi", b.Index)
		}
	}
	if bc.index[b2.Hash].Status == statusInvalid || tipHash(t, bc) != a2.Hash {
		t.Fatal("geçerli önek işaretlendi ya da uç değişti")
	}
	if bc.perHeight[3] != 0 || bc.perHeight[5] != 0 {
		t.Fatalf("geçersiz bloklar yükseklik yuvası tutuyor: %v", bc.perHeight)
	}
}

// Dal içindeki geçici hata (burada gelecek tarihli blok) bloğu geçersiz işaretlemez; blok
// yeniden gönderilince geçiş imza doğrulaması yapılmadan yeniden denenir.
func TestTransientBranchErrorNotInvalid(t *testing.T) {
	bc := newTestChain(t)
	dave := newKey(t)
	g := genesisOf(t, bc)
	mustAccept(t, bc, blockOn(t, bc, g, newKey(t).Address))
	b1 := blockOn(t, bc, g, dave.Address)
	b2 := blockOn(t, bc, b1, dave.Address)
	b2.Timestamp = bc.now() + MaxFutureDrift + 100
	mineAt(t, bc, b2)

	bc.mu.Lock()
	storeOnly(t, bc, b1)
	storeOnly(t, bc, b2)
	err := bc.activateLocked(bc.index[b2.Hash])
	bc.mu.Unlock()
	if !errors.Is(err, ErrTimestampFuture) {
		t.Fatalf("hata %v, beklenen ErrTimestampFuture", err)
	}
	if bc.index[b2.Hash].Status != statusStored || bc.rejected.has(b2.Hash) || tipHash(t, bc) == b2.Hash {
		t.Fatal("gelecek tarihli blok geçersiz işaretlendi ya da zincire geçildi")
	}

	fixedClock(bc, b2.Timestamp)
	checks := bc.bodyChecks.Load()
	if err := bc.AcceptBlock(b2); err != nil {
		t.Fatalf("yeniden deneme: %v", err)
	}
	if tipHash(t, bc) != b2.Hash || bc.bodyChecks.Load() != checks {
		t.Fatal("geçiş yeniden denenmedi ya da gövde yeniden doğrulandı")
	}
	assertSameState(t, bc, replay(t, bc, b1, b2))
}

// Durumca geçersiz bloklar (uç üzerine kurulsalar bile) yükseklik yuvası tüketmez: dürüst
// yan bloklar MaxBlocksPerHeight'e kadar saklanabilir.
func TestInvalidBlocksFreeHeightSlots(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	g := genesisOf(t, bc)
	a1 := blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, a1)
	for i := 0; i < MaxBlocksPerHeight; i++ {
		bad := blockOn(t, bc, a1, newKey(t).Address, signedTx(t, alice, bob.Address, Coin, 1))
		if err := bc.AcceptBlock(bad); !errors.Is(err, ErrInsufficientBal) {
			t.Fatalf("geçersiz blok %d: %v", i, err)
		}
	}
	mustAccept(t, bc, blockOn(t, bc, a1, newKey(t).Address))
	for i := 1; i < MaxBlocksPerHeight; i++ {
		mustAccept(t, bc, blockOn(t, bc, a1, newKey(t).Address))
	}
}

// Olgunluk sınırı: H bloğunun coinbase'i H+M−1'de harcanamaz, H+M'de işlemlerden önce
// alacaklandırılır ve aynı blokta harcanabilir (ağdan gelen blokta ve yerel madencilikte).
// Genesis hiçbir zaman alacaklandırılmaz.
func TestCoinbaseMaturityBoundary(t *testing.T) {
	m := RegtestParams.CoinbaseMaturity
	for _, local := range []bool{false, true} {
		bc := newTestChain(t)
		miner, bob, other := newKey(t), newKey(t), newKey(t)
		g := genesisOf(t, bc)
		b1 := blockOn(t, bc, g, miner.Address)
		mustAccept(t, bc, b1)
		tip := b1
		for _, b := range extend(t, bc, b1, int(m-2), other.Address) {
			mustAccept(t, bc, b)
			tip = b
		}
		// H+M−1: ödül henüz harcanamaz.
		early := blockOn(t, bc, tip, other.Address, signedTx(t, miner, bob.Address, Coin, 1))
		if err := bc.AcceptBlock(early); !errors.Is(err, ErrInsufficientBal) {
			t.Fatalf("olgunlaşmamış coinbase harcandı: %v", err)
		}
		tip = blockOn(t, bc, tip, other.Address)
		mustAccept(t, bc, tip)
		if tip.Index != m || !absent(t, bc, prefixBalance+miner.Address) {
			t.Fatalf("yükseklik %d: coinbase olgunlaşmadan alacaklandırıldı", tip.Index)
		}
		spend := signedTx(t, miner, bob.Address, Coin, 1)
		if local {
			mined, err := bc.AddBlock([]Transaction{spend}, other.Address)
			if err != nil || len(mined.Transactions) != 2 {
				t.Fatalf("yerel madenci olgunlaşan parayı harcayan işlemi almadı: %v", err)
			}
		} else {
			mustAccept(t, bc, blockOn(t, bc, tip, other.Address, spend))
		}
		if mustBalance(t, bc, miner.Address) != BlockSubsidy(1, bc.params)-Coin || mustBalance(t, bc, bob.Address) != Coin {
			t.Fatal("olgunlaşan coinbase doğru alacaklandırılmadı")
		}
		if !absent(t, bc, prefixBalance+RegtestParams.GenesisReceiver()) {
			t.Fatal("genesis alıcısına bakiye yazıldı")
		}
	}
}

// Olgunluk sınırını aşan reorg: çatal H'nin altında; yeni dalın H bloğunu başka madenci
// kazdı. Olgunlaşan ödül kazanan dalın madencisine gider, kaybedeninki geri alınır; geri
// dönüşte tersi olur. Ata bc.main'den değil indeksten bulunmalı (aksi halde yanlış madenci).
func TestReorgAcrossMaturity(t *testing.T) {
	bc := newTestChain(t)
	m := int(bc.params.CoinbaseMaturity)
	minerA, minerB, other := newKey(t), newKey(t), newKey(t)
	g := genesisOf(t, bc)

	a := []*Block{blockOn(t, bc, g, minerA.Address)}
	a = append(a, extend(t, bc, a[0], m, other.Address)...) // yükseklik m+1: A1 olgun
	mustAccept(t, bc, a...)
	reward := BlockSubsidy(1, bc.params)
	if mustBalance(t, bc, minerA.Address) != reward {
		t.Fatal("A1 olgunlaşmadı")
	}

	b := []*Block{blockOn(t, bc, g, minerB.Address)}
	b = append(b, extend(t, bc, b[0], m+1, other.Address)...) // bir blok daha uzun
	mustAccept(t, bc, b...)
	if tipHash(t, bc) != b[len(b)-1].Hash {
		t.Fatal("B dalına geçilmedi")
	}
	if !absent(t, bc, prefixBalance+minerA.Address) || mustBalance(t, bc, minerB.Address) != reward {
		t.Fatal("olgunlaşan ödül reorg'da doğru madenciye geçmedi")
	}
	assertSameState(t, bc, replay(t, bc, b...))

	a = append(a, extend(t, bc, a[len(a)-1], 2, other.Address)...)
	mustAccept(t, bc, a[len(a)-2:]...)
	if tipHash(t, bc) != a[len(a)-1].Hash || mustBalance(t, bc, minerA.Address) != reward ||
		!absent(t, bc, prefixBalance+minerB.Address) {
		t.Fatal("A'ya geri dönüşte olgun ödül geri alınmadı")
	}
	assertSameState(t, bc, replay(t, bc, a...))
}

// Yalnızca H+M bloğunu geri saran reorg olgunluk alacağını geri alır: yeni dalda aynı
// yükseklik yeniden alacaklandırır, öncesinde ise bakiye yoktur.
func TestMaturityCreditRolledBack(t *testing.T) {
	bc := newTestChain(t)
	m := int(bc.params.CoinbaseMaturity)
	miner := newKey(t)
	g := genesisOf(t, bc)
	main := []*Block{blockOn(t, bc, g, miner.Address)}
	main = append(main, extend(t, bc, main[0], m, newKey(t).Address)...) // son blok A1'i olgunlaştırır
	mustAccept(t, bc, main...)
	fork := main[m-1] // yükseklik m: A1 henüz olgun değil

	bc.mu.Lock()
	ov := newOverlay(bc)
	for _, u := range mustUndo(t, bc, main[m].Hash) {
		ov.set(u.Key, u.Prev)
	}
	bc.mu.Unlock()
	if _, ok, _ := ov.getRaw(prefixBalance + miner.Address); ok {
		t.Fatal("H+M bloğunun undo kaydı olgunluk alacağını geri almıyor")
	}

	alt := extend(t, bc, fork, 2, newKey(t).Address)
	mustAccept(t, bc, alt...)
	if tipHash(t, bc) != alt[1].Hash || mustBalance(t, bc, miner.Address) != BlockSubsidy(1, bc.params) {
		t.Fatal("yeni dal olgun ödülü bir kez alacaklandırmadı")
	}
	assertSameState(t, bc, replay(t, bc, with(main[:m], alt...)...))
}

func mustUndo(t *testing.T, bc *Blockchain, hash string) []undoItem {
	t.Helper()
	u, err := bc.loadUndo(hash)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
