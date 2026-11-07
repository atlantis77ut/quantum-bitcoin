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
// İmza ise ayrı bir alanda atılır (Params.SigDomain, "QBTC-SIG-v3|<ağ>|<genesis özeti>"):
// testnet'te atılan imza mainnet'te ya da regtest'te geçersizdir. v3 ile eski
// "QBTC-TX-v2|qbtc-mainnet" imzaları geçersiz oldu.
const txIDTag = "QBTC-TXID-v1"

var (
	ErrNoGenesis          = errors.New("bu ağın genesis bloğu henüz tanımlı değil")
	ErrNonPositiveAmount  = errors.New("tutar pozitif olmalı")
	ErrNegativeFee        = errors.New("işlem ücreti negatif olamaz")
	ErrBadSender          = errors.New("gönderen adresi açık anahtarla eşleşmiyor")
	ErrBadReceiver        = errors.New("geçersiz alıcı adresi")
	ErrSelfTransfer       = errors.New("kendine transfer desteklenmiyor")
	ErrBadSignature       = errors.New("geçersiz imza")
	ErrUnexpectedCoinbase = errors.New("coinbase işlemi yalnızca bloğun ilk işlemi olabilir")
)

type Transaction struct {
	Sender     string `json:"sender"`
	Receiver   string `json:"receiver"`
	Amount     int64  `json:"amount"` // Taban birim (1 QBTC = Coin)
	Fee        int64  `json:"fee"`    // Madenciye giden ücret, taban birim; göndericiden Amount+Fee düşülür
	Timestamp  int64  `json:"timestamp"`
	Nonce      uint64 `json:"nonce"`      // Gönderenin hesap sırası; her işlemde 1 artar (replay koruması)
	PublicKey  []byte `json:"public_key"` // Gönderenin ML-DSA-65 açık anahtarı
	Signature  []byte `json:"signature"`
	IsCoinbase bool   `json:"is_coinbase"` // Sistemin ödül olarak bastığı para mı?
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
	binary.BigEndian.PutUint64(n[:], uint64(tx.Timestamp))
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

// VerifyStateless durumdan bağımsız kontrolleri yapar: tutar, adresler, adres–anahtar
// bağı ve verilen ağın imza alanında imza. Coinbase işlemleri burada reddedilir; onlar
// blok düzeyinde doğrulanır.
func (tx *Transaction) VerifyStateless(p *Params) error {
	domain, err := p.SigDomain()
	if err != nil {
		return err
	}
	if tx.IsCoinbase {
		return ErrUnexpectedCoinbase
	}
	if tx.Amount <= 0 {
		return ErrNonPositiveAmount
	}
	if tx.Fee < 0 {
		return ErrNegativeFee
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
	if !crypto.Verify(tx.PublicKey, tx.SigningBytes(domain), tx.Signature) {
		return ErrBadSignature
	}
	return nil
}

func (tx *Transaction) String() string {
	return fmt.Sprintf("tx{%s -> %s, %s QBTC, ücret=%s, nonce=%d}", tx.Sender, tx.Receiver,
		FormatAmount(tx.Amount), FormatAmount(tx.Fee), tx.Nonce)
}
