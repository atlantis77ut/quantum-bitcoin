package crypto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/bits"
	"slices"
	"strings"
	"testing"
)

// Gömülü vektörün özetleri: selftest_vector.go'daki yeniden üretim komutlarının çıktısıdır
// (OpenSSL 3.5.9, 2026-11-18).
const (
	katPublicKeySHA256 = "d666806e11cee19a7c989f7445f90dd419cf4d2d51db8c0fdb4c0f0a542238c9"
	katSignatureSHA256 = "107c5e279b091046158b19359a3c946e04a78590d46290c28fc295fb462c1c19"
)

func mustKAT(t *testing.T) ([]byte, []byte, []byte) {
	t.Helper()
	pk, msg, sig, err := katVector()
	if err != nil {
		t.Fatal(err)
	}
	return pk, msg, sig
}

// diffAt eşit boylu iki dilimin ayrıldığı bayt konumlarını döndürür.
func diffAt(t *testing.T, a, b []byte) []int {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("boylar farklı: %d ve %d", len(a), len(b))
	}
	var at []int
	for i := range a {
		if a[i] != b[i] {
			at = append(at, i)
		}
	}
	return at
}

// hintSets imzanın ipucu bölümünü sıra, yineleme ve dolgu denetimi yapmadan çözer: polinom
// başına işaretli katsayıların kümesi. Kanoniklik denetimi yapmayan bir doğrulayıcının
// gördüğü ipucudur.
func hintSets(t *testing.T, sig []byte) [sigHintPolys][256]bool {
	t.Helper()
	var sets [sigHintPolys][256]bool
	hint := sig[sigHintOffset:]
	from := 0
	for i := 0; i < sigHintPolys; i++ {
		to := int(hint[sigHintOmega+i])
		if to < from || to > sigHintOmega {
			t.Fatalf("%d. polinomun sayacı %d (önceki %d, ω %d)", i, to, from, sigHintOmega)
		}
		for _, v := range hint[from:to] {
			sets[i][v] = true
		}
		from = to
	}
	return sets
}

// Bağlı liboqs (CI'da sabit sürüm) öz-denetimden geçer.
func TestSelfTest(t *testing.T) {
	if err := SelfTest(); err != nil {
		t.Fatalf("liboqs %s: %v", LibraryVersion(), err)
	}
	if LibraryVersion() == "" {
		t.Fatal("liboqs sürümü boş")
	}
	t.Logf("liboqs %s", LibraryVersion())
}

// Gömülü vektör belgelenen girdilerden üretilmiş olandır ve başka bir uygulamanın (OpenSSL)
// imzası, uzlaşma yolunun kullandığı Verify ile doğrulanır.
func TestKnownAnswerVector(t *testing.T) {
	pk, msg, sig := mustKAT(t)
	if len(pk) != PublicKeySize || len(sig) != SignatureSize {
		t.Fatalf("vektör boyutları: açık anahtar %d, imza %d", len(pk), len(sig))
	}
	if string(msg) != "Q-BTC ML-DSA-65 known-answer test 1" {
		t.Fatalf("vektör mesajı değişmiş: %q", msg)
	}
	if sum := sha256.Sum256(pk); hex.EncodeToString(sum[:]) != katPublicKeySHA256 {
		t.Fatalf("açık anahtar özeti %x", sum)
	}
	if sum := sha256.Sum256(sig); hex.EncodeToString(sum[:]) != katSignatureSHA256 {
		t.Fatalf("imza özeti %x", sum)
	}
	if !Verify(pk, msg, sig) {
		t.Fatalf("liboqs %s OpenSSL'in ürettiği geçerli imzayı doğrulamadı", LibraryVersion())
	}
}

// Reddedilmesi gereken her üçlü geçerli vektörden yalnızca belgelenen yerde ayrılır; ipucu
// bozmaları c̃, z ve ipucu kümesini değiştirmeden FIPS 204'ün kanoniklik denetimlerini (kesin
// artan sıra, sıfır dolgu) hedefler ve bağlı liboqs hepsini reddeder.
func TestKnownAnswerInvalidCases(t *testing.T) {
	pk, msg, sig := mustKAT(t)
	if sigHintOffset+sigHintOmega+sigHintPolys != SignatureSize {
		t.Fatal("imza düzeni sabitleri imza boyuyla tutmuyor")
	}

	// Geçerli imzanın ipucu bölümü kanoniktir: sayaçlar azalmaz ve ω'yı aşmaz, her polinomun
	// indisleri kesin artan sıradadır, kullanılmayan indis baytları sıfırdır.
	hint := sig[sigHintOffset:]
	idx, counts := hint[:sigHintOmega], hint[sigHintOmega:]
	total := 0
	for i, c := range counts {
		n := int(c)
		if n < total || n > sigHintOmega {
			t.Fatalf("%d. polinomun sayacı %d (önceki %d, ω %d)", i, n, total, sigHintOmega)
		}
		for j := total + 1; j < n; j++ {
			if idx[j-1] >= idx[j] {
				t.Fatalf("geçerli vektörde %d. polinomun indisleri artan sırada değil", i)
			}
		}
		total = n
	}
	for j := total; j < sigHintOmega; j++ {
		if idx[j] != 0 {
			t.Fatalf("geçerli vektörde %d. dolgu baytı sıfır değil", j)
		}
	}
	// Bozmaların önkoşulları: ilk polinomda yer değiştirecek iki indis, bir indis daha
	// yazılacak ve bir dolgu baytı kalacak kadar yer.
	if total != katHintCount {
		t.Fatalf("vektörde %d ipucu var, katHintCount %d", total, katHintCount)
	}
	if counts[0] < 2 {
		t.Fatalf("ilk polinomda %d ipucu var, en az iki gerekli", counts[0])
	}
	if total+1 >= sigHintOmega {
		t.Fatalf("%d ipucu var: çiftlenmiş indisten sonra dolgu baytı kalmıyor", total)
	}

	// Çiftleme bozmasının değiştirdiği konumlar: bir sağa kayan indisler ve altı sayaç.
	var repeatAt []int
	for j := 1; j <= total; j++ {
		repeatAt = append(repeatAt, sigHintOffset+j)
	}
	for i := 0; i < sigHintPolys; i++ {
		repeatAt = append(repeatAt, sigHintOffset+sigHintOmega+i)
	}
	cases := katInvalid(pk, msg, sig)
	// Her üçlünün geçerli vektörden ayrıldığı bayt konumları.
	want := []struct {
		name         string
		pk, msg, sig []int
	}{
		{"mesajın bir biti değişti", nil, []int{0}, nil},
		{"açık anahtarın bir biti değişti", []int{PublicKeySize - 1}, nil, nil},
		{"imzanın c̃ alanında bir bit değişti", nil, nil, []int{0}},
		{"ipucu indisleri artan sırada değil", nil, nil, []int{sigHintOffset, sigHintOffset + 1}},
		{"ipucu indisi iki kez yazılmış", nil, nil, repeatAt},
		{"ipucu dolgusu sıfır değil", nil, nil, []int{sigHintOffset + sigHintOmega - 1}},
	}
	if len(cases) != len(want) {
		t.Fatalf("%d üçlü var, beklenen %d", len(cases), len(want))
	}
	for i, c := range cases {
		w := want[i]
		if c.name != w.name {
			t.Fatalf("%d. üçlü %q, beklenen %q", i, c.name, w.name)
		}
		if got := diffAt(t, c.pk, pk); !slices.Equal(got, w.pk) {
			t.Errorf("%s: açık anahtar %v konumlarında ayrılıyor, beklenen %v", c.name, got, w.pk)
		}
		if got := diffAt(t, c.msg, msg); !slices.Equal(got, w.msg) {
			t.Errorf("%s: mesaj %v konumlarında ayrılıyor, beklenen %v", c.name, got, w.msg)
		}
		if got := diffAt(t, c.sig, sig); !slices.Equal(got, w.sig) {
			t.Errorf("%s: imza %v konumlarında ayrılıyor, beklenen %v", c.name, got, w.sig)
		}
		if Verify(c.pk, c.msg, c.sig) {
			t.Errorf("%s: liboqs %s doğruladı", c.name, LibraryVersion())
		}
	}
	// İlk üç üçlü tek bit ayrılır.
	oneBit := func(a, b byte) bool { return bits.OnesCount8(a^b) == 1 }
	last := len(pk) - 1
	if !oneBit(cases[0].msg[0], msg[0]) || !oneBit(cases[1].pk[last], pk[last]) || !oneBit(cases[2].sig[0], sig[0]) {
		t.Error("bit bozmaları tek bit değil")
	}
	// Son üç üçlü yalnızca yeniden kodlamadır: kanoniklik denetimi yapmayan bir doğrulayıcı
	// aynı ipucu kümesini görür, yani imzayı kabul eder.
	valid := hintSets(t, sig)
	for _, c := range cases[3:] {
		if hintSets(t, c.sig) != valid {
			t.Errorf("%s: ipucu kümesi değişti", c.name)
		}
	}
	// Sıra bozması aynı iki indisi yer değiştirir, çiftleme ilk indisi yineler ve sayaçları
	// birer artırır, dolgu bozması sayaçların kapsamadığı bir baytı sıfırdan çıkarır.
	o := sigHintOffset
	if cases[3].sig[o] != sig[o+1] || cases[3].sig[o+1] != sig[o] || sig[o] >= sig[o+1] {
		t.Error("sıra bozması ilk iki indisi yer değiştirmiyor")
	}
	if r := cases[4].sig[o:]; r[0] != r[1] || int(r[sigHintOmega+sigHintPolys-1]) != total+1 {
		t.Error("çiftleme bozması ilk indisi yinelemiyor ya da sayaçları artırmıyor")
	}
	if p := sigHintOffset + sigHintOmega - 1; p < sigHintOffset+total || sig[p] != 0 || cases[5].sig[p] == 0 {
		t.Error("dolgu bozması dolgu baytını değiştirmiyor")
	}

	// katInvalid girdilerini değiştirmez.
	pk2, msg2, sig2 := mustKAT(t)
	if !bytes.Equal(pk, pk2) || !bytes.Equal(msg, msg2) || !bytes.Equal(sig, sig2) {
		t.Fatal("katInvalid geçerli vektörü değiştirdi")
	}
}

// Öz-denetim bozuk doğrulayıcıları yakalar: hepsini kabul eden, hepsini reddeden ve gerçek
// doğrulayıcı gibi davranıp reddedilmesi gereken üçlülerden yalnızca birini kabul eden.
func TestSelfTestDetectsBrokenVerifier(t *testing.T) {
	if err := selfTest(Verify); err != nil {
		t.Fatalf("gerçek doğrulayıcı: %v", err)
	}
	if err := selfTest(func(_, _, _ []byte) bool { return true }); !errors.Is(err, ErrSelfTest) {
		t.Fatalf("hepsini kabul eden doğrulayıcı yakalanmadı: %v", err)
	}
	if err := selfTest(func(_, _, _ []byte) bool { return false }); !errors.Is(err, ErrSelfTest) {
		t.Fatalf("hepsini reddeden doğrulayıcı yakalanmadı: %v", err)
	}
	pk, msg, sig := mustKAT(t)
	for _, c := range katInvalid(pk, msg, sig) {
		lax := func(p, m, s []byte) bool {
			if bytes.Equal(p, c.pk) && bytes.Equal(m, c.msg) && bytes.Equal(s, c.sig) {
				return true
			}
			return Verify(p, m, s)
		}
		if err := selfTest(lax); !errors.Is(err, ErrSelfTest) || !strings.Contains(err.Error(), c.name) {
			t.Errorf("%q üçlüsünü kabul eden doğrulayıcı yakalanmadı: %v", c.name, err)
		}
	}
}

// Bağlı liboqs'ta bulunmayan algoritma öz-denetim hatası verir (ML-DSA-65'i etkin olmayan
// liboqs derlemesinin düştüğü dal).
func TestCheckAlgorithm(t *testing.T) {
	if err := checkAlgorithm(Algorithm); err != nil {
		t.Fatal(err)
	}
	if err := checkAlgorithm("ML-DSA-65-yok"); !errors.Is(err, ErrSelfTest) {
		t.Fatalf("bulunmayan algoritma hata vermedi: %v", err)
	}
}
