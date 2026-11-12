# Açılmış SHA256d çekirdeğini (gpu/sha256d.cl) üretir ve aynı işlem dizisini Python'da
# hashlib'e karşı doğrular. Kullanım: python3 gpu/gen/genkernel.py gpu/sha256d.cl
# Döngülü karşılaştırma çekirdeği gpu/testdata/sha256d_loop.cl'dedir (TestKernelAB).
import hashlib, os, struct, sys

K = [
0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]
IV = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19]
V = list("abcdefgh")

C, P = [], []  # C satırları, Python satırları

def emit(c, p):
    C.append("\t" + c)
    P.append(p)

def rounds(wexpr, expand):
    """64 tur; wexpr(i) i'inci tur sözcüğünün ifadesi. expand True ise x0..x15 kayan takvimi."""
    for i in range(64):
        if expand and i >= 16:
            j = i % 16
            x = lambda n: "x%d" % (n % 16)
            emit("%s = SS1(%s) + %s + SS0(%s) + %s;" % (x(j), x(i - 2), x(i - 7), x(i - 15), x(i - 16)),
                 "%s = (SS1(%s) + %s + SS0(%s) + %s) & M" % (x(j), x(i - 2), x(i - 7), x(i - 15), x(i - 16)))
        r = [V[(k - i) % 8] for k in range(8)]  # bu turun a..h adları
        a, b, c, d, e, f, g, h = r
        w = wexpr(i)
        emit("RND(%s, %s, %s, %s, %s, %s, %s, %s, 0x%08xu, %s);" % (a, b, c, d, e, f, g, h, K[i], w),
             "t = (%s + BS1(%s) + CH(%s, %s, %s) + 0x%08x + %s) & M; %s = (%s + t) & M; %s = (t + BS0(%s) + MAJ(%s, %s, %s)) & M"
             % (h, e, e, f, g, K[i], w, d, d, h, a, a, b, c))

def load_state(src):
    for k, v in enumerate(V):
        emit("%s = %s;" % (v, src[k]), "%s = %s" % (v, src[k]))

def add_state(dst, base):
    for k, v in enumerate(V):
        emit("%s = %s + %s;" % (dst[k], base[k], v), "%s = (%s + %s) & M" % (dst[k], base[k], v))

IVN = ["IV%d" % k for k in range(8)]
S = ["s%d" % k for k in range(8)]
H = ["hs%d" % k for k in range(8)]

# 1. blok: x0 = hi, x1 = lo, x2..x15 sabitler (konaktan).
emit("x0 = hi;", "x0 = hi")
emit("x1 = lo;", "x1 = lo")
for k in range(2, 16):
    emit("x%d = w1[%d];" % (k, k), "x%d = w1[%d]" % (k, k))
load_state(IVN)
rounds(lambda i: "x%d" % (i % 16), True)
add_state(S, IVN)
# 2. blok: konakta genişletilmiş takvim.
load_state(S)
rounds(lambda i: "w2[%d]" % i, False)
add_state(S, S)
# İkinci SHA-256: özet + 0x80 + uzunluk 256 bit; sabitler derleyicide katlanır.
for k in range(8):
    emit("x%d = s%d;" % (k, k), "x%d = s%d" % (k, k))
emit("x8 = 0x80000000u;", "x8 = 0x80000000")
for k in range(9, 15):
    emit("x%d = 0u;" % k, "x%d = 0" % k)
emit("x15 = 256u;", "x15 = 256")
load_state(IVN)
rounds(lambda i: "x%d" % (i % 16), True)
add_state(H, IVN)

# Python doğrulaması
M = 0xffffffff
def ROTR(x, n): return ((x >> n) | (x << (32 - n))) & M
def BS0(x): return ROTR(x, 2) ^ ROTR(x, 13) ^ ROTR(x, 22)
def BS1(x): return ROTR(x, 6) ^ ROTR(x, 11) ^ ROTR(x, 25)
def SS0(x): return ROTR(x, 7) ^ ROTR(x, 18) ^ (x >> 3)
def SS1(x): return ROTR(x, 17) ^ ROTR(x, 19) ^ (x >> 10)
def CH(x, y, z): return (x & y) ^ (~x & z & M)
def MAJ(x, y, z): return (x & y) ^ (x & z) ^ (y & z)
code = compile("\n".join(P), "kernel", "exec")

def schedule2(hdr):
    w = [struct.unpack(">I", hdr[64 + 4 * i:68 + 4 * i])[0] for i in range(8)] + [0x80000000, 0, 0, 0, 0, 0, 0, 768]
    for i in range(16, 64):
        w.append((SS1(w[i - 2]) + w[i - 7] + SS0(w[i - 15]) + w[i - 16]) & M)
    return w

for trial in range(200):
    hdr = bytearray(os.urandom(96))
    nonce = int.from_bytes(os.urandom(8), "big")
    hdr[0:8] = nonce.to_bytes(8, "big")
    env = dict(globals())
    env.update(hi=nonce >> 32, lo=nonce & M, w1=[struct.unpack(">I", hdr[4 * i:4 * i + 4])[0] for i in range(16)],
               w2=schedule2(bytes(hdr)), **{"IV%d" % k: IV[k] for k in range(8)})
    exec(code, env)
    got = b"".join(env["hs%d" % k].to_bytes(4, "big") for k in range(8))
    want = hashlib.sha256(hashlib.sha256(bytes(hdr)).digest()).digest()
    assert got == want, (trial, got.hex(), want.hex())
print("python doğrulaması: 200 başlık eşleşti", file=sys.stderr)

# Karşılaştırma zinciri: özet <= hedef (büyük-endian).
def cmp(k):
    if k == 7:
        return "hs7 <= tgt[7]"
    return "hs%d < tgt[%d] || (hs%d == tgt[%d] && (%s))" % (k, k, k, k, cmp(k + 1))

HEAD = """// Q-BTC başlığı için SHA256d tarama çekirdeği (OpenCL 1.2).
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
// Üreteç aynı işlem dizisini Python'da standart SHA-256'ya karşı doğrular. Döngülü sürüm
// (gpu/testdata/sha256d_loop.cl) ile aynı süreçteki A/B ölçümü: TestKernelAB.
"""
body = """
#define ROTR(x, n) rotate((uint)(x), (uint)(32u - (n)))
#define CH(x, y, z) bitselect((z), (y), (x))
#define MAJ(x, y, z) bitselect((x), (y), ((z) ^ (x)))
#define BS0(x) (ROTR((x), 2) ^ ROTR((x), 13) ^ ROTR((x), 22))
#define BS1(x) (ROTR((x), 6) ^ ROTR((x), 11) ^ ROTR((x), 25))
#define SS0(x) (ROTR((x), 7) ^ ROTR((x), 18) ^ ((x) >> 3))
#define SS1(x) (ROTR((x), 17) ^ ROTR((x), 19) ^ ((x) >> 10))

// RND bir SHA-256 turudur; değişkenler kaydırılmaz, adları her turda döner.
#define RND(a, b, c, d, e, f, g, h, k, w)                              \\
	{                                                                  \\
		uint t1_ = (h) + BS1(e) + CH((e), (f), (g)) + (k) + (w);       \\
		(d) += t1_;                                                    \\
		(h) = t1_ + BS0(a) + MAJ((a), (b), (c));                       \\
	}

""" + "".join("#define IV%d 0x%08xu\n" % (k, IV[k]) for k in range(8)) + """
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

""" + "\n".join(C) + """

	// Büyük-endian 256 bitlik karşılaştırma: özet <= hedef.
	if ((""" + cmp(0) + """) && atomic_cmpxchg(&out[0], 0u, 1u) == 0u) {
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
"""
open(sys.argv[1], "w", encoding="utf-8", newline="\n").write(HEAD + body)
print("yazıldı:", sys.argv[1], len(C), "satır", file=sys.stderr)
