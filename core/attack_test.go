package core

import (
	"math"
	"math/big"
	"testing"
)

// Nakamoto makalesinin (§11) tabloları: olasılıklar ve P < 0,001 için gereken en küçük z.
func TestAttackerSuccessPaperVectors(t *testing.T) {
	for _, c := range []struct {
		q    float64
		z    int64
		want float64
	}{
		{0.1, 0, 1}, {0.1, 1, 0.2045873}, {0.1, 2, 0.0509779}, {0.1, 5, 0.0009137},
		{0.1, 10, 0.0000012}, {0.3, 5, 0.1773523}, {0.3, 10, 0.0416605}, {0.3, 25, 0.0006132},
		{0.3, 50, 0.0000006},
	} {
		if got := AttackerSuccess(c.q, c.z); math.Abs(got-c.want) > 5e-8 {
			t.Fatalf("q=%v z=%d: %.7f, beklenen %.7f", c.q, c.z, got, c.want)
		}
	}
	for _, c := range []struct {
		q float64
		z int64
	}{
		{0.10, 5}, {0.15, 8}, {0.20, 11}, {0.25, 15}, {0.30, 24}, {0.35, 41}, {0.40, 89}, {0.45, 340},
	} {
		z := int64(0)
		for AttackerSuccess(c.q, z) >= 0.001 {
			z++
		}
		if z != c.z {
			t.Fatalf("q=%v: P < 0,001 için z=%d, beklenen %d", c.q, z, c.z)
		}
	}
}

// Sınırlar ve derinlik: q ≥ 0,5, NaN ya da z ≤ 0 için 1; q = 0 için 0; olasılık z'de azalır ve
// derin bloklarda e^(−λ)'nın sıfıra düşmesi P = 1 vermez; sınırın ötesi sınırdaki değerdir.
func TestAttackerSuccessBounds(t *testing.T) {
	for _, c := range []struct {
		q    float64
		z    int64
		want float64
	}{
		{0.5, 10, 1}, {0.7, 10, 1}, {math.NaN(), 10, 1}, {0.1, -3, 1}, {0, 5, 0}, {-1, 5, 0},
	} {
		if got := AttackerSuccess(c.q, c.z); got != c.want {
			t.Fatalf("q=%v z=%d: %v, beklenen %v", c.q, c.z, got, c.want)
		}
	}
	for _, q := range []float64{0.1, 0.3, 0.45, 0.499} {
		prev := 1.0
		for z := int64(0); z <= 2000; z += 7 {
			got := AttackerSuccess(q, z)
			if math.IsNaN(got) || got < 0 || got > prev+1e-12 {
				t.Fatalf("q=%v z=%d: %v (önceki %v)", q, z, got, prev)
			}
			prev = got
		}
	}
	if got := AttackerSuccess(0.3, 10_000); got > 1e-12 {
		t.Fatalf("derin blok: %v", got)
	}
	if AttackerSuccess(0.45, 1<<40) != AttackerSuccess(0.45, attackDepthCap) {
		t.Fatal("sınırın ötesi sınırdaki değerden farklı")
	}
}

// Model derinliği blok sayısı − 1'dir; işlem bloğunun işi cinsinden üstteki iş ve geçen süreden
// FTL düşülerek karşılık gelen blok sayısıyla sınırlanır (taban zorluğundaki sel z'yi şişirmez).
func TestAttackDepth(t *testing.T) {
	const w = 1000 // işlem bloğunun işi
	for _, c := range []struct {
		blocks, work, elapsed, spacing, want int64
	}{
		{0, 0, 10_000, 60, 0},
		{1, w, 10_000, 60, 0},
		{11, 11 * w, 900, 60, 10},     // iş ve süre yeterli: blok sayısı − 1
		{101, 101 * w, 120, 60, 0},    // iki dakikada 100 blokluk sel
		{101, 101 * w, 1500, 60, 20},  // (1500 − 300) / 60
		{601, w + 600, 36_000, 60, 0}, // 10 saat duraklama, sonra binde bir zorlukta 600 blokluk sel
		{601, 3*w + 600, 36_000, 60, 2},
		{11, 22 * w, 900, 60, 10}, // zorluk arttı: iş sınırı blok sayısını aşmaz
		{5, 5 * w, 10_000, 0, 0},
	} {
		conf := Confirmation{Blocks: c.blocks, Work: big.NewInt(c.work), BlockWork: big.NewInt(w),
			ElapsedSeconds: c.elapsed}
		if got := AttackDepth(conf, c.spacing); got != c.want {
			t.Fatalf("%+v: %d", c, got)
		}
	}
	if AttackDepth(Confirmation{Blocks: 5, ElapsedSeconds: 10_000}, 60) != 0 {
		t.Fatal("iş bilgisi olmadan derinlik verildi")
	}
}
