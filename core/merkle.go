package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Merkle ağacı RFC 6962 tarzıdır: yaprak ve iç düğüm özetleri farklı öneklerle ayrılır,
// tek sayıdaki son eleman kopyalanmaz. Böylece Bitcoin'deki "son işlemi çoğaltınca aynı kök"
// (CVE-2012-2459) belirsizliği oluşmaz ve bir iç düğüm yaprak gibi sunulamaz.
//
// Biçim (üçüncü taraf doğrulayıcılar için):
//
//	yaprak = SHA256(0x00 ‖ txid)          txid: işlem kimliğinin 32 baytı (hex çözülmüş, ters çevrilmez)
//	düğüm  = SHA256(0x01 ‖ sol ‖ sağ)
//	n > 1 yaprakta sol alt ağaç ilk k yapraktır; k, n'den küçük en büyük ikinin kuvvetidir.
//
// Kanıt (MerkleProof) kardeş özetlerin yapraktan köke dizisidir; her kardeşin solda mı sağda mı
// olduğu (sıra, işlem sayısı) çiftinden çıkar. Uzunluk sıraya göre değişebilir (n = 5'te son
// yaprağın kanıtı 1, diğerlerininki 3 elemanlıdır).
const (
	merkleLeafPrefix = 0x00
	merkleNodePrefix = 0x01

	// MaxMerkleProofLen kabul edilen en uzun kanıttır (2^64 yaprağa kadar yeter).
	MaxMerkleProofLen = 64
)

// ErrMerkleIndex istenen sıra bloğun işlemleri dışında olduğunda döner.
var ErrMerkleIndex = errors.New("merkle kanıtı: işlem sırası blok dışında")

// MerkleRoot işlem kimliklerinin (imza dahil) Merkle kökünü hesaplar.
func MerkleRoot(txs []Transaction) [32]byte {
	return merkleNodes(txLeaves(txs))
}

// MerkleProof txs[index] işleminin köke dahil olduğunu gösteren kardeş özetleri yapraktan köke
// sırayla döndürür. Tek yapraklı ağaçta kanıt boştur.
func MerkleProof(txs []Transaction, index int) ([][32]byte, error) {
	if index < 0 || index >= len(txs) {
		return nil, ErrMerkleIndex
	}
	return merklePath(txLeaves(txs), index, nil), nil
}

// MerkleRootFromProof, kimliği id olan işlemin count işlemli bir blokta index sırasında olduğu
// iddiasıyla kanıttan kökü hesaplar. Girdi yaprak özeti değil işlem kimliğidir ve yaprak burada
// 0x00 önekiyle hesaplanır: böylece bir iç düğüm işlem gibi sunulamaz. Yaprak özetinden doğrulayan
// bir biçim bu yüzden eklenmemelidir. Kanıt eksikse ya da fazla elemanlıysa false döner.
//
// Kök başlık tarafından bağlanır, sıra ve işlem sayısı bağlanmaz: kanıt yalnızca dahil olmayı
// kanıtlar, sıra kanıtı sunanın iddiasıdır (farklı bir (sıra, sayı) aynı köke çıkabilir).
func MerkleRootFromProof(id [32]byte, index, count int, proof [][32]byte) ([32]byte, bool) {
	if count < 1 || index < 0 || index >= count || len(proof) > MaxMerkleProofLen {
		return [32]byte{}, false
	}
	root, rest, ok := merkleFold(merkleLeaf(id), index, count, proof)
	if !ok || len(rest) != 0 {
		return [32]byte{}, false
	}
	return root, true
}

// VerifyMerkleProof kanıttan hesaplanan kökün root olduğunu denetler. root, istemcinin kendisinin
// doğruladığı (iş ve zincir bağı) bir başlıktan alınmalıdır; kanıtı sunan düğümün bildirdiği kök
// kanıtı anlamsız kılar.
func VerifyMerkleProof(id [32]byte, index, count int, proof [][32]byte, root [32]byte) bool {
	r, ok := MerkleRootFromProof(id, index, count, proof)
	return ok && r == root
}

func txLeaves(txs []Transaction) [][32]byte {
	leaves := make([][32]byte, len(txs))
	for i := range txs {
		leaves[i] = merkleLeaf(txs[i].IDBytes())
	}
	return leaves
}

func merkleLeaf(id [32]byte) [32]byte {
	var buf [33]byte
	buf[0] = merkleLeafPrefix
	copy(buf[1:], id[:])
	return sha256.Sum256(buf[:])
}

func merkleNode(l, r [32]byte) [32]byte {
	var buf [65]byte
	buf[0] = merkleNodePrefix
	copy(buf[1:33], l[:])
	copy(buf[33:], r[:])
	return sha256.Sum256(buf[:])
}

// merkleSplit n > 1 yapraklı ağacın sol alt ağacındaki yaprak sayısıdır: n'den küçük en büyük
// ikinin kuvveti. Kök, kanıt üretimi ve doğrulama aynı bölmeyi kullanır. Koşul k < n − k
// biçiminde yazılır: doğrulayıcıya gelen sayı güvenilmezdir ve k*2 taşabilirdi.
func merkleSplit(n int) int {
	k := 1
	for k < n-k {
		k *= 2
	}
	return k
}

func merkleNodes(n [][32]byte) [32]byte {
	switch len(n) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return n[0]
	}
	k := merkleSplit(len(n))
	return merkleNode(merkleNodes(n[:k]), merkleNodes(n[k:]))
}

// merklePath n[m] yaprağının kardeş özetlerini yapraktan köke path'e ekler (RFC 6962 PATH).
func merklePath(n [][32]byte, m int, path [][32]byte) [][32]byte {
	if len(n) <= 1 {
		return path
	}
	k := merkleSplit(len(n))
	if m < k {
		return append(merklePath(n[:k], m, path), merkleNodes(n[k:]))
	}
	return append(merklePath(n[k:], m-k, path), merkleNodes(n[:k]))
}

// merkleFold merklePath'in tersidir: kanıt yapraktan köke sıralı olduğundan bu düzeyin kardeşi
// son elemandır; kalanlar alt ağaca geçer. Her düzey bir eleman tükettiğinden özyineleme
// derinliği kanıt uzunluğuyla sınırlıdır.
func merkleFold(leaf [32]byte, m, n int, proof [][32]byte) ([32]byte, [][32]byte, bool) {
	if n == 1 {
		return leaf, proof, true
	}
	if len(proof) == 0 {
		return [32]byte{}, nil, false
	}
	sib, rest := proof[len(proof)-1], proof[:len(proof)-1]
	k := merkleSplit(n)
	if m < k {
		l, rest, ok := merkleFold(leaf, m, k, rest)
		return merkleNode(l, sib), rest, ok
	}
	r, rest, ok := merkleFold(leaf, m-k, n-k, rest)
	return merkleNode(sib, r), rest, ok
}

// ComputeMerkleRoot bloğun işlemlerinden Merkle kökünü hex olarak hesaplar.
func (b *Block) ComputeMerkleRoot() string {
	r := MerkleRoot(b.Transactions)
	return hex.EncodeToString(r[:])
}
