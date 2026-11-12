package core

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
)

// BlockVersion desteklenen tek başlık sürümüdür.
const BlockVersion uint32 = 1

// HeaderSize sabit boyutlu başlığın bayt uzunluğudur.
//
// Düzen (büyük-endian):
//
//	[0:8]   nonce        ┐
//	[8:12]  version      │ ilk 64 baytlık SHA-256 bloğu: madencinin değiştirdiği
//	[12:20] height       │ her alan (nonce, zaman) burada
//	[20:28] timestamp    │
//	[28:32] bits         │
//	[32:64] prev_hash    ┘
//	[64:96] merkle_root  ─ ikinci blok: yalnızca merkle kökü + dolgu
//
// Bitcoin ASIC'leri ilk 64 baytın ara durumunu (midstate) bir kez hesaplayıp yalnızca
// ikinci bloktaki 4 baytlık nonce'u döndürür ve 80 baytlık mesaj dolgusu varsayar.
// Burada nonce ilk bloktadır ve toplam uzunluk 96 bayttır; bu bir donanım testi değil,
// tasarım gerekçesidir.
const HeaderSize = 96

var (
	ErrBadVersion    = errors.New("desteklenmeyen blok sürümü")
	ErrBadMerkleRoot = errors.New("merkle kökü işlemlerle eşleşmiyor")
)

// decodeHash yalnızca kanonik (64 küçük harf hex) özetleri kabul eder; böylece aynı
// baytlara karşılık gelen farklı metin (ör. büyük harf) aynı bloğun takma adı olamaz.
func decodeHash(s string) ([32]byte, bool) {
	var h [32]byte
	if len(s) != 64 {
		return h, false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return h, false
		}
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return h, false
	}
	return h, true
}

// headerBytes başlığı sabit düzende kodlar; özet alanları kanonik değilse hata döner.
func (b *Block) headerBytes() ([HeaderSize]byte, bool) {
	var hdr [HeaderSize]byte
	prev, ok := decodeHash(b.PrevHash)
	if !ok {
		return hdr, false
	}
	root, ok := decodeHash(b.MerkleRoot)
	if !ok {
		return hdr, false
	}
	binary.BigEndian.PutUint64(hdr[0:8], b.Nonce)
	binary.BigEndian.PutUint32(hdr[8:12], b.Version)
	binary.BigEndian.PutUint64(hdr[12:20], uint64(b.Index))
	binary.BigEndian.PutUint64(hdr[20:28], uint64(b.Timestamp))
	binary.BigEndian.PutUint32(hdr[28:32], b.Bits)
	copy(hdr[32:64], prev[:])
	copy(hdr[64:96], root[:])
	return hdr, true
}

func hashHeader(hdr []byte) [32]byte {
	first := sha256.Sum256(hdr)
	return sha256.Sum256(first[:])
}

// CalculateHash başlığın çift SHA-256 özetini hex olarak döndürür. İşlemler özete
// yalnızca MerkleRoot üzerinden bağlanır; kökün işlemlerle eşleştiği ayrıca doğrulanmalıdır.
func (b *Block) CalculateHash() string {
	hdr, ok := b.headerBytes()
	if !ok {
		return "" // boş özet hiçbir doğrulamadan geçmez
	}
	h := hashHeader(hdr[:])
	return hex.EncodeToString(h[:])
}

// MineBlock sürümü, hedefi ve Merkle kökünü ayarlar; ardından başlık hedefi karşılayana
// kadar nonce'u artırır. Hedef çözülemezse ErrBadBits döner. Testlerin kural dışı
// (powLimit'ten kolay) hedeflerle blok üretebilmesi için yalnızca compact çözümü şarttır.
func (b *Block) MineBlock(bits uint32) error {
	target, ok := compactToBig(bits)
	if !ok {
		return ErrBadBits
	}
	b.Version = BlockVersion
	b.Bits = bits
	b.MerkleRoot = b.ComputeMerkleRoot()
	hdr, ok := b.headerBytes()
	if !ok {
		return ErrBadHash
	}
	v := new(big.Int)
	for nonce := b.Nonce; ; nonce++ {
		binary.BigEndian.PutUint64(hdr[0:8], nonce)
		h := hashHeader(hdr[:])
		if v.SetBytes(h[:]).Cmp(target) <= 0 {
			b.Nonce = nonce
			b.Hash = hex.EncodeToString(h[:])
			return nil
		}
	}
}
