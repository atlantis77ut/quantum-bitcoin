package crypto

import (
	"strings"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	kp, err := GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(kp.PublicKey) != PublicKeySize || len(kp.SecretKey) != SecretKeySize {
		t.Fatalf("anahtar boyutları beklenmedik: pk=%d sk=%d", len(kp.PublicKey), len(kp.SecretKey))
	}
	if kp.Address != AddressFromPublicKey(kp.PublicKey) || !IsValidAddress(kp.Address) {
		t.Fatalf("adres hatalı: %s", kp.Address)
	}

	msg := []byte("transfer 10 QBTC")
	sig, err := Sign(kp.SecretKey, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != SignatureSize {
		t.Fatalf("imza boyutu %d, beklenen %d", len(sig), SignatureSize)
	}
	if !Verify(kp.PublicKey, msg, sig) {
		t.Fatal("geçerli imza doğrulanamadı")
	}
	if Verify(kp.PublicKey, []byte("transfer 99 QBTC"), sig) {
		t.Fatal("değiştirilmiş mesaj doğrulandı")
	}

	other, err := GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	if Verify(other.PublicKey, msg, sig) {
		t.Fatal("başka anahtarla doğrulandı")
	}
}

func TestVerifyMalformedInputsDoNotPanic(t *testing.T) {
	kp, err := GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		pk, msg, sig []byte
	}{
		{"boş imza", kp.PublicKey, []byte("m"), nil},
		{"boş mesaj", kp.PublicKey, nil, make([]byte, SignatureSize)},
		{"boş anahtar", nil, []byte("m"), make([]byte, SignatureSize)},
		{"kısa imza", kp.PublicKey, []byte("m"), []byte{1, 2, 3}},
		{"uzun imza", kp.PublicKey, []byte("m"), make([]byte, SignatureSize+1)},
		{"sıfır imza", kp.PublicKey, []byte("m"), make([]byte, SignatureSize)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if Verify(c.pk, c.msg, c.sig) {
				t.Fatal("bozuk girdi doğrulandı")
			}
		})
	}
}

func TestIsValidAddress(t *testing.T) {
	good := "QBTC" + strings.Repeat("a", 40)
	for addr, want := range map[string]bool{
		good:                             true,
		"QBTC" + strings.Repeat("A", 40): false, // büyük harf kanonik değil
		"QBTC" + strings.Repeat("a", 39): false,
		"QBTC" + strings.Repeat("g", 40): false,
		"XBTC" + strings.Repeat("a", 40): false,
		"QBTC_SATOSHI_QUANTUM":           false,
		"":                               false,
	} {
		if got := IsValidAddress(addr); got != want {
			t.Errorf("IsValidAddress(%q) = %v, beklenen %v", addr, got, want)
		}
	}
}

// Regresyon: liboqs-go Clean() gizli anahtarı yerinde sıfırladığı için ikinci imza bozuluyordu.
func TestSignDoesNotWipeCallerSecretKey(t *testing.T) {
	kp, err := GenerateQuantumKeys()
	if err != nil {
		t.Fatal(err)
	}
	orig := append([]byte(nil), kp.SecretKey...)
	for i := 0; i < 3; i++ {
		msg := []byte{byte(i + 1)}
		sig, err := Sign(kp.SecretKey, msg)
		if err != nil {
			t.Fatal(err)
		}
		if !Verify(kp.PublicKey, msg, sig) {
			t.Fatalf("%d. imza doğrulanamadı", i+1)
		}
	}
	for i := range orig {
		if orig[i] != kp.SecretKey[i] {
			t.Fatal("Sign çağıranın gizli anahtarını değiştirdi")
		}
	}
}

func TestSignRejectsBadSecretKey(t *testing.T) {
	if _, err := Sign([]byte{1, 2, 3}, []byte("m")); err == nil {
		t.Fatal("bozuk gizli anahtar kabul edildi")
	}
}
