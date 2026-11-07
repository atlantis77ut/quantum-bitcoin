package core

import (
	"crypto/sha256"
	"encoding/hex"
)

// Merkle ağacı RFC 6962 tarzıdır: yaprak ve iç düğüm özetleri farklı öneklerle ayrılır,
// tek sayıdaki son eleman kopyalanmaz. Böylece Bitcoin'deki "son işlemi çoğaltınca aynı kök"
// (CVE-2012-2459) belirsizliği oluşmaz ve bir iç düğüm yaprak gibi sunulamaz.
const (
	merkleLeafPrefix = 0x00
	merkleNodePrefix = 0x01
)

// MerkleRoot işlem kimliklerinin (imza dahil) Merkle kökünü hesaplar.
func MerkleRoot(txs []Transaction) [32]byte {
	leaves := make([][32]byte, len(txs))
	for i := range txs {
		id := txs[i].IDBytes()
		var buf [33]byte
		buf[0] = merkleLeafPrefix
		copy(buf[1:], id[:])
		leaves[i] = sha256.Sum256(buf[:])
	}
	return merkleNodes(leaves)
}

func merkleNodes(n [][32]byte) [32]byte {
	switch len(n) {
	case 0:
		return sha256.Sum256(nil)
	case 1:
		return n[0]
	}
	k := 1 // n'den küçük en büyük ikinin kuvveti
	for k*2 < len(n) {
		k *= 2
	}
	l, r := merkleNodes(n[:k]), merkleNodes(n[k:])
	var buf [65]byte
	buf[0] = merkleNodePrefix
	copy(buf[1:33], l[:])
	copy(buf[33:], r[:])
	return sha256.Sum256(buf[:])
}

// ComputeMerkleRoot bloğun işlemlerinden Merkle kökünü hex olarak hesaplar.
func (b *Block) ComputeMerkleRoot() string {
	r := MerkleRoot(b.Transactions)
	return hex.EncodeToString(r[:])
}
