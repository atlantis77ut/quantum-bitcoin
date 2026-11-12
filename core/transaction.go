package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"quantum-bitcoin/crypto"
)

// txIDTag işlem kimliği ve Merkle kökü içindir ve ağdan bağımsızdır. Genesis bir işlem
// içerdiğinden, kimlik genesis özetine bağlı olsaydı genesis özeti kendine bağlı olurdu.
// İmza ise ayrı bir alanda atılır (Params.SigDomain, "QBTC-SIG-v4|<ağ>|<genesis özeti>"):
// testnet'te atılan imza mainnet'te ya da regtest'te geçersizdir. v4'te gövdedeki zaman
// damgası yerini son kullanma yüksekliğine bıraktı; alan sayısı aynı kaldığından v3 ve v4
// gövdeleri bayt düzeyinde çakışabilirdi, iki etiket de bu yüzden artırıldı.
const txIDTag = "QBTC-TXID-v2"

var (
	ErrNoGenesis          = errors.New("bu ağın genesis bloğu henüz tanımlı değil")
	ErrNonPositiveAmount  = errors.New("tutar pozitif olmalı")
	ErrNegativeFee        = errors.New("işlem ücreti negatif olamaz")
	ErrBadSender          = errors.New("gönderen adresi açık anahtarla eşleşmiyor")
	ErrBadReceiver        = errors.New("geçersiz alıcı adresi")
	ErrSelfTransfer       = errors.New("kendine transfer desteklenmiyor")
	ErrBadSignature       = errors.New("geçersiz imza")
	ErrUnexpectedCoinbase = errors.New("coinbase işlemi yalnızca bloğun ilk işlemi olabilir")
	ErrBadExpiry          = errors.New("son kullanma yüksekliği en az 1 olmalı")
	ErrTxExpired          = errors.New("işlemin son kullanma yüksekliği geçti")
)

// Transaction bir hesaptan diğerine imzalı transferdir.
//
// ExpiryHeight işlemin dahil edilebileceği son blok yüksekliğidir (dahil): h <= ExpiryHeight.
// Zorunludur (>= 1); "süresiz" değeri yoktur, alanı eksik istemci reddedilir. Uzlaşmada azami
// ufuk yoktur: ufuk ömrü sınırlamaz, yalnızca örtük bir "şu yükseklikten itibaren geçerli"
// kilidi yaratırdı; azami ömür ve varsayılan süre mempool/cüzdan politikasıdır. Coinbase'de 0'dır.
// Reorg bir işlemi ExpiryHeight'ın ötesine iterse işlem bir daha zincire giremez; alıcı için
// etkin kesinlik onay sayısı ile ExpiryHeight'a kalan bloğun küçüğüdür.
type Transaction struct {
	Sender       string `json:"sender"`
	Receiver     string `json:"receiver"`
	Amount       int64  `json:"amount"`        // Taban birim (1 QBTC = Coin)
	Fee          int64  `json:"fee"`           // Madenciye giden ücret, taban birim; göndericiden Amount+Fee düşülür
	ExpiryHeight int64  `json:"expiry_height"` // Dahil edilebileceği son yükseklik (dahil); bkz. tür belgesi
	Nonce        uint64 `json:"nonce"`         // Gönderenin hesap sırası; her işlemde 1 artar (replay koruması)
	PublicKey    []byte `json:"public_key"`    // Gönderenin ML-DSA-65 açık anahtarı
	Signature    []byte `json:"signature"`
	IsCoinbase   bool   `json:"is_coinbase"` // Sistemin ödül olarak bastığı para mı?
}

func writeField(buf *bytes.Buffer, b []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	buf.Write(l[:])
	buf.Write(b)
}

// SigningBytes imzalanan kanonik veridir: ağın imza alanı + gövde. Gövde, Signature
// hariç tüm alanları uzunluk önekli ve sabit sırayla içerir; böylece iki farklı işlem
// aynı baytlara serileşemez.
func (tx *Transaction) SigningBytes(domain string) []byte {
	var buf bytes.Buffer
	writeField(&buf, []byte(domain))
	tx.writeBody(&buf)
	return buf.Bytes()
}

// writeBody imza ve kimlik için ortak, alan etiketsiz gövdeyi yazar.
func (tx *Transaction) writeBody(buf *bytes.Buffer) {
	writeField := func(b []byte) { writeField(buf, b) }
	writeField([]byte(tx.Sender))
	writeField([]byte(tx.Receiver))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(tx.Amount))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(tx.Fee))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(tx.ExpiryHeight))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], tx.Nonce)
	writeField(n[:])
	writeField(tx.PublicKey)
	if tx.IsCoinbase {
		writeField([]byte{1})
	} else {
		writeField([]byte{0})
	}
}

// IDBytes işlemin imza dahil, ağdan bağımsız kimliğidir:
// SHA-256(field(txIDTag) || gövde || Signature). İmza alanını (SigningBytes) asla kullanmaz.
// Gövde uzunluk önekli olduğundan imzanın başladığı yer belirsiz değildir.
func (tx *Transaction) IDBytes() [32]byte {
	var buf bytes.Buffer
	writeField(&buf, []byte(txIDTag))
	tx.writeBody(&buf)
	h := sha256.New()
	h.Write(buf.Bytes())
	h.Write(tx.Signature)
	var id [32]byte
	copy(id[:], h.Sum(nil))
	return id
}

// ID işlem kimliğinin hex gösterimidir.
func (tx *Transaction) ID() string {
	id := tx.IDBytes()
	return hex.EncodeToString(id[:])
}

// Sign işlemi gönderenin anahtarıyla, verilen ağın imza alanında imzalar; Sender ve
// PublicKey alanlarını doldurur. Genesis'i tanımsız ağ (lansman öncesi mainnet) için
// imza atılmaz: aksi halde lansmandan önce atılan imzalar lansmandan sonra geçerli kalırdı.
func (tx *Transaction) Sign(kp *crypto.KeyPair, p *Params) error {
	domain, err := p.SigDomain()
	if err != nil {
		return err
	}
	tx.Sender = kp.Address
	tx.PublicKey = append([]byte(nil), kp.PublicKey...)
	sig, err := crypto.Sign(kp.SecretKey, tx.SigningBytes(domain))
	if err != nil {
		return err
	}
	tx.Signature = sig
	return nil
}

// VerifyStateless durumdan bağımsız kontrolleri yapar: biçim (checkFormat) ve verilen
// ağın imza alanında imza. Coinbase işlemleri burada reddedilir; onlar blok düzeyinde
// doğrulanır. Son kullanma yüksekliği bloğa bağlıdır, bkz. CheckExpiry.
func (tx *Transaction) VerifyStateless(p *Params) error {
	if err := tx.checkFormat(); err != nil {
		return err
	}
	return tx.verifySignature(p)
}

// CheckExpiry işlemin height yüksekliğindeki bir bloğa girip giremeyeceğini söyler.
// Kural tek yerdedir: blok doğrulaması ve blok üretimi (ileride mempool) aynısını kullanır.
func (tx *Transaction) CheckExpiry(height int64) error {
	if height > tx.ExpiryHeight {
		return fmt.Errorf("%w: yükseklik %d, son geçerli %d", ErrTxExpired, height, tx.ExpiryHeight)
	}
	return nil
}

// checkFormat imza doğrulaması gerektirmeyen ucuz kontrolleri yapar.
func (tx *Transaction) checkFormat() error {
	if tx.IsCoinbase {
		return ErrUnexpectedCoinbase
	}
	if tx.Amount <= 0 {
		return ErrNonPositiveAmount
	}
	if tx.Fee < 0 {
		return ErrNegativeFee
	}
	if tx.ExpiryHeight < 1 {
		return ErrBadExpiry
	}
	if !crypto.IsValidAddress(tx.Receiver) {
		return ErrBadReceiver
	}
	if len(tx.PublicKey) != crypto.PublicKeySize || crypto.AddressFromPublicKey(tx.PublicKey) != tx.Sender {
		return ErrBadSender
	}
	if tx.Sender == tx.Receiver {
		return ErrSelfTransfer
	}
	return nil
}

// verifySignature imzayı verilen ağın imza alanında doğrular (ML-DSA, pahalı).
func (tx *Transaction) verifySignature(p *Params) error {
	domain, err := p.SigDomain()
	if err != nil {
		return err
	}
	if !crypto.Verify(tx.PublicKey, tx.SigningBytes(domain), tx.Signature) {
		return ErrBadSignature
	}
	return nil
}

func (tx *Transaction) String() string {
	return fmt.Sprintf("tx{%s -> %s, %s QBTC, ücret=%s, nonce=%d, son=%d}", tx.Sender, tx.Receiver,
		FormatAmount(tx.Amount), FormatAmount(tx.Fee), tx.Nonce, tx.ExpiryHeight)
}
