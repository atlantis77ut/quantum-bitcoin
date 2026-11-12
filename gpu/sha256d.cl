// Q-BTC başlığı için SHA256d tarama çekirdeği (OpenCL 1.2).
//
// Başlık 96 bayttır, nonce büyük-endian olarak [0:8]'dedir: ilk SHA-256 bloğunun W0 (üst
// 32 bit) ve W1 (alt 32 bit) sözcükleri. Konak:
//   w1   ilk bloğun 16 sözcüğü (W0 ve W1 burada nonce ile değiştirilir),
//   w2   ikinci bloğun (merkle kökü + dolgu) önceden genişletilmiş 64 sözcüklük takvimi,
//   tgt  hedefin 8 büyük-endian sözcüğü (kural: özet <= hedef),
//   base nonce aralığının başı (hi, lo); iş öğesi gid, base + gid nonce'unu dener.
// Bulan ilk iş öğesi out[0]'ı 0'dan 1'e çevirir ve out[1..2]'ye nonce'u, out[3..10]'a özeti
// yazar. 64 bitlik atomikler 1.2'de garantili olmadığından 32 bitlik bayrak kullanılır.
//
// Bu dosyanın gövdesi gpu/gen/genkernel.py ile üretilir (elle düzenlemeyin): üç sıkıştırma
// tamamen açılmıştır, takvim 16 sözcüklük kayan pencerededir ve değişken indeksli dizi yoktur;
// böylece durum GPU yazmaçlarında kalır ve ikinci SHA'nın sabit dolgusu derleyicide katlanır.
// Üreteç aynı işlem dizisini Python'da standart SHA-256'ya karşı doğrular.

#define ROTR(x, n) rotate((uint)(x), (uint)(32u - (n)))
#define CH(x, y, z) bitselect((z), (y), (x))
#define MAJ(x, y, z) bitselect((x), (y), ((z) ^ (x)))
#define BS0(x) (ROTR((x), 2) ^ ROTR((x), 13) ^ ROTR((x), 22))
#define BS1(x) (ROTR((x), 6) ^ ROTR((x), 11) ^ ROTR((x), 25))
#define SS0(x) (ROTR((x), 7) ^ ROTR((x), 18) ^ ((x) >> 3))
#define SS1(x) (ROTR((x), 17) ^ ROTR((x), 19) ^ ((x) >> 10))

// RND bir SHA-256 turudur; değişkenler kaydırılmaz, adları her turda döner.
#define RND(a, b, c, d, e, f, g, h, k, w)                              \
	{                                                                  \
		uint t1_ = (h) + BS1(e) + CH((e), (f), (g)) + (k) + (w);       \
		(d) += t1_;                                                    \
		(h) = t1_ + BS0(a) + MAJ((a), (b), (c));                       \
	}

#define IV0 0x6a09e667u
#define IV1 0xbb67ae85u
#define IV2 0x3c6ef372u
#define IV3 0xa54ff53au
#define IV4 0x510e527fu
#define IV5 0x9b05688cu
#define IV6 0x1f83d9abu
#define IV7 0x5be0cd19u

__kernel void scan(__constant uint *w1, __constant uint *w2, __constant uint *tgt,
                   uint base_hi, uint base_lo, __global volatile uint *out)
{
	uint gid = (uint)get_global_id(0);
	uint lo = base_lo + gid;
	uint hi = base_hi + (lo < base_lo ? 1u : 0u); // alt sözcükten üst sözcüğe elde

	uint a, b, c, d, e, f, g, h;
	uint x0, x1, x2, x3, x4, x5, x6, x7, x8, x9, x10, x11, x12, x13, x14, x15;
	uint s0, s1, s2, s3, s4, s5, s6, s7;
	uint hs0, hs1, hs2, hs3, hs4, hs5, hs6, hs7;

	x0 = hi;
	x1 = lo;
	x2 = w1[2];
	x3 = w1[3];
	x4 = w1[4];
	x5 = w1[5];
	x6 = w1[6];
	x7 = w1[7];
	x8 = w1[8];
	x9 = w1[9];
	x10 = w1[10];
	x11 = w1[11];
	x12 = w1[12];
	x13 = w1[13];
	x14 = w1[14];
	x15 = w1[15];
	a = IV0;
	b = IV1;
	c = IV2;
	d = IV3;
	e = IV4;
	f = IV5;
	g = IV6;
	h = IV7;
	RND(a, b, c, d, e, f, g, h, 0x428a2f98u, x0);
	RND(h, a, b, c, d, e, f, g, 0x71374491u, x1);
	RND(g, h, a, b, c, d, e, f, 0xb5c0fbcfu, x2);
	RND(f, g, h, a, b, c, d, e, 0xe9b5dba5u, x3);
	RND(e, f, g, h, a, b, c, d, 0x3956c25bu, x4);
	RND(d, e, f, g, h, a, b, c, 0x59f111f1u, x5);
	RND(c, d, e, f, g, h, a, b, 0x923f82a4u, x6);
	RND(b, c, d, e, f, g, h, a, 0xab1c5ed5u, x7);
	RND(a, b, c, d, e, f, g, h, 0xd807aa98u, x8);
	RND(h, a, b, c, d, e, f, g, 0x12835b01u, x9);
	RND(g, h, a, b, c, d, e, f, 0x243185beu, x10);
	RND(f, g, h, a, b, c, d, e, 0x550c7dc3u, x11);
	RND(e, f, g, h, a, b, c, d, 0x72be5d74u, x12);
	RND(d, e, f, g, h, a, b, c, 0x80deb1feu, x13);
	RND(c, d, e, f, g, h, a, b, 0x9bdc06a7u, x14);
	RND(b, c, d, e, f, g, h, a, 0xc19bf174u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0xe49b69c1u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0xefbe4786u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x0fc19dc6u, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x240ca1ccu, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x2de92c6fu, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x4a7484aau, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x5cb0a9dcu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x76f988dau, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0x983e5152u, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0xa831c66du, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0xb00327c8u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0xbf597fc7u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0xc6e00bf3u, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xd5a79147u, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0x06ca6351u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0x14292967u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0x27b70a85u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0x2e1b2138u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x4d2c6dfcu, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x53380d13u, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x650a7354u, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x766a0abbu, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x81c2c92eu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x92722c85u, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0xa2bfe8a1u, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0xa81a664bu, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0xc24b8b70u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0xc76c51a3u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0xd192e819u, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xd6990624u, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0xf40e3585u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0x106aa070u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0x19a4c116u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0x1e376c08u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x2748774cu, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x34b0bcb5u, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x391c0cb3u, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x4ed8aa4au, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x5b9cca4fu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x682e6ff3u, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0x748f82eeu, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0x78a5636fu, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0x84c87814u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0x8cc70208u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0x90befffau, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xa4506cebu, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0xbef9a3f7u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0xc67178f2u, x15);
	s0 = IV0 + a;
	s1 = IV1 + b;
	s2 = IV2 + c;
	s3 = IV3 + d;
	s4 = IV4 + e;
	s5 = IV5 + f;
	s6 = IV6 + g;
	s7 = IV7 + h;
	a = s0;
	b = s1;
	c = s2;
	d = s3;
	e = s4;
	f = s5;
	g = s6;
	h = s7;
	RND(a, b, c, d, e, f, g, h, 0x428a2f98u, w2[0]);
	RND(h, a, b, c, d, e, f, g, 0x71374491u, w2[1]);
	RND(g, h, a, b, c, d, e, f, 0xb5c0fbcfu, w2[2]);
	RND(f, g, h, a, b, c, d, e, 0xe9b5dba5u, w2[3]);
	RND(e, f, g, h, a, b, c, d, 0x3956c25bu, w2[4]);
	RND(d, e, f, g, h, a, b, c, 0x59f111f1u, w2[5]);
	RND(c, d, e, f, g, h, a, b, 0x923f82a4u, w2[6]);
	RND(b, c, d, e, f, g, h, a, 0xab1c5ed5u, w2[7]);
	RND(a, b, c, d, e, f, g, h, 0xd807aa98u, w2[8]);
	RND(h, a, b, c, d, e, f, g, 0x12835b01u, w2[9]);
	RND(g, h, a, b, c, d, e, f, 0x243185beu, w2[10]);
	RND(f, g, h, a, b, c, d, e, 0x550c7dc3u, w2[11]);
	RND(e, f, g, h, a, b, c, d, 0x72be5d74u, w2[12]);
	RND(d, e, f, g, h, a, b, c, 0x80deb1feu, w2[13]);
	RND(c, d, e, f, g, h, a, b, 0x9bdc06a7u, w2[14]);
	RND(b, c, d, e, f, g, h, a, 0xc19bf174u, w2[15]);
	RND(a, b, c, d, e, f, g, h, 0xe49b69c1u, w2[16]);
	RND(h, a, b, c, d, e, f, g, 0xefbe4786u, w2[17]);
	RND(g, h, a, b, c, d, e, f, 0x0fc19dc6u, w2[18]);
	RND(f, g, h, a, b, c, d, e, 0x240ca1ccu, w2[19]);
	RND(e, f, g, h, a, b, c, d, 0x2de92c6fu, w2[20]);
	RND(d, e, f, g, h, a, b, c, 0x4a7484aau, w2[21]);
	RND(c, d, e, f, g, h, a, b, 0x5cb0a9dcu, w2[22]);
	RND(b, c, d, e, f, g, h, a, 0x76f988dau, w2[23]);
	RND(a, b, c, d, e, f, g, h, 0x983e5152u, w2[24]);
	RND(h, a, b, c, d, e, f, g, 0xa831c66du, w2[25]);
	RND(g, h, a, b, c, d, e, f, 0xb00327c8u, w2[26]);
	RND(f, g, h, a, b, c, d, e, 0xbf597fc7u, w2[27]);
	RND(e, f, g, h, a, b, c, d, 0xc6e00bf3u, w2[28]);
	RND(d, e, f, g, h, a, b, c, 0xd5a79147u, w2[29]);
	RND(c, d, e, f, g, h, a, b, 0x06ca6351u, w2[30]);
	RND(b, c, d, e, f, g, h, a, 0x14292967u, w2[31]);
	RND(a, b, c, d, e, f, g, h, 0x27b70a85u, w2[32]);
	RND(h, a, b, c, d, e, f, g, 0x2e1b2138u, w2[33]);
	RND(g, h, a, b, c, d, e, f, 0x4d2c6dfcu, w2[34]);
	RND(f, g, h, a, b, c, d, e, 0x53380d13u, w2[35]);
	RND(e, f, g, h, a, b, c, d, 0x650a7354u, w2[36]);
	RND(d, e, f, g, h, a, b, c, 0x766a0abbu, w2[37]);
	RND(c, d, e, f, g, h, a, b, 0x81c2c92eu, w2[38]);
	RND(b, c, d, e, f, g, h, a, 0x92722c85u, w2[39]);
	RND(a, b, c, d, e, f, g, h, 0xa2bfe8a1u, w2[40]);
	RND(h, a, b, c, d, e, f, g, 0xa81a664bu, w2[41]);
	RND(g, h, a, b, c, d, e, f, 0xc24b8b70u, w2[42]);
	RND(f, g, h, a, b, c, d, e, 0xc76c51a3u, w2[43]);
	RND(e, f, g, h, a, b, c, d, 0xd192e819u, w2[44]);
	RND(d, e, f, g, h, a, b, c, 0xd6990624u, w2[45]);
	RND(c, d, e, f, g, h, a, b, 0xf40e3585u, w2[46]);
	RND(b, c, d, e, f, g, h, a, 0x106aa070u, w2[47]);
	RND(a, b, c, d, e, f, g, h, 0x19a4c116u, w2[48]);
	RND(h, a, b, c, d, e, f, g, 0x1e376c08u, w2[49]);
	RND(g, h, a, b, c, d, e, f, 0x2748774cu, w2[50]);
	RND(f, g, h, a, b, c, d, e, 0x34b0bcb5u, w2[51]);
	RND(e, f, g, h, a, b, c, d, 0x391c0cb3u, w2[52]);
	RND(d, e, f, g, h, a, b, c, 0x4ed8aa4au, w2[53]);
	RND(c, d, e, f, g, h, a, b, 0x5b9cca4fu, w2[54]);
	RND(b, c, d, e, f, g, h, a, 0x682e6ff3u, w2[55]);
	RND(a, b, c, d, e, f, g, h, 0x748f82eeu, w2[56]);
	RND(h, a, b, c, d, e, f, g, 0x78a5636fu, w2[57]);
	RND(g, h, a, b, c, d, e, f, 0x84c87814u, w2[58]);
	RND(f, g, h, a, b, c, d, e, 0x8cc70208u, w2[59]);
	RND(e, f, g, h, a, b, c, d, 0x90befffau, w2[60]);
	RND(d, e, f, g, h, a, b, c, 0xa4506cebu, w2[61]);
	RND(c, d, e, f, g, h, a, b, 0xbef9a3f7u, w2[62]);
	RND(b, c, d, e, f, g, h, a, 0xc67178f2u, w2[63]);
	s0 = s0 + a;
	s1 = s1 + b;
	s2 = s2 + c;
	s3 = s3 + d;
	s4 = s4 + e;
	s5 = s5 + f;
	s6 = s6 + g;
	s7 = s7 + h;
	x0 = s0;
	x1 = s1;
	x2 = s2;
	x3 = s3;
	x4 = s4;
	x5 = s5;
	x6 = s6;
	x7 = s7;
	x8 = 0x80000000u;
	x9 = 0u;
	x10 = 0u;
	x11 = 0u;
	x12 = 0u;
	x13 = 0u;
	x14 = 0u;
	x15 = 256u;
	a = IV0;
	b = IV1;
	c = IV2;
	d = IV3;
	e = IV4;
	f = IV5;
	g = IV6;
	h = IV7;
	RND(a, b, c, d, e, f, g, h, 0x428a2f98u, x0);
	RND(h, a, b, c, d, e, f, g, 0x71374491u, x1);
	RND(g, h, a, b, c, d, e, f, 0xb5c0fbcfu, x2);
	RND(f, g, h, a, b, c, d, e, 0xe9b5dba5u, x3);
	RND(e, f, g, h, a, b, c, d, 0x3956c25bu, x4);
	RND(d, e, f, g, h, a, b, c, 0x59f111f1u, x5);
	RND(c, d, e, f, g, h, a, b, 0x923f82a4u, x6);
	RND(b, c, d, e, f, g, h, a, 0xab1c5ed5u, x7);
	RND(a, b, c, d, e, f, g, h, 0xd807aa98u, x8);
	RND(h, a, b, c, d, e, f, g, 0x12835b01u, x9);
	RND(g, h, a, b, c, d, e, f, 0x243185beu, x10);
	RND(f, g, h, a, b, c, d, e, 0x550c7dc3u, x11);
	RND(e, f, g, h, a, b, c, d, 0x72be5d74u, x12);
	RND(d, e, f, g, h, a, b, c, 0x80deb1feu, x13);
	RND(c, d, e, f, g, h, a, b, 0x9bdc06a7u, x14);
	RND(b, c, d, e, f, g, h, a, 0xc19bf174u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0xe49b69c1u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0xefbe4786u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x0fc19dc6u, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x240ca1ccu, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x2de92c6fu, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x4a7484aau, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x5cb0a9dcu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x76f988dau, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0x983e5152u, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0xa831c66du, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0xb00327c8u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0xbf597fc7u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0xc6e00bf3u, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xd5a79147u, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0x06ca6351u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0x14292967u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0x27b70a85u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0x2e1b2138u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x4d2c6dfcu, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x53380d13u, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x650a7354u, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x766a0abbu, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x81c2c92eu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x92722c85u, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0xa2bfe8a1u, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0xa81a664bu, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0xc24b8b70u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0xc76c51a3u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0xd192e819u, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xd6990624u, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0xf40e3585u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0x106aa070u, x15);
	x0 = SS1(x14) + x9 + SS0(x1) + x0;
	RND(a, b, c, d, e, f, g, h, 0x19a4c116u, x0);
	x1 = SS1(x15) + x10 + SS0(x2) + x1;
	RND(h, a, b, c, d, e, f, g, 0x1e376c08u, x1);
	x2 = SS1(x0) + x11 + SS0(x3) + x2;
	RND(g, h, a, b, c, d, e, f, 0x2748774cu, x2);
	x3 = SS1(x1) + x12 + SS0(x4) + x3;
	RND(f, g, h, a, b, c, d, e, 0x34b0bcb5u, x3);
	x4 = SS1(x2) + x13 + SS0(x5) + x4;
	RND(e, f, g, h, a, b, c, d, 0x391c0cb3u, x4);
	x5 = SS1(x3) + x14 + SS0(x6) + x5;
	RND(d, e, f, g, h, a, b, c, 0x4ed8aa4au, x5);
	x6 = SS1(x4) + x15 + SS0(x7) + x6;
	RND(c, d, e, f, g, h, a, b, 0x5b9cca4fu, x6);
	x7 = SS1(x5) + x0 + SS0(x8) + x7;
	RND(b, c, d, e, f, g, h, a, 0x682e6ff3u, x7);
	x8 = SS1(x6) + x1 + SS0(x9) + x8;
	RND(a, b, c, d, e, f, g, h, 0x748f82eeu, x8);
	x9 = SS1(x7) + x2 + SS0(x10) + x9;
	RND(h, a, b, c, d, e, f, g, 0x78a5636fu, x9);
	x10 = SS1(x8) + x3 + SS0(x11) + x10;
	RND(g, h, a, b, c, d, e, f, 0x84c87814u, x10);
	x11 = SS1(x9) + x4 + SS0(x12) + x11;
	RND(f, g, h, a, b, c, d, e, 0x8cc70208u, x11);
	x12 = SS1(x10) + x5 + SS0(x13) + x12;
	RND(e, f, g, h, a, b, c, d, 0x90befffau, x12);
	x13 = SS1(x11) + x6 + SS0(x14) + x13;
	RND(d, e, f, g, h, a, b, c, 0xa4506cebu, x13);
	x14 = SS1(x12) + x7 + SS0(x15) + x14;
	RND(c, d, e, f, g, h, a, b, 0xbef9a3f7u, x14);
	x15 = SS1(x13) + x8 + SS0(x0) + x15;
	RND(b, c, d, e, f, g, h, a, 0xc67178f2u, x15);
	hs0 = IV0 + a;
	hs1 = IV1 + b;
	hs2 = IV2 + c;
	hs3 = IV3 + d;
	hs4 = IV4 + e;
	hs5 = IV5 + f;
	hs6 = IV6 + g;
	hs7 = IV7 + h;

	// Büyük-endian 256 bitlik karşılaştırma: özet <= hedef.
	if ((hs0 < tgt[0] || (hs0 == tgt[0] && (hs1 < tgt[1] || (hs1 == tgt[1] && (hs2 < tgt[2] || (hs2 == tgt[2] && (hs3 < tgt[3] || (hs3 == tgt[3] && (hs4 < tgt[4] || (hs4 == tgt[4] && (hs5 < tgt[5] || (hs5 == tgt[5] && (hs6 < tgt[6] || (hs6 == tgt[6] && (hs7 <= tgt[7]))))))))))))))) && atomic_cmpxchg(&out[0], 0u, 1u) == 0u) {
		out[1] = hi;
		out[2] = lo;
		out[3] = hs0;
		out[4] = hs1;
		out[5] = hs2;
		out[6] = hs3;
		out[7] = hs4;
		out[8] = hs5;
		out[9] = hs6;
		out[10] = hs7;
	}
}
