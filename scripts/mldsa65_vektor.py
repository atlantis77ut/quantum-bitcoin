"""ML-DSA-65 bilinen-yanıt vektörünü (crypto/selftest_vector.go) liboqs'tan bağımsız olarak
OpenSSL ile yeniden üretir ve depodakiyle karşılaştırır.

Kullanım: python3 -I scripts/mldsa65_vektor.py [OPENSSL]   (depo kökünden; OPENSSL varsayılan "openssl")

Çıkış kodu:
  0  açık anahtar ve imza depodakiyle aynı; OpenSSL geçerli imzayı doğruladı, bozulmuş altı
     üçlüyü reddetti
  1  fark ya da beklenmeyen sonuç (vektör üretildikten sonraki her belirsizlik dahil)
  3  denetim yapılamadı: bu OpenSSL ML-DSA-65'i desteklemiyor (3.5 öncesi) ya da vektörü
     üretmek için gereken seçenekleri tanımıyor

Yalnızca standart kitaplık kullanır. Buradaki tohum herkesçe bilinen bir sınama değeridir
(RFC 9881 Ek C örnek anahtarı); gerçek anahtar üretmek için kullanılmaz.
"""

import hashlib
import pathlib
import re
import subprocess
import sys
import tempfile

SEED = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
PK_SIZE, SIG_SIZE = 1952, 3309
# ML-DSA-65 SubjectPublicKeyInfo öneki (RFC 9881): ardından 1952 baytlık ham açık anahtar gelir.
SPKI_PREFIX = bytes.fromhex("308207b2300b0609608648016503040312038207a100")
# İmza düzeni (FIPS 204 sigEncode): c̃ (48), z (5 × 640), ipucu bölümü (ω = 55 indis, k = 6 sayaç).
HINT_OFFSET, OMEGA, POLYS = 48 + 5 * 640, 55, 6
# Depodaki vektörün imzasındaki ipucu sayısı (crypto/selftest.go katHintCount).
HINT_COUNT = 41

ROOT = pathlib.Path(__file__).resolve().parent.parent
VECTOR = ROOT / "crypto" / "selftest_vector.go"


def stop(code, msg):
    print(("ATLANDI: " if code == 3 else "HATA: ") + msg, file=sys.stderr)
    sys.exit(code)


def go_vector():
    """Go kaynağındaki sabitleri okur: açık anahtar, mesaj, imza."""
    text = VECTOR.read_text(encoding="utf-8").replace("\r\n", "\n")

    def const_hex(name):
        m = re.search(r'const %s = "" \+\n((?:\t"[0-9a-f]+"(?: \+)?\n)+)' % name, text)
        if not m:
            stop(1, "%s içinde %s bulunamadı" % (VECTOR, name))
        return bytes.fromhex("".join(re.findall(r'"([0-9a-f]+)"', m.group(1))))

    m = re.search(r'const katMessage = "([^"\\]*)"', text)
    if not m:
        stop(1, "%s içinde katMessage bulunamadı" % VECTOR)
    return const_hex("katPublicKeyHex"), m.group(1).encode("ascii"), const_hex("katSignatureHex")


def run(openssl, args, stdin=None):
    return subprocess.run([openssl] + args, input=stdin, stdout=subprocess.PIPE, stderr=subprocess.PIPE)


def need(openssl, args, what):
    """Vektörü üretmek için gereken komut: başarısızsa denetim yapılamamıştır (çıkış 3)."""
    r = run(openssl, args)
    if r.returncode != 0:
        stop(3, "%s başarısız (çıkış %d): %s" % (what, r.returncode, r.stderr.decode("utf-8", "replace").strip()))
    return r.stdout


def main():
    for stream in (sys.stdout, sys.stderr):
        stream.reconfigure(encoding="utf-8", errors="replace")
    openssl = sys.argv[1] if len(sys.argv) > 1 else "openssl"
    pk_go, msg, sig_go = go_vector()
    if len(pk_go) != PK_SIZE or len(sig_go) != SIG_SIZE:
        stop(1, "depodaki vektörün boyutları beklenmedik: açık anahtar %d, imza %d" % (len(pk_go), len(sig_go)))

    try:
        version = run(openssl, ["version"]).stdout.decode("utf-8", "replace").strip()
        algs = run(openssl, ["list", "-signature-algorithms"]).stdout.decode("utf-8", "replace")
    except OSError as e:
        stop(3, "%s çalıştırılamadı: %s" % (openssl, e))
    if "ML-DSA-65" not in algs:
        stop(3, "%s ML-DSA-65'i desteklemiyor (OpenSSL 3.5 ya da sonrası gerekir)" % version)

    with tempfile.TemporaryDirectory() as tmp:
        d = pathlib.Path(tmp)
        key, msg_file = str(d / "sk.pem"), str(d / "msg.bin")
        (d / "msg.bin").write_bytes(msg)
        need(openssl, ["genpkey", "-algorithm", "ML-DSA-65", "-pkeyopt", "hexseed:" + SEED, "-out", key], "anahtar üretimi")
        spki = need(openssl, ["pkey", "-in", key, "-pubout", "-outform", "DER"], "açık anahtarın çıkarılması")
        sign = ["pkeyutl", "-sign", "-inkey", key, "-in", msg_file, "-pkeyopt", "deterministic:1"]
        sig = need(openssl, sign, "belirlenimci imzalama")
        if need(openssl, sign, "belirlenimci imzalama") != sig:
            stop(1, "belirlenimci imzalama iki çağrıda farklı imza üretti")

        if len(spki) != len(SPKI_PREFIX) + PK_SIZE or not spki.startswith(SPKI_PREFIX):
            stop(1, "OpenSSL'in açık anahtar kodlaması beklenmedik (%d bayt)" % len(spki))
        pk = spki[len(SPKI_PREFIX):]
        print("%s" % version)
        print("açık anahtar SHA-256: %s" % hashlib.sha256(pk).hexdigest())
        print("imza SHA-256:         %s" % hashlib.sha256(sig).hexdigest())
        if pk != pk_go:
            stop(1, "açık anahtar depodakinden farklı (depodaki SHA-256 %s)" % hashlib.sha256(pk_go).hexdigest())
        if sig != sig_go:
            stop(1, "imza depodakinden farklı (depodaki SHA-256 %s)" % hashlib.sha256(sig_go).hexdigest())

        def verifies(name, pk_bytes, message, signature):
            (d / "t_pk.der").write_bytes(SPKI_PREFIX + pk_bytes)
            (d / "t_msg.bin").write_bytes(message)
            (d / "t_sig.bin").write_bytes(signature)
            r = run(openssl, ["pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", str(d / "t_pk.der"),
                              "-in", str(d / "t_msg.bin"), "-sigfile", str(d / "t_sig.bin")])
            out = (r.stdout + r.stderr).decode("utf-8", "replace")
            if r.returncode == 0 and "Signature Verified Successfully" in out:
                return True
            if r.returncode != 0 and "Signature Verification Failure" in out:
                return False
            # Vektör üretilip karşılaştırıldıktan sonraki belirsizlik "atlandı" sayılmaz.
            stop(1, "%s: doğrulama komutunun çıktısı anlaşılamadı (çıkış %d): %s" % (name, r.returncode, out.strip()))

        if not verifies("geçerli vektör", pk, msg, sig):
            stop(1, "OpenSSL kendi ürettiği imzayı doğrulamadı")

        # crypto/selftest.go katInvalid ile aynı altı bozma; aynı sırayla ve aynı adlarla.
        def flip(b, i):
            c = bytearray(b)
            c[i] ^= 1
            return bytes(c)

        order = bytearray(sig)
        order[HINT_OFFSET], order[HINT_OFFSET + 1] = order[HINT_OFFSET + 1], order[HINT_OFFSET]
        repeat = bytearray(sig)
        repeat[HINT_OFFSET + 1:HINT_OFFSET + 1 + HINT_COUNT] = sig[HINT_OFFSET:HINT_OFFSET + HINT_COUNT]
        for i in range(HINT_OFFSET + OMEGA, HINT_OFFSET + OMEGA + POLYS):
            repeat[i] += 1
        padding = bytearray(sig)
        padding[HINT_OFFSET + OMEGA - 1] = 1
        if (sig[HINT_OFFSET] >= sig[HINT_OFFSET + 1] or sig[HINT_OFFSET + OMEGA + POLYS - 1] != HINT_COUNT
                or HINT_COUNT + 1 >= OMEGA or sig[HINT_OFFSET + OMEGA - 1] != 0):
            stop(1, "vektörün ipucu bölümü bozmaların önkoşullarını karşılamıyor")
        invalid = [
            ("mesajın bir biti değişti", pk, flip(msg, 0), sig),
            ("açık anahtarın bir biti değişti", flip(pk, len(pk) - 1), msg, sig),
            ("imzanın c̃ alanında bir bit değişti", pk, msg, flip(sig, 0)),
            ("ipucu indisleri artan sırada değil", pk, msg, bytes(order)),
            ("ipucu indisi iki kez yazılmış", pk, msg, bytes(repeat)),
            ("ipucu dolgusu sıfır değil", pk, msg, bytes(padding)),
        ]
        for name, p, m, s in invalid:
            if verifies(name, p, m, s):
                stop(1, "OpenSSL reddedilmesi gereken üçlüyü doğruladı: %s" % name)
            print("reddedildi: %s" % name)

    print("vektör OpenSSL çıktısıyla aynı: %s" % VECTOR.relative_to(ROOT).as_posix())


if __name__ == "__main__":
    main()
