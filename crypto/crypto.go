package crypto

/*
#cgo LDFLAGS: -L/usr/local/lib -loqs -lcrypto
#cgo CFLAGS: -I/usr/local/include
*/
import "C"

import (
	"crypto/sha256"
	"fmt"

	"github.com/open-quantum-safe/liboqs-go/oqs"
)

type KeyPair struct {
	PublicKey []byte
	Address   string
}

// GenerateQuantumKeys ML-DSA-65 ile kuantum-dayanıklı anahtar çiftini simüle eder
func GenerateQuantumKeys() (*KeyPair, error) {
	var signer oqs.Signature
	if err := signer.Init("ML-DSA-65", nil); err != nil {
		return nil, err
	}
	defer signer.Clean()

	pubKey, err := signer.GenerateKeyPair()
	if err != nil {
		return nil, err
	}

	// Kamu anahtarının hash'ini alarak benzersiz kuantum adres üretiyoruz
	hash := sha256.Sum256(pubKey)
	address := fmt.Sprintf("QBTC%x", hash[:20])

	pubKeyCopy := make([]byte, len(pubKey))
	copy(pubKeyCopy, pubKey)

	return &KeyPair{
		PublicKey: pubKeyCopy,
		Address:   address,
	}, nil
}
