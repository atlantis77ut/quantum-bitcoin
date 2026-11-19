package crypto

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/open-quantum-safe/liboqs-go/oqs"
)

// ErrSelfTest bağlı liboqs ML-DSA-65 bilinen-yanıt denetiminden geçemediğinde döner.
var ErrSelfTest = errors.New("ML-DSA-65 bilinen-yanıt denetimi başarısız")

// ML-DSA-65 imza düzeni (FIPS 204, sigEncode): c̃ (48 bayt), z (5 × 640 bayt), ipucu bölümü
// (ω = 55 indis baytı ve ardından k = 6 birikimli sayaç baytı).
const (
	sigHintOffset = 48 + 5*640 // ipucu bölümünün başı
	sigHintOmega  = 55         // ω: indis alanının boyu, en çok ipucu sayısı
	sigHintPolys  = 6          // k: sayaç baytı sayısı
)

// katHintCount gömülü vektörün imzasındaki ipucu sayısıdır (ω'dan az: indis alanının sonu
// dolgudur). TestKnownAnswerInvalidCases imzadaki sayaçlarla karşılaştırır.
const katHintCount = 41

// LibraryVersion bağlı liboqs'un sürümüdür. Yalnızca bilgi içindir (günlük, hata iletisi):
// kütüphanenin kabulü sürüme değil SelfTest'e bağlıdır.
func LibraryVersion() string {
	return oqs.LiboqsVersion()
}

// SelfTest bağlı liboqs'un ML-DSA-65'ini sabit bir vektörle (selftest_vector.go) denetler.
// İmza doğrulaması uzlaşmanın parçasıdır: ML-DSA-65'i etkin olmayan ya da farklı davranan
// bir liboqs ile çalışan düğüm açılır, ilk 240 blokta (imzalı işlem olamaz: coinbase
// olgunluğu) uyumlu görünür ve imzalı ilk işlemi içeren blokta uyarı vermeden ağdan kopar.
// Bu yüzden her çalıştırılabilir, başka hiçbir şey yapmadan önce bunu çağırır ve hata
// dönerse çalışmaz.
//
// Denetlenenler: algoritmanın kullanılabilirliği; başka bir uygulamanın (OpenSSL) ürettiği
// geçerli imzanın doğrulanması; mesajı, açık anahtarı ya da c̃ alanı değişmiş imzanın reddi;
// ipucu bölümü kanonik olmayan üç imzanın reddi (FIPS 204 HintBitUnpack: indisler kesin
// artan sırada, dolgu sıfır). Son üçü aynı c̃, z ve ipucu kümesini başka baytlarla kodlar:
// onları kabul eden bir kütüphane, üçüncü kişilerin imzayı ve onunla işlem kimliğini (tanık
// özeti kimliğe girer) değiştirebilmesine izin verir ve onları reddeden düğümlerle ayrışır.
//
// Kapsamadıkları: uygulamanın genel doğruluğu, sabit zamanlılığı, imzalama yolu ve z'nin
// norm sınırı (sınırdaki bir imza ancak değiştirilmiş bir imzalayıcıyla üretilebilir).
// Denetimin geçmesi kütüphanenin FIPS 204'e tam uyduğunu kanıtlamaz.
func SelfTest() error {
	if err := checkAlgorithm(Algorithm); err != nil {
		return err
	}
	return selfTest(Verify)
}

// checkAlgorithm algoritmanın bağlı liboqs derlemesinde kullanılabildiğini denetler.
func checkAlgorithm(name string) error {
	var s oqs.Signature
	if err := s.Init(name, nil); err != nil {
		return fmt.Errorf("%w: %s bu liboqs derlemesinde kullanılamıyor: %v", ErrSelfTest, name, err)
	}
	s.Clean()
	return nil
}

// selfTest vektör denetimini verilen doğrulayıcıyla yürütür (testler bozuk doğrulayıcıların
// yakalandığını gösterebilsin diye SelfTest'ten ayrıdır).
func selfTest(verify func(pubKey, message, signature []byte) bool) error {
	pk, msg, sig, err := katVector()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSelfTest, err)
	}
	if !verify(pk, msg, sig) {
		return fmt.Errorf("%w: başka bir uygulamanın ürettiği geçerli imza doğrulanmadı", ErrSelfTest)
	}
	for _, c := range katInvalid(pk, msg, sig) {
		if verify(c.pk, c.msg, c.sig) {
			return fmt.Errorf("%w: geçersiz imza doğrulandı (%s)", ErrSelfTest, c.name)
		}
	}
	return nil
}

// katVector gömülü vektörü çözer: açık anahtar, mesaj, imza.
func katVector() (pk, msg, sig []byte, err error) {
	if pk, err = hex.DecodeString(katPublicKeyHex); err != nil {
		return nil, nil, nil, fmt.Errorf("vektörün açık anahtarı çözülemedi: %v", err)
	}
	if sig, err = hex.DecodeString(katSignatureHex); err != nil {
		return nil, nil, nil, fmt.Errorf("vektörün imzası çözülemedi: %v", err)
	}
	if len(pk) != PublicKeySize || len(sig) != SignatureSize {
		return nil, nil, nil, fmt.Errorf("vektör boyutları beklenmedik: açık anahtar %d, imza %d", len(pk), len(sig))
	}
	return pk, []byte(katMessage), sig, nil
}

// katCase reddedilmesi gereken bir (açık anahtar, mesaj, imza) üçlüsüdür.
type katCase struct {
	name         string
	pk, msg, sig []byte
}

// katInvalid geçerli vektörden türetilen, reddedilmesi gereken üçlüleri döndürür; girdileri
// değiştirmez. Son üçü ipucu bölümünü kanonik olmayan biçimde yeniden kodlar ve bu vektörün
// ipucu bölümüne göre seçilmiştir: ilk polinomun en az iki ipucu vardır ve ipucu sayısı
// (katHintCount) ω'dan azdır. TestKnownAnswerInvalidCases bu yapıyı denetler.
func katInvalid(pk, msg, sig []byte) []katCase {
	flip := func(b []byte, i int) []byte {
		c := append([]byte(nil), b...)
		c[i] ^= 1
		return c
	}
	// İlk polinomun ilk iki indisi yer değiştirir: indisler artan sırada değildir.
	order := append([]byte(nil), sig...)
	order[sigHintOffset], order[sigHintOffset+1] = order[sigHintOffset+1], order[sigHintOffset]
	// İlk indis iki kez yazılır: kalan indisler bir sağa kayar, altı sayaç birer artar.
	repeat := append([]byte(nil), sig...)
	hint, valid := repeat[sigHintOffset:], sig[sigHintOffset:]
	copy(hint[1:], valid[:katHintCount])
	for i := sigHintOmega; i < sigHintOmega+sigHintPolys; i++ {
		hint[i]++
	}
	// İndis alanının son baytı (dolgu) sıfırdan çıkar.
	padding := append([]byte(nil), sig...)
	padding[sigHintOffset+sigHintOmega-1] = 1
	return []katCase{
		{"mesajın bir biti değişti", pk, flip(msg, 0), sig},
		{"açık anahtarın bir biti değişti", flip(pk, len(pk)-1), msg, sig},
		{"imzanın c̃ alanında bir bit değişti", pk, msg, flip(sig, 0)},
		{"ipucu indisleri artan sırada değil", pk, msg, order},
		{"ipucu indisi iki kez yazılmış", pk, msg, repeat},
		{"ipucu dolgusu sıfır değil", pk, msg, padding},
	}
}
