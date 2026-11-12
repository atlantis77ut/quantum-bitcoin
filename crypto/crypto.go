package crypto

/*
#cgo LDFLAGS: -L/usr/local/lib -loqs -lcrypto
#cgo CFLAGS: -I/usr/local/include
*/
import "C"

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/open-quantum-safe/liboqs-go/oqs"
)

// Algorithm Q-BTC işlemlerinde kullanılan imza algoritmasıdır (FIPS 204).
const Algorithm = "ML-DSA-65"

// ML-DSA-65 sabit boyutları (FIPS 204).
const (
	PublicKeySize = 1952
	SecretKeySize = 4032
	SignatureSize = 3309
)

// AddressPrefix tüm Q-BTC adreslerinin önekidir.
const AddressPrefix = "QBTC"

// addressHexLen adresin önekten sonraki hex uzunluğudur (20 bayt).
const addressHexLen = 40

var (
	ErrInvalidPublicKey = errors.New("geçersiz açık anahtar uzunluğu")
	ErrInvalidSignature = errors.New("geçersiz imza uzunluğu")
	ErrInvalidSecretKey = errors.New("geçersiz gizli anahtar uzunluğu")
	ErrEmptyMessage     = errors.New("imzalanacak mesaj boş")
)

// KeyPair bir ML-DSA-65 anahtar çiftidir. SecretKey asla loglanmamalı,
// diske şifresiz yazılmamalı ve ağ üzerinden gönderilmemelidir.
type KeyPair struct {
	PublicKey []byte
	SecretKey []byte
	Address   string
}

// AddressFromPublicKey açık anahtardan adres türetir: "QBTC" + hex(SHA-256(pk)[:20]).
func AddressFromPublicKey(pubKey []byte) string {
	hash := sha256.Sum256(pubKey)
	return AddressPrefix + hex.EncodeToString(hash[:20])
}

// IsValidAddress adresin biçimsel olarak geçerli olup olmadığını kontrol eder.
func IsValidAddress(addr string) bool {
	if !strings.HasPrefix(addr, AddressPrefix) || len(addr) != len(AddressPrefix)+addressHexLen {
		return false
	}
	h := addr[len(AddressPrefix):]
	if strings.ToLower(h) != h {
		return false // tek kanonik biçim: küçük harf hex
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// GenerateQuantumKeys ML-DSA-65 ile kuantum-dayanıklı anahtar çifti üretir.
func GenerateQuantumKeys() (*KeyPair, error) {
	var signer oqs.Signature
	if err := signer.Init(Algorithm, nil); err != nil {
		return nil, err
	}
	defer signer.Clean()

	pubKey, err := signer.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	secret := signer.ExportSecretKey()

	return &KeyPair{
		PublicKey: append([]byte(nil), pubKey...),
		SecretKey: append([]byte(nil), secret...),
		Address:   AddressFromPublicKey(pubKey),
	}, nil
}

// Sign mesajı verilen gizli anahtarla imzalar.
func Sign(secretKey, message []byte) ([]byte, error) {
	if len(secretKey) != SecretKeySize {
		return nil, ErrInvalidSecretKey
	}
	if len(message) == 0 {
		return nil, ErrEmptyMessage
	}
	// liboqs-go Clean() Init'e verilen dilimi yerinde sıfırlar; çağıranın anahtarı
	// silinmesin diye kopyasını veriyoruz (kopya Clean ile temizlenir).
	keyCopy := append([]byte(nil), secretKey...)
	var signer oqs.Signature
	if err := signer.Init(Algorithm, keyCopy); err != nil {
		return nil, err
	}
	defer signer.Clean()

	sig, err := signer.Sign(message)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), sig...), nil
}

// Verify bir ML-DSA-65 imzasını doğrular. Girdiler ağdan gelebileceği için
// liboqs'a gitmeden önce uzunluklar kontrol edilir (boş dilim liboqs-go'da panik üretir).
func Verify(pubKey, message, signature []byte) bool {
	if len(pubKey) != PublicKeySize || len(signature) != SignatureSize || len(message) == 0 {
		return false
	}
	var verifier oqs.Signature
	if err := verifier.Init(Algorithm, nil); err != nil {
		return false
	}
	defer verifier.Clean()

	ok, err := verifier.Verify(message, signature, pubKey)
	return err == nil && ok
}
