package core

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// rewriteBody hash anahtarındaki gövdeyi mut ile değiştirip yeniden yazar ve eski baytları
// döndürür: özet alanına dokunmayan değişiklik çözülebilen ama başlığıyla tutmayan bir gövde
// bırakır (diskteki ya da bellekteki bozulmanın taklidi).
func rewriteBody(t *testing.T, bc *Blockchain, hash string, mut func(*Block)) []byte {
	t.Helper()
	saved, err := bc.db.Get([]byte(hash), nil)
	if err != nil {
		t.Fatal(err)
	}
	var b Block
	if err := json.Unmarshal(saved, &b); err != nil {
		t.Fatal(err)
	}
	mut(&b)
	if err := bc.db.Put([]byte(hash), mustJSON(t, &b), nil); err != nil {
		t.Fatal(err)
	}
	return saved
}

// noInvalid indeksteki hiçbir bloğun geçersiz işaretlenmediğini ve ret önbelleğine girmediğini
// doğrular.
func noInvalid(t *testing.T, bc *Blockchain) {
	t.Helper()
	for _, e := range bc.index {
		if e.Status == statusInvalid || bc.rejected.has(e.Hash) {
			t.Fatalf("blok %d (%s) geçersiz işaretlendi", e.Height, e.Hash)
		}
	}
}

// Zincire bağlanacak bloğun gövdesi diskte bozuksa (çözülemiyor ya da başlığıyla tutmuyor) eksik
// gövdeyle birebir aynı davranılır: geçiş olmaz, durum değişmez, hiçbir blok geçersiz
// işaretlenmez ve kalıcı alarm corrupt_body türüyle verilir. Aynı olay yeniden yazılmaz; gövde
// düzelince geçiş olur, alarm kalır ve yeniden açılışta sürer. Bu denetimden önce ilk üç bozulma
// sessizce zincire uygulanır (işlem indeksi yanlış kimlikle kurulur), dördüncüsü dürüst bloğu
// torunlarıyla kalıcı geçersiz işaretler, beşincisi alarmsız ErrCorruptState dönerdi.
func TestCorruptBranchBodyAlarm(t *testing.T) {
	// Ana zincir g-m1-m2, daha çok işli dal g-s1-s2-s3; s1'in gövdesi bozulur.
	for _, c := range []struct {
		name string
		mut  func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block)
	}{
		{"coinbase tutarı değişmiş", func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block) {
			rewriteBody(t, bc, s1.Hash, func(b *Block) { b.Transactions[0].Amount++ })
		}},
		{"coinbase alıcısı değişmiş", func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block) {
			rewriteBody(t, bc, s1.Hash, func(b *Block) { b.Transactions[0].Receiver = main[0].Transactions[0].Receiver })
		}},
		{"başlık zamanı değişmiş", func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block) {
			rewriteBody(t, bc, s1.Hash, func(b *Block) { b.Timestamp++ })
		}},
		{"başka yükseklikteki bloğun gövdesi", func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block) {
			if err := bc.db.Put([]byte(s1.Hash), mustJSON(t, main[1]), nil); err != nil {
				t.Fatal(err)
			}
		}},
		{"çözülemeyen gövde", func(t *testing.T, bc *Blockchain, s1 *Block, main []*Block) {
			if err := bc.db.Put([]byte(s1.Hash), []byte("{"), nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bc := openTestChain(t, dir)
			g := genesisOf(t, bc)
			main := extend(t, bc, g, 2, newKey(t).Address)
			mustAccept(t, bc, main...)
			side := extend(t, bc, g, 3, newKey(t).Address)
			s1, last := side[0], side[2]
			mustAccept(t, bc, side[:2]...) // eşit iş: uç değişmez
			saved, err := bc.db.Get([]byte(s1.Hash), nil)
			if err != nil {
				t.Fatal(err)
			}
			c.mut(t, bc, s1, main)
			before := chainDump(t, bc)

			err = bc.AcceptBlock(last)
			if !errors.Is(err, ErrBlockDataMissing) || !errors.Is(err, ErrCorruptState) {
				t.Fatalf("hata %v, beklenen bozuk gövde", err)
			}
			if tipHash(t, bc) != main[1].Hash || !reflect.DeepEqual(chainDump(t, bc), before) {
				t.Fatal("bozuk gövdeyle uç ya da durum değişti")
			}
			noInvalid(t, bc)
			rec := bc.dataAlarm
			if rec == nil || rec.Hash != s1.Hash || rec.Kind != dataCorruptBody || rec.Target != last.Hash ||
				rec.Tip != main[1].Hash || rec.Count != 1 || !bc.MissingDataAlarm() {
				t.Fatalf("alarm kaydı %+v", rec)
			}

			// Aynı olay yeniden yazılmaz (sayı artmaz).
			if err := bc.AcceptBlock(last); !errors.Is(err, ErrBlockDataMissing) {
				t.Fatalf("yeniden gönderim: %v", err)
			}
			if bc.dataAlarm.Count != 1 {
				t.Fatalf("aynı olay sayıyı artırdı: %d", bc.dataAlarm.Count)
			}

			// Gövde düzelince geçiş olur (hiçbir şey yanlışlıkla geçersiz işaretlenmemişti).
			if err := bc.db.Put([]byte(s1.Hash), saved, nil); err != nil {
				t.Fatal(err)
			}
			if err := bc.AcceptBlock(last); err != nil {
				t.Fatalf("gövde düzeldikten sonra: %v", err)
			}
			if tipHash(t, bc) != last.Hash || !bc.MissingDataAlarm() {
				t.Fatal("gövde düzeldikten sonra dala geçilmedi ya da alarm kendiliğinden silindi")
			}
			assertSameState(t, bc, replay(t, bc, side...))
			bc.Close()

			bc2 := openTestChain(t, dir)
			got, err := bc2.ClearMissingDataAlarm()
			if err != nil || got == nil || got.Hash != s1.Hash || got.Kind != dataCorruptBody {
				t.Fatalf("yeniden açılışta alarm kaydı %+v, %v", got, err)
			}
		})
	}
}

// Para: yan daldaki bir transferin diskteki tutarı ya da nonce'u bozulursa bozuk işlem zincire
// uygulanmaz ve dürüst blok geçersiz işaretlenmez. Bu denetimden önce bozuk tutar, imza yeniden
// doğrulanmadığı için bakiyelere yazılır; bozuk nonce ise bloğu torunlarıyla kalıcı geçersiz
// yapardı.
func TestCorruptTransferNotApplied(t *testing.T) {
	for _, c := range []struct {
		name string
		mut  func(tx *Transaction)
	}{
		{"tutar iki katı", func(tx *Transaction) { tx.Amount = 2 * Coin }},
		{"nonce kaymış", func(tx *Transaction) { tx.Nonce = 7 }},
	} {
		t.Run(c.name, func(t *testing.T) {
			bc := newTestChain(t)
			alice, bob := newKey(t), newKey(t)
			pre := prefix(t, bc, alice) // alice'in ilk ödülü harcanabilir
			base := pre[len(pre)-1]
			m1 := blockOn(t, bc, base, newKey(t).Address)
			mustAccept(t, bc, m1)
			s1 := blockOn(t, bc, base, newKey(t).Address, signedTx(t, alice, bob.Address, Coin, 1))
			mustAccept(t, bc, s1) // eşit iş: yan blok olarak saklanır
			if tipHash(t, bc) != m1.Hash {
				t.Fatal("eşit işli yan blok ucu değiştirdi")
			}
			saved := rewriteBody(t, bc, s1.Hash, func(b *Block) { c.mut(&b.Transactions[1]) })
			funds := mustBalance(t, bc, alice.Address)

			s2 := blockOn(t, bc, s1, newKey(t).Address)
			if err := bc.AcceptBlock(s2); !errors.Is(err, ErrBlockDataMissing) || !errors.Is(err, ErrCorruptState) {
				t.Fatalf("hata %v, beklenen bozuk gövde", err)
			}
			if tipHash(t, bc) != m1.Hash || mustBalance(t, bc, bob.Address) != 0 ||
				mustBalance(t, bc, alice.Address) != funds {
				t.Fatal("bozuk transfer zincire uygulandı")
			}
			noInvalid(t, bc)
			if rec := bc.dataAlarm; rec == nil || rec.Hash != s1.Hash || rec.Kind != dataCorruptBody {
				t.Fatalf("alarm kaydı %+v", rec)
			}

			if err := bc.db.Put([]byte(s1.Hash), saved, nil); err != nil {
				t.Fatal(err)
			}
			if err := bc.AcceptBlock(s2); err != nil {
				t.Fatalf("gövde düzeldikten sonra: %v", err)
			}
			if n, _ := bc.GetNonce(alice.Address); tipHash(t, bc) != s2.Hash || n != 1 ||
				mustBalance(t, bc, bob.Address) != Coin || mustBalance(t, bc, alice.Address) != funds-Coin {
				t.Fatalf("düzelen dal doğru uygulanmadı (nonce %d)", n)
			}
			assertSameState(t, bc, replay(t, bc, with(pre, s1, s2)...))
		})
	}
}

// Bozuk gövdeli dala geçiş denemesi ya hep ya hiçtir (eksik gövdeyle aynı): bozuk bloktan önceki
// sağlam bloklar uçtan ağır olsa da o denemede onlara geçilmez. Sağlam önek kendi bloğu yeniden
// gelince bağlanır (açılışta ayrı aday olarak: TestRecoverCorruptBodyAlarm); hiçbir blok geçersiz
// işaretlenmediğinden gövde düzelince dalın kalanına da geçilir.
func TestCorruptBodyPrefixNotActivated(t *testing.T) {
	bc := newTestChain(t)
	g := genesisOf(t, bc)
	m1 := blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, m1)
	side := extend(t, bc, g, 3, newKey(t).Address) // s1-s2 sağlam ve uçtan ağır, s3 bozulacak
	s2, s3 := side[1], side[2]
	bc.mu.Lock()
	for _, b := range side {
		storeOnly(t, bc, b) // zincire geçmeden saklanır (toplu varış)
	}
	bc.mu.Unlock()
	saved := rewriteBody(t, bc, s3.Hash, func(b *Block) { b.Transactions[0].Amount++ })
	before := chainDump(t, bc)

	err := bc.AcceptBlock(s3) // saklı ağır blok yeniden gelir: geçiş yeniden denenir
	if !errors.Is(err, ErrBlockDataMissing) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("hata %v, beklenen bozuk gövde", err)
	}
	if tipHash(t, bc) != m1.Hash || !reflect.DeepEqual(chainDump(t, bc), before) {
		t.Fatal("bozuk gövdeli dalın sağlam önekine geçildi ya da durum değişti")
	}
	noInvalid(t, bc)
	if rec := bc.dataAlarm; rec == nil || rec.Hash != s3.Hash || rec.Kind != dataCorruptBody ||
		rec.Target != s3.Hash || rec.Tip != m1.Hash {
		t.Fatalf("alarm kaydı %+v", rec)
	}

	// Sağlam önekin ucu yeniden gelince öneke geçilir.
	if err := bc.AcceptBlock(s2); err != nil || tipHash(t, bc) != s2.Hash {
		t.Fatalf("sağlam öneke geçilmedi: %v", err)
	}
	// Gövde düzelince dalın kalanı bağlanır.
	if err := bc.db.Put([]byte(s3.Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	if err := bc.AcceptBlock(s3); err != nil || tipHash(t, bc) != s3.Hash {
		t.Fatalf("gövde düzeldikten sonra dala geçilmedi: %v", err)
	}
	assertSameState(t, bc, replay(t, bc, side...))
}

// Açılış kurtarması da gövdeyi başlığına bağlar: bozuk gövdeli en ağır adaya geçilmez, alarm
// verilir, hiçbir blok geçersiz işaretlenmez. Kurtarma sıradaki adayla sürer: bozuk bloktan
// önceki sağlam blok uçtan ağırsa düğüm ona geçer (hafif zincirde takılı kalmaz). Gövde düzelince
// blok yeniden gelince dalın kalanına geçilir.
func TestRecoverCorruptBodyAlarm(t *testing.T) {
	dir := t.TempDir()
	bc := openTestChain(t, dir)
	// Açılıştaki kurtarma gerçek saatle çalışır (FTL): bloklar gerçek saatin gerisinde olsun.
	g := genesisOf(t, bc)
	clock := max(time.Now().Unix()-1000, g.Timestamp)
	bc.now = func() int64 { clock++; return clock }
	m1 := blockOn(t, bc, g, newKey(t).Address)
	mustAccept(t, bc, m1)
	side := extend(t, bc, g, 3, newKey(t).Address) // s1-s2 sağlam ve uçtan ağır, s3 bozulacak
	s2, s3 := side[1], side[2]
	bc.mu.Lock()
	for _, b := range side {
		storeOnly(t, bc, b) // zincire geçmeden "çökme"
	}
	bc.mu.Unlock()
	saved := rewriteBody(t, bc, s3.Hash, func(b *Block) { b.Transactions[0].Amount++ })
	now := bc.now
	bc.Close()

	bc2, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc2.Close() })
	bc2.now = now
	bc2.retryDefer = 0 // açılış denemesinden hemen sonra yeniden gönderim (retry_test.go)
	if tipHash(t, bc2) != s2.Hash {
		t.Fatal("açılışta bozuk gövdeli bloğa geçildi ya da sağlam ağır öneke geçilmedi")
	}
	noInvalid(t, bc2)
	// Alarm en ağır adayın denemesinde verildi: o anki uç m1, hedef s3.
	if rec := bc2.dataAlarm; rec == nil || rec.Hash != s3.Hash || rec.Kind != dataCorruptBody ||
		rec.Target != s3.Hash || rec.Tip != m1.Hash {
		t.Fatalf("alarm kaydı %+v", rec)
	}

	if err := bc2.db.Put([]byte(s3.Hash), saved, nil); err != nil {
		t.Fatal(err)
	}
	if err := bc2.AcceptBlock(s3); err != nil {
		t.Fatalf("gövde düzeldikten sonra: %v", err)
	}
	if tipHash(t, bc2) != s3.Hash {
		t.Fatal("gövde düzeldikten sonra dala geçilmedi")
	}
	assertSameState(t, bc2, replay(t, bc2, side...))
}

// Gövdesi başlığına bağlanmış blokta yükseklik, önceki özet ya da medyan zaman hatası ancak yerel
// indeks bozulmasından doğabilir (aynı denetimler saklamadan önce precheckLocked'ta yapılır):
// geçiş olmaz, blok geçersiz işaretlenmez, alarm verilmez; indeks düzelince geçiş olur. Eskiden
// dürüst blok torunlarıyla birlikte kalıcı geçersiz işaretlenirdi.
func TestIndexMismatchNotInvalid(t *testing.T) {
	// Ana zincir g-m1; g-s1-s2 saklı ama etkin değil. corrupt bellekteki indeksi bozar; geri
	// alma fonksiyonunu ve hatanın bağlandığı bloğu döndürür.
	for _, c := range []struct {
		name    string
		corrupt func(bc *Blockchain, g, m1, s1, s2 *Block) (undo func(), at *Block)
	}{
		{"medyan zaman", func(bc *Blockchain, g, m1, s1, s2 *Block) (func(), *Block) {
			e := bc.index[g.Hash]
			old := e.Timestamp
			e.Timestamp = s1.Timestamp + 1000 // s1 artık atasının medyanından eski görünür
			return func() { e.Timestamp = old }, s1
		}},
		{"önceki özet", func(bc *Blockchain, g, m1, s1, s2 *Block) (func(), *Block) {
			e := bc.index[s2.Hash]
			e.Prev = m1.Hash // doğru yükseklikte yanlış ebeveyn
			return func() { e.Prev = s1.Hash }, s2
		}},
		{"yükseklik", func(bc *Blockchain, g, m1, s1, s2 *Block) (func(), *Block) {
			e := bc.index[s2.Hash]
			e.Prev = g.Hash // ebeveyn bir yükseklik aşağıda
			return func() { e.Prev = s1.Hash }, s2
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			bc := newTestChain(t)
			g := genesisOf(t, bc)
			m1 := blockOn(t, bc, g, newKey(t).Address)
			mustAccept(t, bc, m1)
			s1 := blockOn(t, bc, g, newKey(t).Address)
			s2 := blockOn(t, bc, s1, newKey(t).Address)
			bc.mu.Lock()
			storeOnly(t, bc, s1)
			storeOnly(t, bc, s2)
			undo, at := c.corrupt(bc, g, m1, s1, s2)
			err := bc.activateLocked(bc.index[s2.Hash])
			bc.mu.Unlock()
			if !errors.Is(err, ErrCorruptState) || errors.Is(err, ErrBlockDataMissing) ||
				errors.Is(err, ErrTimestampOld) || errors.Is(err, ErrBadPrevHash) || errors.Is(err, ErrBadIndex) {
				t.Fatalf("hata %v, beklenen yalnızca bozuk durum", err)
			}
			if tipHash(t, bc) != m1.Hash || bc.MissingDataAlarm() || bc.indexWarn != at.Hash {
				t.Fatalf("uç değişti, alarm verildi ya da hata yanlış bloğa bağlandı (%s)", bc.indexWarn)
			}
			noInvalid(t, bc)

			bc.mu.Lock()
			undo()
			err = bc.activateLocked(bc.index[s2.Hash])
			bc.mu.Unlock()
			if err != nil || tipHash(t, bc) != s2.Hash {
				t.Fatalf("indeks düzeldikten sonra dala geçilmedi: %v", err)
			}
			assertSameState(t, bc, replay(t, bc, s1, s2))
		})
	}
}

// connectBody eksik gövdeyi eskisi gibi sınıflar (body türü, ErrCorruptState değil).
func TestConnectBodyMissing(t *testing.T) {
	bc := newTestChain(t)
	_, err := bc.connectBody(hexRepeat("ab", 32))
	var md *missingDataError
	if !errors.As(err, &md) || md.kind != dataBody || errors.Is(err, ErrCorruptState) {
		t.Fatalf("eksik gövde: %v", err)
	}
}

// Diske yazılan gövde JSON'dur ve bağlanırken geri okunup başlığına bağlanır (connectBody). Bu
// yüzden "bellekte doğrulanan blok JSON gidiş-dönüşünde özet girdilerini korur" değişmezi artık
// canlılık koşuludur: bozulursa bütün düğümler aynı blokta alarm verir. Test değişmezi kenar
// biçimli bloklarda ve tuhaf ağ girdisinde sabitler; alan listesi koruması Block ya da
// Transaction'a alan eklendiğinde değişmezin yeniden denetlenmesini zorlar.
func TestBodyRoundTripInvariant(t *testing.T) {
	fields := func(v interface{}, want ...string) {
		typ := reflect.TypeOf(v)
		var got []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			got = append(got, f.Name)
			if tag := f.Tag.Get("json"); tag == "" || strings.HasPrefix(tag, "-") {
				t.Errorf("%v.%s JSON'a yazılmıyor (etiket %q)", typ, f.Name, tag)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v alanları değişti: %v; yeni alanın özetlendiği (işlem kimliği ya da başlık) ve JSON gidiş-dönüşünün birebir olduğu denetlenip bu liste güncellenmeli", typ, got)
		}
	}
	fields(Block{}, "Version", "Index", "Timestamp", "Transactions", "PrevHash", "MerkleRoot", "Bits", "Nonce", "Hash")
	fields(Transaction{}, "Sender", "Receiver", "Amount", "Fee", "ExpiryHeight", "Nonce", "PublicKey", "Signature", "IsCoinbase", "Commitment")
	for _, v := range []interface{}{&Block{}, &Transaction{}} {
		_, m := v.(json.Marshaler)
		_, u := v.(json.Unmarshaler)
		_, tm := v.(encoding.TextMarshaler)
		_, tu := v.(encoding.TextUnmarshaler)
		if m || u || tm || tu {
			t.Errorf("%T özel JSON/metin kodlaması taşıyor; gidiş-dönüş yeniden denetlenmeli", v)
		}
	}

	kp := newKey(t)
	prev := hexRepeat("ab", 32)
	seal := func(b *Block) *Block {
		b.MerkleRoot = b.ComputeMerkleRoot()
		if b.Hash = b.CalculateHash(); b.Hash == "" {
			t.Fatal("başlık özetlenemedi")
		}
		return b
	}
	coinbase := func(mut func(*Transaction)) Transaction {
		tx := Transaction{Sender: CoinbaseSender, Receiver: kp.Address, Amount: testReward, Nonce: 1,
			IsCoinbase: true, Commitment: make([]byte, CoinbaseCommitmentSize)}
		if mut != nil {
			mut(&tx)
		}
		return tx
	}
	blk := func(txs ...Transaction) *Block {
		return seal(&Block{Version: BlockVersion, Index: 1, Timestamp: 5, PrevHash: prev, Transactions: txs})
	}
	// Boş ama nil olmayan tanık ve nil taahhüt: JSON'da "" ile null, omitempty ile hiç yok.
	bare := coinbase(func(tx *Transaction) { tx.PublicKey, tx.Signature, tx.Commitment = []byte{}, []byte{}, nil })
	// Sınır değerleri ve JSON'da kaçış gerektiren metin (uzlaşmada geçersiz, kodlamada geçerli).
	edge := Transaction{Sender: "<&> \"\\ ğüş", Receiver: "\x00\t\n", Amount: math.MaxInt64, Fee: math.MinInt64,
		ExpiryHeight: math.MaxInt64, Nonce: math.MaxUint64, PublicKey: []byte{0}, Signature: bytes.Repeat([]byte{0xff}, 100)}
	limits := seal(&Block{Version: math.MaxUint32, Index: math.MaxInt64, Timestamp: math.MinInt64, PrevHash: prev,
		Bits: math.MaxUint32, Nonce: math.MaxUint64, Transactions: []Transaction{coinbase(nil), edge}})
	for _, c := range []struct {
		name string
		b    *Block
	}{
		{"yalnızca coinbase", blk(coinbase(nil))},
		{"boş tanık, nil taahhüt", blk(bare)},
		{"imzalı transfer", blk(coinbase(nil), signedTx(t, kp, newKey(t).Address, Coin, 1))},
		{"sınır değerleri", limits},
	} {
		data := mustJSON(t, c.b)
		var got Block
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, err := verifyLoadedBlock(c.b.Hash, &got); err != nil {
			t.Errorf("%s: gidiş-dönüşten sonra gövde başlığıyla tutmuyor: %v", c.name, err)
		}
		if !reflect.DeepEqual(TxIDs(got.Transactions), TxIDs(c.b.Transactions)) {
			t.Errorf("%s: işlem kimlikleri gidiş-dönüşte değişti", c.name)
		}
		if !bytes.Equal(mustJSON(t, &got), data) {
			t.Errorf("%s: ikinci serileştirme ilkinden farklı", c.name)
		}
	}

	// Ağ girdisi: çift anahtar, bilinmeyen alan, büyük harfli anahtar, null ve boş dilim, geçersiz
	// UTF-8 ve yalnız vekil içeren JSON bir kez çözüldükten sonra gidiş-dönüşte değişmez.
	raw := `{"version":1,"index":7,"timestamp":5,"extra":true,"transactions":[{"sender":"a\ud800b",` +
		`"Receiver":"x","receiver":"café ` + "\xff" + `","amount":1,"amount":2,"fee":0,"expiry_height":9,` +
		`"nonce":3,"public_key":"AQI=","signature":null,"is_coinbase":false,"commitment":""}],` +
		`"prev_hash":"` + prev + `","merkle_root":"` + prev + `","bits":1,"nonce":2,"hash":"x"}`
	var first, second Block
	if err := json.Unmarshal([]byte(raw), &first); err != nil {
		t.Fatal(err)
	}
	data := mustJSON(t, &first)
	if err := json.Unmarshal(data, &second); err != nil {
		t.Fatal(err)
	}
	if len(first.Transactions) != 1 || first.Transactions[0].Amount != 2 || first.CalculateHash() == "" {
		t.Fatalf("ağ girdisi beklendiği gibi çözülmedi: %+v", first)
	}
	if second.CalculateHash() != first.CalculateHash() || second.ComputeMerkleRoot() != first.ComputeMerkleRoot() ||
		!reflect.DeepEqual(TxIDs(second.Transactions), TxIDs(first.Transactions)) || !bytes.Equal(mustJSON(t, &second), data) {
		t.Fatal("ağdan çözülen blok gidiş-dönüşte değişti")
	}
}
