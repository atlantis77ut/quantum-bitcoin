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

// ChainID imzaları tek bir ağa bağlar: testnet'te imzalanan işlem mainnet'te geçersizdir.
// Sabit genesis kararından sonra genesis özeti de buraya eklenmeli.
const ChainID = "qbtc-mainnet"

// txDomain imza verisini başka protokollerin ve ağların mesajlarından ayırır.
// v2: Fee alanı eklendi; v1 imzaları geçersizdir.
const txDomain = "QBTC-TX-v2|" + ChainID

var (
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

// SigningBytes imzalanan kanonik veridir. Signature alanı hariç tüm alanları,
// uzunluk önekli ve sabit sırayla içerir; böylece iki farklı işlem aynı baytlara
// serileşemez.
func (tx *Transaction) SigningBytes() []byte {
	var buf bytes.Buffer
	writeField := func(b []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(b)))
		buf.Write(l[:])
		buf.Write(b)
	}
	writeField([]byte(txDomain))
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
	return buf.Bytes()
}

// ID işlemin imza dahil benzersiz kimliğidir.
func (tx *Transaction) ID() string {
	h := sha256.New()
	h.Write(tx.SigningBytes())
	h.Write(tx.Signature)
	return hex.EncodeToString(h.Sum(nil))
}

// Sign işlemi gönderenin anahtarıyla imzalar; Sender ve PublicKey alanlarını doldurur.
func (tx *Transaction) Sign(kp *crypto.KeyPair) error {
	tx.Sender = kp.Address
	tx.PublicKey = append([]byte(nil), kp.PublicKey...)
	sig, err := crypto.Sign(kp.SecretKey, tx.SigningBytes())
	if err != nil {
		return err
	}
	tx.Signature = sig
	return nil
}

// VerifyStateless durumdan bağımsız kontrolleri yapar: tutar, adresler, adres–anahtar
// bağı ve imza. Coinbase işlemleri burada reddedilir; onlar blok düzeyinde doğrulanır.
func (tx *Transaction) VerifyStateless() error {
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
	if !crypto.Verify(tx.PublicKey, tx.SigningBytes(), tx.Signature) {
		return ErrBadSignature
	}
	return nil
}

func (tx *Transaction) String() string {
	return fmt.Sprintf("tx{%s -> %s, %s QBTC, ücret=%s, nonce=%d}", tx.Sender, tx.Receiver,
		FormatAmount(tx.Amount), FormatAmount(tx.Fee), tx.Nonce)
}
