//go:build opencl

package gpu

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"os"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// openTest ilk OpenCL aygıtını küçük, sabit tur boyuyla açar. CI'da (QBTC_REQUIRE_OPENCL)
// aygıt yoksa test başarısız olur; yerelde atlanır.
func openTest(t *testing.T) *clMiner {
	t.Helper()
	devs, err := Devices()
	if err != nil || len(devs) == 0 {
		if os.Getenv("QBTC_REQUIRE_OPENCL") != "" {
			t.Fatalf("OpenCL aygıtı yok: %v", err)
		}
		t.Skipf("OpenCL aygıtı yok: %v", err)
	}
	b, err := Open(0, MinBatch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return b.(*clMiner)
}

// refHash çekirdekten bağımsız referanstır: standart kütüphaneyle SHA256d.
func refHash(hdr [core.HeaderSize]byte, nonce uint64) [32]byte {
	binary.BigEndian.PutUint64(hdr[0:8], nonce)
	first := sha256.Sum256(hdr[:])
	return sha256.Sum256(first[:])
}

// testHeader belirlenimci, her bayt dolu bir başlıktır (merkle kökü ve zaman damgası dahil).
func testHeader(seed byte) [core.HeaderSize]byte {
	var hdr [core.HeaderSize]byte
	for i := 0; i < core.HeaderSize; i += 32 {
		h := sha256.Sum256([]byte{seed, byte(i)})
		copy(hdr[i:], h[:])
	}
	return hdr
}

func decrement(b [32]byte) ([32]byte, bool) {
	for i := 31; i >= 0; i-- {
		if b[i] > 0 {
			b[i]--
			return b, true
		}
		b[i] = 0xff
	}
	return b, false
}

// Her aralıkta CPU en küçük özeti bulur ve hedef tam ona eşitlenir: çekirdek yalnızca o
// nonce'u bulmalı ve aynı özeti hesaplamalıdır (yanlış negatif, yanlış pozitif, elde ve
// sarma hataları). Hedef bir eksiltilince hiçbir nonce bulunmamalıdır (<= kuralının sınırı).
func TestKernelMatchesCPU(t *testing.T) {
	checkKernel(t, openTest(t))
}

func checkKernel(t *testing.T, m *clMiner) {
	t.Helper()
	const count = 64
	// Sıfır, alt sözcükten üst sözcüğe elde (iki kez), aralık sonunda ve ortasında 2^64
	// sarması, rastgele bir başlangıç.
	starts := []uint64{
		0,
		0x00000000_FFFFFFF0,
		0x00000001_FFFFFFE0,
		0xFFFFFFFF_FFFFFFC0,
		math.MaxUint64 - count/2 + 1,
		0x12345678_9ABCDEF0,
	}
	for seed := byte(0); seed < 4; seed++ {
		hdr := testHeader(seed)
		for _, start := range starts {
			best, bestNonce := [32]byte{}, uint64(0)
			for i := uint64(0); i < count; i++ {
				h := refHash(hdr, start+i)
				if i == 0 || bytes.Compare(h[:], best[:]) < 0 {
					best, bestNonce = h, start+i
				}
			}
			n, h, ok, err := m.scan(hdr, best, start, count)
			if err != nil || !ok || n != bestNonce || h != best {
				t.Fatalf("tohum %d, başlangıç %x: nonce %x/%x, bulundu %v, err %v\nözet %x\nbekl %x",
					seed, start, n, bestNonce, ok, err, h, best)
			}
			if below, ok := decrement(best); ok {
				if n, _, found, err := m.scan(hdr, below, start, count); err != nil || found {
					t.Fatalf("tohum %d, başlangıç %x: hedef-1'de nonce %x bulundu (err %v)", seed, start, n, err)
				}
			}
		}
	}
}

// Her özeti karşılayan hedefle tek nonce'luk tarama o nonce'u ve doğru özeti döndürür.
func TestKernelSingleNonce(t *testing.T) {
	m := openTest(t)
	var all [32]byte
	for i := range all {
		all[i] = 0xff
	}
	hdr := testHeader(9)
	for _, nonce := range []uint64{0, 1, 0xFFFFFFFF, 0x1_00000000, math.MaxUint64} {
		n, h, ok, err := m.scan(hdr, all, nonce, 1)
		if err != nil || !ok || n != nonce || h != refHash(hdr, nonce) {
			t.Fatalf("nonce %x: %x %v %v", nonce, n, ok, err)
		}
	}
}

// Ayrı bir zaman damgasıyla (ilk bloğun W2–W15'i) ve farklı merkle köküyle (ikinci bloğun
// takvimi) özet değişir ve yine CPU ile eşleşir.
func TestKernelHeaderFields(t *testing.T) {
	m := openTest(t)
	var all [32]byte
	for i := range all {
		all[i] = 0xff
	}
	hdr := testHeader(3)
	for i := 0; i < 4; i++ {
		binary.BigEndian.PutUint64(hdr[20:28], uint64(1_900_000_000+i))
		hdr[95] ^= byte(i)
		_, h, ok, err := m.scan(hdr, all, 42, 1)
		if err != nil || !ok || h != refHash(hdr, 42) {
			t.Fatalf("alan %d: %v %v", i, ok, err)
		}
	}
}

// Backend arayüzü ve sınırlar.
func TestScanBounds(t *testing.T) {
	m := openTest(t)
	var hdr [core.HeaderSize]byte
	var tgt [32]byte
	if _, _, err := m.Scan(hdr, tgt, 0, 0); err == nil {
		t.Fatal("sıfır tur kabul edildi")
	}
	if _, _, err := m.Scan(hdr, tgt, 0, MaxBatch+1); err == nil {
		t.Fatal("MaxBatch aşıldı")
	}
	if m.RoundSize() != MinBatch || m.Name() == "" {
		t.Fatalf("tur %d, ad %q", m.RoundSize(), m.Name())
	}
	if rate, err := core.MeasureBackendHashrate(m, 50*time.Millisecond); err != nil || !(rate > 0) {
		t.Fatalf("ölçüm: %v %v", rate, err)
	}
	m.Close()
	if _, _, err := m.Scan(hdr, tgt, 0, 1); err == nil {
		t.Fatal("kapatılmış kazıcı taradı")
	}
}

func TestOpenErrors(t *testing.T) {
	openTest(t)
	if _, err := Open(-1, 0); err == nil {
		t.Fatal("negatif aygıt kabul edildi")
	}
	if _, err := Open(1<<20, 0); err == nil {
		t.Fatal("olmayan aygıt kabul edildi")
	}
	if _, err := Open(0, MinBatch-1); err == nil {
		t.Fatal("küçük tur kabul edildi")
	}
}

// Otomatik tur boyu sınırlar içinde kalır.
func TestOpenTunes(t *testing.T) {
	openTest(t)
	b, err := Open(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if r := b.RoundSize(); r < MinBatch || r > MaxBatch {
		t.Fatalf("tur boyu %d", r)
	}
}

// go test -tags opencl -run '^$' -bench Kernel ./gpu: ilk aygıtta tek tur (MinBatch·256
// nonce) başına hash/s. CI'da pocl üzerinde yalnızca bilgi içindir; gerçek GPU ölçümü
// -mine-gpu -bench-hashrate ile yapılır.
func BenchmarkKernel(b *testing.B) {
	devs, err := Devices()
	if err != nil || len(devs) == 0 {
		b.Skipf("OpenCL aygıtı yok: %v", err)
	}
	const batch = MinBatch * 256
	be, err := Open(0, batch)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	var hdr [core.HeaderSize]byte
	var tgt [32]byte
	b.ResetTimer()
	start := time.Now()
	for i := 0; i < b.N; i++ {
		if _, _, err := be.Scan(hdr, tgt, uint64(i)*batch, batch); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(b.N)*batch/time.Since(start).Seconds(), "hash/s")
}

// TestKernelAB döngülü (gpu/testdata/sha256d_loop.cl) ve açılmış (gpu/sha256d.cl,
// gpu/gen/genkernel.py ile üretilir) çekirdekleri aynı süreçte ve aynı aygıtta, turlar hâlinde dönüşümlü sırayla ölçer; böylece
// makineler arası fark karşılaştırmayı bozmaz. Önce döngülü çekirdeğin doğruluğu sınanır.
// Yalnızca QBTC_KERNEL_AB ayarlıysa çalışır (CI'da pocl; gerçek GPU ölçümü değildir).
func TestKernelAB(t *testing.T) {
	if os.Getenv("QBTC_KERNEL_AB") == "" {
		t.Skip("QBTC_KERNEL_AB ayarlı değil")
	}
	openTest(t)
	src, err := os.ReadFile("testdata/sha256d_loop.cl")
	if err != nil {
		t.Fatal(err)
	}
	const batch = MinBatch * 256
	names := []string{"döngülü", "açılmış"}
	kernels := make([]Backend, 2)
	for i, source := range []string{string(src), kernelSource} {
		b, err := openSource(0, batch, source)
		if err != nil {
			t.Fatalf("%s: %v", names[i], err)
		}
		defer b.Close()
		kernels[i] = b
	}
	checkKernel(t, kernels[0].(*clMiner))

	var hdr [core.HeaderSize]byte
	var tgt [32]byte
	nonce := uint64(0)
	scan := func(k Backend, n int) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			if _, _, err := k.Scan(hdr, tgt, nonce, batch); err != nil {
				t.Fatal(err)
			}
			nonce += batch
		}
		return time.Since(start)
	}
	for _, k := range kernels { // ısınma
		scan(k, 1)
	}
	const rounds, perRound = 10, 3
	var total [2]time.Duration
	minRatio, maxRatio := math.Inf(1), math.Inf(-1)
	for r := 0; r < rounds; r++ {
		var d [2]time.Duration
		for i := 0; i < 2; i++ {
			idx := (i + r) % 2 // sıra her turda değişir
			d[idx] = scan(kernels[idx], perRound)
			total[idx] += d[idx]
		}
		ratio := d[0].Seconds() / d[1].Seconds() // açılmış hızı / döngülü hızı
		minRatio, maxRatio = math.Min(minRatio, ratio), math.Max(maxRatio, ratio)
	}
	hashes := float64(rounds * perRound * batch)
	for i := range kernels {
		t.Logf("%s: %.0f hash/s", names[i], hashes/total[i].Seconds())
	}
	t.Logf("açılmış/döngülü hız oranı: %.3f (turlarda en düşük %.3f, en yüksek %.3f)",
		total[0].Seconds()/total[1].Seconds(), minRatio, maxRatio)
}
