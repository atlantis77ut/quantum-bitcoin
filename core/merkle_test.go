package core

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"
)

func testIDs(n int) [][32]byte {
	ids := make([][32]byte, n)
	for i := range ids {
		ids[i] = sha256.Sum256([]byte{byte(i), byte(i >> 8)})
	}
	return ids
}

func leavesOf(ids [][32]byte) [][32]byte {
	l := make([][32]byte, len(ids))
	for i, id := range ids {
		l[i] = merkleLeaf(id)
	}
	return l
}

func mustHash(t *testing.T, s string) [32]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		t.Fatalf("hex %q", s)
	}
	var h [32]byte
	copy(h[:], b)
	return h
}

// Bağımsız test vektörü (Python hashlib ile RFC 6962 tanımından hesaplandı): kimlikler i baytıyla
// dolu 32 bayt, i = 0..4. Hem uzlaşmadaki kökü hem kanıt biçimini sabitler; iki taraf birlikte
// değişse de yakalanır.
func TestMerkleFixedVector(t *testing.T) {
	ids := make([][32]byte, 5)
	for i := range ids {
		for j := range ids[i] {
			ids[i][j] = byte(i)
		}
	}
	leaves := leavesOf(ids)
	root := mustHash(t, "85e20cac1f02fda7bcdb2fc3f908568c57018c77815f1fa361acad13994f08bf")
	if merkleNodes(leaves) != root {
		t.Fatal("kök vektörle eşleşmiyor")
	}
	for _, c := range []struct {
		index int
		proof []string
	}{
		{2, []string{
			"acaa04663a8547a2f70c60cc18f9378796b13c4f9a08f70d6adae662365b30c6",
			"28fb81e496897e0ce886f08602392e9239b65c659041e5202163e58ad898f444",
			"1da033bf8927ed69376d91533748494f7f5e88c20603dede2afc9bfd43d46f17",
		}},
		{4, []string{"fdea52008cdae79fa8bf806261959e23f5e11681646a2fa2bc9b5e56b32030a2"}},
	} {
		got := merklePath(leaves, c.index, nil)
		if len(got) != len(c.proof) {
			t.Fatalf("sıra %d: kanıt %d elemanlı, beklenen %d", c.index, len(got), len(c.proof))
		}
		want := make([][32]byte, len(c.proof))
		for i, s := range c.proof {
			want[i] = mustHash(t, s)
			if got[i] != want[i] {
				t.Fatalf("sıra %d, eleman %d vektörle eşleşmiyor", c.index, i)
			}
		}
		if !VerifyMerkleProof(ids[c.index], c.index, 5, want, root) {
			t.Fatalf("sıra %d: vektör kanıtı reddedildi", c.index)
		}
	}
}

// Her ağaç boyunda her yaprağın kanıtı köke çıkar; bozuk kanıtlar reddedilir.
func TestMerkleProofRoundTrip(t *testing.T) {
	for n := 1; n <= 33; n++ {
		ids := testIDs(n)
		leaves := leavesOf(ids)
		root := merkleNodes(leaves)
		for i := 0; i < n; i++ {
			proof := merklePath(leaves, i, nil)
			if !VerifyMerkleProof(ids[i], i, n, proof, root) {
				t.Fatalf("n=%d sıra %d: geçerli kanıt reddedildi", n, i)
			}
			for j := 0; j < n; j++ { // aynı sayıda başka sıra
				if j != i && VerifyMerkleProof(ids[i], j, n, proof, root) {
					t.Fatalf("n=%d: %d sırasının kanıtı %d için kabul edildi", n, i, j)
				}
			}
			if j := (i + 1) % n; j != i && VerifyMerkleProof(ids[j], i, n, proof, root) {
				t.Fatalf("n=%d sıra %d: başka işlemin kimliği kabul edildi", n, i)
			}
			if len(proof) > 0 {
				if VerifyMerkleProof(ids[i], i, n, proof[:len(proof)-1], root) {
					t.Fatalf("n=%d sıra %d: eksik kanıt kabul edildi", n, i)
				}
				bad := append([][32]byte{}, proof...)
				bad[0][0] ^= 1
				if VerifyMerkleProof(ids[i], i, n, bad, root) {
					t.Fatalf("n=%d sıra %d: değiştirilmiş kardeş kabul edildi", n, i)
				}
			}
			if VerifyMerkleProof(ids[i], i, n, append(append([][32]byte{}, proof...), root), root) {
				t.Fatalf("n=%d sıra %d: fazla elemanlı kanıt kabul edildi", n, i)
			}
		}
	}
}

func TestMerkleProofBounds(t *testing.T) {
	ids := testIDs(4)
	leaves := leavesOf(ids)
	root := merkleNodes(leaves)
	proof := merklePath(leaves, 1, nil)
	for _, c := range []struct{ index, count int }{{1, 0}, {-1, 4}, {4, 4}, {0, -1}} {
		if VerifyMerkleProof(ids[1], c.index, c.count, proof, root) {
			t.Fatalf("%+v kabul edildi", c)
		}
	}
	// Güvenilmez dev sayı sonsuz döngüye ya da taşmaya yol açmaz, kısa kanıtla reddedilir.
	if _, ok := MerkleRootFromProof(ids[0], math.MaxInt-1, math.MaxInt, proof); ok {
		t.Fatal("dev sayı kabul edildi")
	}
	if _, ok := MerkleRootFromProof(ids[0], 0, 2, make([][32]byte, MaxMerkleProofLen+1)); ok {
		t.Fatal("sınırdan uzun kanıt kabul edildi")
	}
	if p, err := MerkleProof(make([]Transaction, 3), 3); err == nil || p != nil {
		t.Fatal("blok dışı sıra için kanıt üretildi")
	}
	if p, err := MerkleProof(make([]Transaction, 1), 0); err != nil || len(p) != 0 {
		t.Fatalf("tek işlemli blok: %v, %v", p, err)
	}
}

// Bir iç düğüm işlem kimliği gibi sunulamaz: yaprak 0x00 önekiyle hesaplandığından iç düğümün
// özeti bir kimliğin yaprak özetine denk gelmez.
func TestMerkleInteriorNotLeaf(t *testing.T) {
	ids := testIDs(4)
	leaves := leavesOf(ids)
	root := merkleNodes(leaves)
	left, right := merkleNodes(leaves[:2]), merkleNodes(leaves[2:])
	if VerifyMerkleProof(left, 0, 2, [][32]byte{right}, root) {
		t.Fatal("iç düğüm işlem olarak kabul edildi")
	}
}

// Sıra ve sayı başlıkla bağlanmaz: kanıt dahil olmayı kanıtlar, sırayı değil (belgelenmiş sınır).
// n = 3'te son yaprağın kanıtı, n = 2'de 1. sıranın kanıtıyla aynı köke çıkar.
func TestMerklePositionNotCommitted(t *testing.T) {
	ids := testIDs(3)
	leaves := leavesOf(ids)
	root := merkleNodes(leaves)
	proof := merklePath(leaves, 2, nil)
	if !VerifyMerkleProof(ids[2], 2, 3, proof, root) || !VerifyMerkleProof(ids[2], 1, 2, proof, root) {
		t.Fatal("beklenen belirsizlik değişti; belgeyi güncelleyin")
	}
}

// MerkleProof bloğun işlemleriyle MerkleRoot'a çıkar.
func TestMerkleProofTransactions(t *testing.T) {
	txs := make([]Transaction, 7)
	for i := range txs {
		txs[i] = Transaction{Receiver: "x", Amount: int64(i + 1), Nonce: uint64(i), ExpiryHeight: 1}
	}
	root := MerkleRoot(txs)
	for i := range txs {
		proof, err := MerkleProof(txs, i)
		if err != nil || !VerifyMerkleProof(txs[i].IDBytes(), i, len(txs), proof, root) {
			t.Fatalf("sıra %d: %v", i, err)
		}
	}
}
