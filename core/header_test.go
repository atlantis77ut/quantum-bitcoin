package core

import (
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
)

func TestDifficultyToBitsVectors(t *testing.T) {
	for d, want := range map[int]uint32{1: 0x200fffff, 4: 0x1f00ffff, 60: 0x0300ffff} {
		got, err := DifficultyToBits(d)
		if err != nil || got != want {
			t.Fatalf("DifficultyToBits(%d) = %#x, %v; beklenen %#x", d, got, err, want)
		}
	}
	for d := 1; d <= 60; d++ {
		bits, err := DifficultyToBits(d)
		if err != nil {
			t.Fatalf("d=%d: %v", d, err)
		}
		if _, err := TargetFromBits(bits); err != nil {
			t.Fatalf("d=%d: üretilen bits geçersiz: %v", d, err)
		}
	}
	for _, d := range []int{0, -1, 61, 64} {
		if _, err := DifficultyToBits(d); err == nil {
			t.Fatalf("d=%d kabul edildi", d)
		}
	}
}

func TestTargetFromBitsRejectsBadEncodings(t *testing.T) {
	cases := map[string]uint32{
		"sıfır mantis":     0x1f000000,
		"negatif":          0x1f800001,
		"taşma":            0xff123456,
		"kanonik değil":    0x04000080, // kanonik biçimi 0x03008000
		"powLimit üstünde": easierThanLimitBits(),
		"tamamen sıfır":    0,
	}
	for name, bits := range cases {
		if _, err := TargetFromBits(bits); !errors.Is(err, ErrBadBits) {
			t.Errorf("%s (%#x): hata %v", name, bits, err)
		}
	}
	if _, err := TargetFromBits(0x03008000); err != nil {
		t.Fatalf("kanonik küçük hedef reddedildi: %v", err)
	}
}

// Kural: özet (büyük-endian tamsayı) <= hedef.
func TestHashMeetsTargetBoundary(t *testing.T) {
	bits, _ := DifficultyToBits(4)
	target, err := TargetFromBits(bits)
	if err != nil {
		t.Fatal(err)
	}
	toHash := func(v *big.Int) [32]byte {
		var h [32]byte
		v.FillBytes(h[:])
		return h
	}
	if !hashMeetsTarget(toHash(target), target) {
		t.Fatal("hedefe eşit özet reddedildi")
	}
	if hashMeetsTarget(toHash(new(big.Int).Add(target, big.NewInt(1))), target) {
		t.Fatal("hedef+1 özet kabul edildi")
	}
	eq := toHash(target)
	if !MeetsTarget(hex.EncodeToString(eq[:]), bits) {
		t.Fatal("MeetsTarget hedefe eşit özeti reddetti")
	}
	if MeetsTarget(strings.ToUpper(hex.EncodeToString(eq[:])), bits) {
		t.Fatal("kanonik olmayan hex kabul edildi")
	}
}

func TestMerkleRootProperties(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	txs := make([]Transaction, 5)
	for i := range txs {
		txs[i] = signedTx(t, alice, bob.Address, int64(i+1), uint64(i+1))
	}
	seen := map[[32]byte]int{}
	for n := 1; n <= len(txs); n++ {
		r := MerkleRoot(txs[:n])
		if prev, ok := seen[r]; ok {
			t.Fatalf("%d ve %d işlem aynı kökü verdi", prev, n)
		}
		seen[r] = n
	}

	three := MerkleRoot(txs[:3])
	dup := MerkleRoot([]Transaction{txs[0], txs[1], txs[2], txs[2]})
	if three == dup {
		t.Fatal("son işlemi çoğaltmak aynı kökü verdi (CVE-2012-2459 sınıfı)")
	}
	swapped := MerkleRoot([]Transaction{txs[1], txs[0], txs[2]})
	if three == swapped {
		t.Fatal("işlem sırası köke yansımıyor")
	}
	id := txs[0].IDBytes()
	if MerkleRoot(txs[:1]) == id {
		t.Fatal("tek yapraklı kök ham işlem kimliğine eşit: yaprak öneki uygulanmıyor")
	}
}

// Her başlık alanı özete, her işlem alanı Merkle köküne yansımalı. Yapıya yeni bir alan
// eklenirse alan sayısı testi kırılır; yeni alan özete/köke bağlanmadan geçemez.
func TestEveryFieldIsCommitted(t *testing.T) {
	if n := reflect.TypeOf(Block{}).NumField(); n != 9 {
		t.Fatalf("Block alan sayısı %d: yeni alan başlığa bağlandı mı? Testi güncelle", n)
	}
	if n := reflect.TypeOf(Transaction{}).NumField(); n != 9 {
		t.Fatalf("Transaction alan sayısı %d: yeni alan imzaya/kimliğe bağlandı mı? Testi güncelle", n)
	}

	alice, bob := newKey(t), newKey(t)
	base := &Block{Version: BlockVersion, Index: 7, Timestamp: 1000, Bits: 0x1f00ffff, Nonce: 3,
		PrevHash: strings.Repeat("11", 32),
		Transactions: []Transaction{
			{Sender: CoinbaseSender, Receiver: bob.Address, Amount: BlockReward, Nonce: 7, IsCoinbase: true},
			signedTxFee(t, alice, bob.Address, Coin, 5, 1),
		}}
	base.MerkleRoot = base.ComputeMerkleRoot()
	h0 := base.CalculateHash()

	headerMut := map[string]func(b *Block){
		"Version":    func(b *Block) { b.Version++ },
		"Index":      func(b *Block) { b.Index++ },
		"Timestamp":  func(b *Block) { b.Timestamp++ },
		"PrevHash":   func(b *Block) { b.PrevHash = strings.Repeat("12", 32) },
		"MerkleRoot": func(b *Block) { b.MerkleRoot = strings.Repeat("13", 32) },
		"Bits":       func(b *Block) { b.Bits-- },
		"Nonce":      func(b *Block) { b.Nonce++ },
	}
	for name, mut := range headerMut {
		b := *base
		mut(&b)
		if b.CalculateHash() == h0 {
			t.Errorf("başlık alanı %s özete bağlı değil", name)
		}
	}

	r0 := base.ComputeMerkleRoot()
	txMut := map[string]func(tx *Transaction){
		"Sender":     func(tx *Transaction) { tx.Sender = bob.Address },
		"Receiver":   func(tx *Transaction) { tx.Receiver = alice.Address },
		"Amount":     func(tx *Transaction) { tx.Amount++ },
		"Fee":        func(tx *Transaction) { tx.Fee++ },
		"Timestamp":  func(tx *Transaction) { tx.Timestamp++ },
		"Nonce":      func(tx *Transaction) { tx.Nonce++ },
		"PublicKey":  func(tx *Transaction) { tx.PublicKey = flipFirst(tx.PublicKey) },
		"Signature":  func(tx *Transaction) { tx.Signature = flipFirst(tx.Signature) },
		"IsCoinbase": func(tx *Transaction) { tx.IsCoinbase = !tx.IsCoinbase },
	}
	for name, mut := range txMut {
		b := *base
		b.Transactions = append([]Transaction(nil), base.Transactions...)
		mut(&b.Transactions[1])
		if b.ComputeMerkleRoot() == r0 {
			t.Errorf("işlem alanı %s Merkle köküne bağlı değil", name)
		}
	}
}

// flipFirst ilk baytı tersine çevrilmiş bir kopya döndürür (orijinal dilim değişmez).
func flipFirst(b []byte) []byte {
	c := append([]byte(nil), b...)
	c[0] ^= 0xff
	return c
}

// Madencinin değiştirdiği alanlar (nonce, zaman) ilk 64 baytlık SHA-256 bloğunda olmalı.
func TestHeaderLayout(t *testing.T) {
	b := &Block{Version: 1, Index: 2, Timestamp: 3, Bits: 4, Nonce: 5,
		PrevHash: strings.Repeat("aa", 32), MerkleRoot: strings.Repeat("bb", 32)}
	hdr, ok := b.headerBytes()
	if !ok || len(hdr) != HeaderSize || HeaderSize == 80 {
		t.Fatalf("başlık boyutu %d", len(hdr))
	}
	b2 := *b
	b2.Nonce++
	b2.Timestamp++
	hdr2, _ := b2.headerBytes()
	for i := 64; i < HeaderSize; i++ {
		if hdr[i] != hdr2[i] {
			t.Fatalf("nonce/zaman değişimi ikinci SHA-256 bloğunu (bayt %d) etkiledi", i)
		}
	}
	if hex.EncodeToString(hdr[64:]) != b.MerkleRoot {
		t.Fatal("ikinci blok yalnızca Merkle kökünü içermeli")
	}
}

// Aynı baytlara karşılık gelen kanonik olmayan hex metin (büyük harf) özetlenemez;
// böylece bir bloğun farklı metinli "takma adı" oluşturulamaz.
func TestNonCanonicalHexRejected(t *testing.T) {
	h := strings.Repeat("ab", 32)
	b := &Block{Version: 1, Index: 1, PrevHash: h, MerkleRoot: h}
	if b.CalculateHash() == "" {
		t.Fatal("kanonik başlık özetlenemedi")
	}
	for name, mut := range map[string]func(*Block){
		"büyük harf önceki":    func(b *Block) { b.PrevHash = strings.ToUpper(h) },
		"büyük harf merkle":    func(b *Block) { b.MerkleRoot = strings.ToUpper(h) },
		"kısa önceki":          func(b *Block) { b.PrevHash = h[:62] },
		"hex olmayan merkle":   func(b *Block) { b.MerkleRoot = "zz" + h[2:] },
		"boşluklu önceki hash": func(b *Block) { b.PrevHash = " " + h[1:] },
	} {
		c := *b
		mut(&c)
		if got := c.CalculateHash(); got != "" {
			t.Errorf("%s: kanonik olmayan hex özetlendi (%s)", name, got)
		}
	}
}

// Merkle köküne bağlı gövde hatası (ör. bozuk imza) kalıcıdır ve önbelleğe alınır.
func TestBodyErrorIsCached(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	tx.Signature = flipFirst(tx.Signature)
	b := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx}) // kök bozuk imzayı kapsıyor
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("ilk ret: %v", err)
	}
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrKnownInvalid) {
		t.Fatalf("gövde hatası önbelleğe alınmadı: %v", err)
	}
}

// Ucun üzerine kurulmayan blokta imzalar doğrulanmaz: bozuk imzalı ama yanlış ebeveynli
// blok imza hatası değil ErrBadPrevHash ile, önbelleğe girmeden reddedilir.
func TestWrongParentRejectedBeforeSignatures(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	tx := signedTx(t, alice, bob.Address, Coin, 1)
	tx.Signature = flipFirst(tx.Signature)
	b := craftBlock(t, bc, []Transaction{coinbaseTo(bob.Address), tx})
	b.PrevHash = strings.Repeat("ab", 32)
	mineAt(t, bc, b)
	if err := bc.AcceptBlock(b); !errors.Is(err, ErrBadPrevHash) {
		t.Fatalf("hata %v, beklenen ErrBadPrevHash (imza doğrulamasından önce)", err)
	}
	if bc.rejected.has(b.Hash) {
		t.Fatal("yanlış ebeveynli blok önbelleğe alındı")
	}
}

func TestSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	bc, err := NewBlockchain(dir, testParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := bc.db.Get([]byte(keySchema), nil); err != nil || string(v) != schemaVersion {
		t.Fatalf("yeni veritabanına şema yazılmadı: %q %v", v, err)
	}
	if err := bc.db.Delete([]byte(keySchema), nil); err != nil { // eski (şemasız) dizini taklit et
		t.Fatal(err)
	}
	bc.Close()
	if _, err := NewBlockchain(dir, testParams); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("şemasız dizin açıldı: %v", err)
	}

	dir2 := t.TempDir()
	bc2, err := NewBlockchain(dir2, testParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc2.db.Put([]byte(keySchema), []byte("1"), nil); err != nil {
		t.Fatal(err)
	}
	bc2.Close()
	if _, err := NewBlockchain(dir2, testParams); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("eski şemalı dizin açıldı: %v", err)
	}
}
