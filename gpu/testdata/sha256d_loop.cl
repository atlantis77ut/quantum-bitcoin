// Q-BTC başlığı için döngülü SHA256d tarama çekirdeği (OpenCL 1.2). Yalnızca A/B testi
// (gpu/opencl_test.go, TestKernelAB) içindir; kazıcı gpu/sha256d.cl'deki açılmış çekirdeği
// kullanır. 2026-11-12'de CI'da pocl üzerinde açılmış çekirdek bundan ~%26 hızlıydı.
//
// Başlık 96 bayttır, nonce büyük-endian olarak [0:8]'dedir: ilk SHA-256 bloğunun W0 (üst
// 32 bit) ve W1 (alt 32 bit) sözcükleri. Konak:
//   w1   ilk bloğun 16 sözcüğü (W0 ve W1 burada nonce ile değiştirilir),
//   w2   ikinci bloğun (merkle kökü + dolgu) önceden genişletilmiş 64 sözcüklük takvimi,
//   tgt  hedefin 8 büyük-endian sözcüğü (kural: özet <= hedef),
//   base nonce aralığının başı (hi, lo); iş öğesi gid, base + gid nonce'unu dener.
// Bulan ilk iş öğesi out[0]'ı 0'dan 1'e çevirir ve out[1..2]'ye nonce'u, out[3..10]'a özeti
// yazar. 64 bitlik atomikler 1.2'de garantili olmadığından 32 bitlik bayrak kullanılır.

#define ROTR(x, n) rotate((uint)(x), (uint)(32u - (n)))
#define CH(x, y, z) bitselect((z), (y), (x))
#define MAJ(x, y, z) bitselect((x), (y), ((z) ^ (x)))
#define BS0(x) (ROTR((x), 2) ^ ROTR((x), 13) ^ ROTR((x), 22))
#define BS1(x) (ROTR((x), 6) ^ ROTR((x), 11) ^ ROTR((x), 25))
#define SS0(x) (ROTR((x), 7) ^ ROTR((x), 18) ^ ((x) >> 3))
#define SS1(x) (ROTR((x), 17) ^ ROTR((x), 19) ^ ((x) >> 10))

__constant uint K[64] = {
	0x428a2f98u, 0x71374491u, 0xb5c0fbcfu, 0xe9b5dba5u, 0x3956c25bu, 0x59f111f1u, 0x923f82a4u, 0xab1c5ed5u,
	0xd807aa98u, 0x12835b01u, 0x243185beu, 0x550c7dc3u, 0x72be5d74u, 0x80deb1feu, 0x9bdc06a7u, 0xc19bf174u,
	0xe49b69c1u, 0xefbe4786u, 0x0fc19dc6u, 0x240ca1ccu, 0x2de92c6fu, 0x4a7484aau, 0x5cb0a9dcu, 0x76f988dau,
	0x983e5152u, 0xa831c66du, 0xb00327c8u, 0xbf597fc7u, 0xc6e00bf3u, 0xd5a79147u, 0x06ca6351u, 0x14292967u,
	0x27b70a85u, 0x2e1b2138u, 0x4d2c6dfcu, 0x53380d13u, 0x650a7354u, 0x766a0abbu, 0x81c2c92eu, 0x92722c85u,
	0xa2bfe8a1u, 0xa81a664bu, 0xc24b8b70u, 0xc76c51a3u, 0xd192e819u, 0xd6990624u, 0xf40e3585u, 0x106aa070u,
	0x19a4c116u, 0x1e376c08u, 0x2748774cu, 0x34b0bcb5u, 0x391c0cb3u, 0x4ed8aa4au, 0x5b9cca4fu, 0x682e6ff3u,
	0x748f82eeu, 0x78a5636fu, 0x84c87814u, 0x8cc70208u, 0x90befffau, 0xa4506cebu, 0xbef9a3f7u, 0xc67178f2u,
};

#define IV0 0x6a09e667u
#define IV1 0xbb67ae85u
#define IV2 0x3c6ef372u
#define IV3 0xa54ff53au
#define IV4 0x510e527fu
#define IV5 0x9b05688cu
#define IV6 0x1f83d9abu
#define IV7 0x5be0cd19u

#define ROUNDS(WI)                                                          \
	uint a = st[0], b = st[1], c = st[2], d = st[3];                        \
	uint e = st[4], f = st[5], g = st[6], h = st[7];                        \
	for (int i = 0; i < 64; i++) {                                          \
		uint t1 = h + BS1(e) + CH(e, f, g) + K[i] + (WI);                   \
		uint t2 = BS0(a) + MAJ(a, b, c);                                    \
		h = g;                                                              \
		g = f;                                                              \
		f = e;                                                              \
		e = d + t1;                                                         \
		d = c;                                                              \
		c = b;                                                              \
		b = a;                                                              \
		a = t1 + t2;                                                        \
	}                                                                       \
	st[0] += a;                                                             \
	st[1] += b;                                                             \
	st[2] += c;                                                             \
	st[3] += d;                                                             \
	st[4] += e;                                                             \
	st[5] += f;                                                             \
	st[6] += g;                                                             \
	st[7] += h;

// compress_p özel bellekteki 64 sözcüklük takvimle bir SHA-256 sıkıştırması yapar.
void compress_p(uint *st, const uint *w) { ROUNDS(w[i]) }

// compress_c sabit bellekteki (konakta genişletilmiş) takvimle sıkıştırır.
void compress_c(uint *st, __constant uint *w) { ROUNDS(w[i]) }

void expand(uint *w) {
	for (int i = 16; i < 64; i++) {
		w[i] = SS1(w[i - 2]) + w[i - 7] + SS0(w[i - 15]) + w[i - 16];
	}
}

__kernel void scan(__constant uint *w1, __constant uint *w2, __constant uint *tgt,
                   uint base_hi, uint base_lo, __global volatile uint *out)
{
	uint gid = (uint)get_global_id(0);
	uint lo = base_lo + gid;
	uint hi = base_hi + (lo < base_lo ? 1u : 0u); // alt sözcükten üst sözcüğe elde

	uint w[64];
	w[0] = hi;
	w[1] = lo;
	for (int i = 2; i < 16; i++) {
		w[i] = w1[i];
	}
	expand(w);
	uint st[8] = {IV0, IV1, IV2, IV3, IV4, IV5, IV6, IV7};
	compress_p(st, w);
	compress_c(st, w2);

	// İkinci SHA-256: 32 baytlık özet + dolgu (0x80, uzunluk 256 bit).
	for (int i = 0; i < 8; i++) {
		w[i] = st[i];
	}
	w[8] = 0x80000000u;
	for (int i = 9; i < 15; i++) {
		w[i] = 0u;
	}
	w[15] = 256u;
	expand(w);
	uint hs[8] = {IV0, IV1, IV2, IV3, IV4, IV5, IV6, IV7};
	compress_p(hs, w);

	// Büyük-endian 256 bitlik karşılaştırma: özet <= hedef.
	int ok = 1;
	for (int i = 0; i < 8; i++) {
		if (hs[i] < tgt[i]) {
			break;
		}
		if (hs[i] > tgt[i]) {
			ok = 0;
			break;
		}
	}
	if (ok && atomic_cmpxchg(&out[0], 0u, 1u) == 0u) {
		out[1] = hi;
		out[2] = lo;
		for (int i = 0; i < 8; i++) {
			out[3 + i] = hs[i];
		}
	}
}
