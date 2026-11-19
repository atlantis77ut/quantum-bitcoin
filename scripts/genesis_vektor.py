"""Q-BTC genesis vektörünü Go kodunu çağırmadan hesaplar ve denetler.

Kullanım (depo kökünden):
  python3 -I scripts/genesis_vektor.py denetle core/testdata/*_vector.json
  python3 -I scripts/genesis_vektor.py uret GIRDI.json [-o VEKTOR.json]

Dosya bir JSON nesnesidir. Girdi alanları:
  network          "mainnet", "testnet" ya da "regtest"
  timestamp        genesis zaman damgası T_g (Unix saniyesi)
  message          genesis mesajı (yazdırılabilir ASCII)
  bits             genesis hedefi (ağın başlangıç hedefi): compact kodlamanın 8 küçük harf hex
                   karakteri
  pow_limit_bits   ağın zorluk tabanı (en kolay hedefi), aynı kodlamayla. Başlığa girmez; vektör
                   onu da sabitlesin diye girdidedir.
  nonce            genesis nonce'u
  bitcoin_headers  yalnızca mainnet: 80 baytlık iki Bitcoin başlığı (hex). İlki mesajda özeti
                   geçen blok, ikincisi onu onaylayan bloktur.
Türetilmiş alanlar: receiver, coinbase_txid, merkle_root, hash, sig_domain, kat_txid,
kat_signing_hash; mainnet'te ayrıca bitcoin_hash, bitcoin_time (t_b) ve
bitcoin_confirmation_hash.

denetle  Her dosyanın girdi alanlarından türetilmiş alanları yeniden hesaplar. Dosya eksiksiz
         ve betiğin üreteceği baytlarla aynı olmalıdır (satır sonu CRLF olabilir). Saate bakmaz.
uret     Girdiden vektörü üretir. Girdide bulunan türetilmiş alanlar (ör. genesis kazım aracının
         yazdığı "hash") hesaplananla aynı olmalıdır. T_g bu makinenin saatinden 300 saniyeden
         fazla ilerideyse durur. -o verilmezse vektör standart çıktıya yazılır. Verilirse dosya
         yoksa yazılır, varsa içeriği aynı olmalıdır: var olan dosyanın üzerine yazılmaz.

Denetlenen şartlar: iki hedefin kodlaması kanoniktir; başlangıç hedefi tabandan, taban 2^252 - 1'den
kolay değildir; genesis özeti hedefi karşılar. Mainnet'te ayrıca: taban tam olarak
min(başlangıç hedefi · 2^16, 0x1d00ffff)'tir; iki Bitcoin başlığı da kendi iş ispatını taşır ve
hedefleri gerçek Bitcoin zorluğu ölçeğindedir (BITCOIN_MAX_BITS); ikinci başlık ilkine bağlanır;
mesaj tam olarak "Q-BTC mainnet genesis | BTC <ilk başlığın özeti>" biçimindedir; T_g >= t_b.
Başlangıç hedefinin ölçülen hashrate'e karşılık geldiğini betik denetleyemez.

Çıkış kodu: 0 tamam; 1 fark ya da geçersiz girdi; 2 kullanım hatası.

Yalnızca standart kitaplık kullanır (Python 3.8 ya da sonrası). Kodlamalar core/ paketinden ayrı
olarak burada yeniden yazılmıştır: başlık core/header.go, işlem kimliği core/transaction.go,
Merkle kökü core/merkle.go, hedef core/target.go belgelerindeki biçimdedir.
"""

import argparse
import collections
import datetime
import hashlib
import json
import re
import struct
import sys
import time

# Uzlaşma sabitlerinin buradaki kopyaları (core/transaction.go, core/params.go, core/block.go,
# core/header.go, core/target.go). Go tarafında biri değişirse core/testdata altındaki vektörler
# Go testinde tutmaz; bu betik de birlikte güncellenmelidir.
TXID_TAG = b"QBTC-TXID-v3"
SIG_TAG = "QBTC-SIG-v5"
ADDRESS_PREFIX = "QBTC"
COINBASE_SENDER = "SYSTEM_REWARD"
COMMITMENT_SIZE = 32
BLOCK_VERSION = 1
POW_LIMIT = (1 << 252) - 1
MAX_FUTURE_DRIFT = 300

# Mainnet genesis mesajı: bu önek ve ardından mesajdaki Bitcoin bloğunun özeti (gezgin
# sitelerindeki gösterimle, 64 küçük harf hex).
MAINNET_MESSAGE_PREFIX = "Q-BTC mainnet genesis | BTC "
# Mainnet tabanı bundan kolay olamaz (core/params.go mainnetMaxPowLimitBits) ve hedef cinsinden
# tam olarak min(başlangıç hedefi · 2^16, bu tavan) olmalıdır (lansman kontrol listesi, madde 6a).
MAINNET_MAX_BITS = 0x1D00FFFF
MAINNET_FLOOR_SHIFT = 16
# Kabul edilen en kolay Bitcoin hedefi: 976600. bloğun (2026-11-19, nBits 0x17021673, zorluk
# yaklaşık 1,35e14) hedefinin 16 katı. Pay geniştir: Bitcoin zorluğu iki haftada bir ayarlanır ve
# aşağı yönlü ayarlamalar gerçek başlıkları reddettirmemelidir. Tam provada yenilenir.
BITCOIN_MAX_BITS = 0x17216730

# Bilinen-yanıt işlemi (core/params_test.go katTx): imzası geçerli olmak zorunda değildir, yalnızca
# kodlamayı sabitler. kat_txid ağdan bağımsızdır; kat_signing_hash ağın imza alanına bağlıdır.
KAT_SENDER = ADDRESS_PREFIX + "1" * 40
KAT_RECEIVER = ADDRESS_PREFIX + "2" * 40
KAT_AMOUNT, KAT_FEE, KAT_EXPIRY_HEIGHT, KAT_NONCE = 123456789, 1000, 500000, 7
KAT_PUBLIC_KEY = bytes(range(32))
KAT_SIGNATURE = b"\xab" * 16

INPUT_KEYS = ("network", "timestamp", "message", "bits", "pow_limit_bits", "nonce", "bitcoin_headers")
DERIVED_KEYS = ("receiver", "coinbase_txid", "merkle_root", "hash", "sig_domain", "kat_txid",
                "kat_signing_hash", "bitcoin_hash", "bitcoin_time", "bitcoin_confirmation_hash")
MAX_FILE_SIZE = 1 << 16

# max_target: ağın tabanı bundan kolay olamaz. floor_shift: None değilse taban tam olarak
# min(başlangıç hedefi << floor_shift, max_target) olmalıdır. bitcoin_max_target: None değilse mesaj
# bir Bitcoin bloğuna bağlanır ve o bloğun başlıkları en çok bu kadar kolay bir hedef taşıyabilir.
Rules = collections.namedtuple("Rules", "max_target floor_shift bitcoin_max_target")
BitcoinHeader = collections.namedtuple("BitcoinHeader", "hash prev time")


class VectorError(Exception):
    """Girdi geçersiz ya da hesaplanan değer dosyadakiyle aynı değil."""


def sha256(data):
    return hashlib.sha256(data).digest()


def compact_to_target(bits):
    """Compact hedefi (üs 8 bit | işaret 1 bit | mantis 23 bit) tamsayıya çevirir. İşaretli, sıfır
    ya da 256 biti aşan değerde None döner."""
    exponent, mantissa = bits >> 24, bits & 0x007FFFFF
    if bits & 0x00800000 or mantissa == 0:
        return None
    if exponent <= 3:
        target = mantissa >> (8 * (3 - exponent))
    else:
        target = mantissa << (8 * (exponent - 3))
    if target == 0 or target.bit_length() > 256:
        return None
    return target


def target_to_compact(target):
    """Pozitif hedefin kanonik compact kodlaması (alt baytlar kesilir)."""
    size = (target.bit_length() + 7) // 8
    if size <= 3:
        mantissa = target << (8 * (3 - size))
    else:
        mantissa = target >> (8 * (size - 3))
    if mantissa & 0x00800000:  # işaret biti kullanılamaz: mantis bir bayt kayar
        mantissa >>= 8
        size += 1
    return size << 24 | mantissa


def decode_bits(bits, label):
    """Kanonik compact hedefi çözer. Q-BTC ve Bitcoin başlıkları aynı kodlamayı kullanır."""
    target = compact_to_target(bits)
    if target is None or target_to_compact(target) != bits:
        raise VectorError("%s: hedef kodlaması %08x geçersiz ya da kanonik değil" % (label, bits))
    return target


NETWORKS = {
    "mainnet": Rules(compact_to_target(MAINNET_MAX_BITS), MAINNET_FLOOR_SHIFT, compact_to_target(BITCOIN_MAX_BITS)),
    "testnet": Rules(POW_LIMIT, None, None),
    "regtest": Rules(POW_LIMIT, None, None),
}


def field(data):
    """Uzunluk önekli alan: 4 baytlık büyük-endian uzunluk ve ardından veri."""
    return struct.pack(">I", len(data)) + data


def u64(value):
    return struct.pack(">Q", value)


def tx_body(sender, receiver, amount, fee, expiry_height, nonce, coinbase, commitment):
    """İşlem gövdesi: sekiz uzunluk önekli alan, tamsayılar 8 baytlık büyük-endian."""
    parts = (sender.encode("ascii"), receiver.encode("ascii"), u64(amount), u64(fee),
             u64(expiry_height), u64(nonce), b"\x01" if coinbase else b"\x00", commitment)
    return b"".join(field(part) for part in parts)


def tx_id(body, public_key, signature):
    """İşlem kimliği: SHA256(field(etiket) ‖ SHA256(gövde) ‖ SHA256(tanık))."""
    witness = field(public_key) + field(signature)
    return sha256(field(TXID_TAG) + sha256(body) + sha256(witness))


def genesis(message, timestamp, bits, nonce):
    """Genesis bloğunun alıcısını, coinbase kimliğini, Merkle kökünü ve özetini hesaplar."""
    receiver = ADDRESS_PREFIX + sha256(message.encode("ascii"))[:20].hex()
    # Premine yok: tek işlem sıfır tutarlı coinbase'dir; nonce'u yükseklik (0), taahhüt yuvası
    # 32 sıfır bayt, açık anahtarı ve imzası boştur.
    body = tx_body(COINBASE_SENDER, receiver, 0, 0, 0, 0, True, bytes(COMMITMENT_SIZE))
    txid = tx_id(body, b"", b"")
    # Tek yapraklı ağaçta kök yaprağın kendisidir: SHA256(0x00 ‖ kimlik).
    merkle_root = sha256(b"\x00" + txid)
    # 96 baytlık başlık (büyük-endian): nonce, sürüm, yükseklik, zaman, hedef, önceki özet, kök.
    header = struct.pack(">QIQQI", nonce, BLOCK_VERSION, 0, timestamp, bits) + bytes(32) + merkle_root
    return receiver, txid, merkle_root, sha256(sha256(header))


def kat(sig_domain):
    """Bilinen-yanıt işleminin kimliği ve verilen imza alanında imzalanan mesajın özeti."""
    body = tx_body(KAT_SENDER, KAT_RECEIVER, KAT_AMOUNT, KAT_FEE, KAT_EXPIRY_HEIGHT, KAT_NONCE, False, b"")
    return tx_id(body, KAT_PUBLIC_KEY, KAT_SIGNATURE), sha256(field(sig_domain.encode("ascii")) + body)


def bitcoin_header(text, max_target, label):
    """80 baytlık Bitcoin başlığını doğrular: iş ispatı kendi hedefini karşılamalı ve hedef
    max_target'tan kolay olmamalıdır. Özetler gezgin sitelerindeki gösterimle (bayt sırası ters)
    döner."""
    if type(text) is not str or not re.fullmatch("[0-9a-f]{160}", text):
        raise VectorError("%s: 160 küçük harf hex karakter olmalı" % label)
    raw = bytes.fromhex(text)
    # Düzen (küçük-endian): sürüm 4, önceki özet 32, Merkle kökü 32, zaman 4, hedef 4, nonce 4.
    block_time, bits = struct.unpack("<II", raw[68:76])
    target = decode_bits(bits, label)
    digest = sha256(sha256(raw))
    if int.from_bytes(digest, "little") > target:
        raise VectorError("%s: iş ispatı kendi hedefini (%08x) karşılamıyor" % (label, bits))
    if target > max_target:
        raise VectorError("%s: hedef %08x gerçek Bitcoin zorluğu ölçeğinde değil (en kolay %08x)"
                          % (label, bits, target_to_compact(max_target)))
    return BitcoinHeader(digest[::-1].hex(), raw[4:36][::-1].hex(), block_time)


def require_int(doc, key, low, high):
    value = doc.get(key)
    if type(value) is not int or not low <= value <= high:  # bool bir int alt türüdür, kabul edilmez
        raise VectorError("%s: %d ile %d arasında bir tamsayı olmalı" % (key, low, high))
    return value


def require_bits(doc, key):
    """Compact hedef alanını çözer; (metin, sayı, hedef) döner."""
    text = doc.get(key)
    if type(text) is not str or not re.fullmatch("[0-9a-f]{8}", text):
        raise VectorError("%s: 8 küçük harf hex karakter olmalı" % key)
    bits = int(text, 16)
    return text, bits, decode_bits(bits, key)


def build_vector(doc, networks=NETWORKS):
    """Girdi alanlarından vektörü hesaplar ve genesis şartlarını denetler. Dönen sözlüğün anahtar
    sırası dosyadaki sıradır."""
    network = doc.get("network")
    if type(network) is not str or network not in networks:
        raise VectorError("network: %s değerlerinden biri olmalı" % ", ".join(sorted(networks)))
    rules = networks[network]
    timestamp = require_int(doc, "timestamp", 1, (1 << 63) - 1)
    message = doc.get("message")
    if type(message) is not str or not re.fullmatch("[ -~]{1,256}", message):
        raise VectorError("message: 1-256 yazdırılabilir ASCII karakter olmalı")
    bits_text, bits, target = require_bits(doc, "bits")
    limit_text, limit_bits, limit = require_bits(doc, "pow_limit_bits")
    # core/params.go Validate ile aynı şartlar: taban sınırların içinde, başlangıç hedefi tabandan
    # zor ya da ona eşit. Mainnet taban kuralı Validate'te yoktur; burada ek şarttır.
    if limit > POW_LIMIT:
        raise VectorError("pow_limit_bits: taban %08x izin verilen en kolay hedeften (2^252 - 1) kolay"
                          % limit_bits)
    if limit > rules.max_target:
        raise VectorError("pow_limit_bits: taban %08x %s için izin verilen en kolay hedeften (%08x) kolay"
                          % (limit_bits, network, target_to_compact(rules.max_target)))
    if target > limit:
        raise VectorError("bits: başlangıç hedefi %08x tabandan (%08x) kolay" % (bits, limit_bits))
    if rules.floor_shift is not None:
        # Başlangıç hedefi kanonik compact bir değer olduğundan 2^16 katı da tam olarak kodlanır.
        expected = min(target << rules.floor_shift, rules.max_target)
        if limit != expected:
            raise VectorError("pow_limit_bits: taban %08x; %s tabanı min(başlangıç hedefi · 2^%d, %08x) = %08x "
                              "olmalı" % (limit_bits, network, rules.floor_shift,
                                          target_to_compact(rules.max_target), target_to_compact(expected)))
    nonce = require_int(doc, "nonce", 0, (1 << 64) - 1)

    vector = {"network": network, "timestamp": timestamp, "message": message, "bits": bits_text,
              "pow_limit_bits": limit_text, "nonce": nonce}
    anchor = {}
    if rules.bitcoin_max_target is None:
        if "bitcoin_headers" in doc:
            raise VectorError("bitcoin_headers: %s genesis'i bir Bitcoin bloğuna bağlanmaz" % network)
    else:
        headers = doc.get("bitcoin_headers")
        if type(headers) is not list or len(headers) != 2:
            raise VectorError("bitcoin_headers: tam iki başlık olmalı (mesajdaki blok ve onu onaylayan blok)")
        block = bitcoin_header(headers[0], rules.bitcoin_max_target, "mesajdaki Bitcoin başlığı")
        confirmation = bitcoin_header(headers[1], rules.bitcoin_max_target, "onaylayan Bitcoin başlığı")
        if confirmation.prev != block.hash:
            raise VectorError("onaylayan Bitcoin başlığı mesajdaki bloğa bağlanmıyor")
        if message != MAINNET_MESSAGE_PREFIX + block.hash:
            raise VectorError('message: "%s%s" olmalı (mesajdaki Bitcoin başlığının özeti)'
                              % (MAINNET_MESSAGE_PREFIX, block.hash))
        if timestamp < block.time:
            raise VectorError("timestamp: T_g (%d) mesajdaki Bitcoin bloğunun zamanından (t_b = %d) küçük"
                              % (timestamp, block.time))
        vector["bitcoin_headers"] = list(headers)
        anchor = {"bitcoin_hash": block.hash, "bitcoin_time": block.time,
                  "bitcoin_confirmation_hash": confirmation.hash}

    receiver, txid, merkle_root, digest = genesis(message, timestamp, bits, nonce)
    if int.from_bytes(digest, "big") > target:
        raise VectorError("genesis özeti %s hedefi (%08x) karşılamıyor" % (digest.hex(), bits))
    sig_domain = "%s|%s|%s" % (SIG_TAG, network, digest.hex())
    kat_txid, kat_signing_hash = kat(sig_domain)
    vector.update({"receiver": receiver, "coinbase_txid": txid.hex(), "merkle_root": merkle_root.hex(),
                   "hash": digest.hex(), "sig_domain": sig_domain, "kat_txid": kat_txid.hex(),
                   "kat_signing_hash": kat_signing_hash.hex()})
    vector.update(anchor)
    return vector


def parse(text):
    """Dosya metnini katı biçimde çözer: tek bir nesne, yalnızca bilinen anahtarlar, yinelenen
    anahtar yok, kesirli ya da özel sayı yok."""
    def pairs(items):
        keys = [key for key, _ in items]
        if len(set(keys)) != len(keys):
            raise VectorError("yinelenen anahtar")
        return dict(items)

    def reject(token):
        raise VectorError("tamsayı olmayan sayı: %s" % token)

    try:
        doc = json.loads(text, object_pairs_hook=pairs, parse_float=reject, parse_constant=reject)
    except (ValueError, RecursionError) as e:
        raise VectorError("JSON çözülemedi: %s" % e)
    if type(doc) is not dict:
        raise VectorError("dosya bir JSON nesnesi olmalı")
    unknown = sorted(set(doc) - set(INPUT_KEYS) - set(DERIVED_KEYS))
    if unknown:
        raise VectorError("bilinmeyen alan: %s" % ", ".join(unknown))
    return doc


def compare(doc, vector):
    """Dosyada bulunan her alan hesaplanan vektördekiyle aynı türde ve aynı değerde olmalıdır."""
    for key, value in doc.items():
        if key not in vector:
            raise VectorError("%s: %s vektöründe bu alan olmaz" % (key, vector["network"]))
        if type(value) is not type(vector[key]) or value != vector[key]:
            raise VectorError("%s: dosyada %s, hesaplanan %s"
                              % (key, json.dumps(value), json.dumps(vector[key])))


def serialize(vector):
    return json.dumps(vector, indent=2, ensure_ascii=True) + "\n"


def read_text(path):
    with open(path, "rb") as f:
        raw = f.read(MAX_FILE_SIZE + 1)
    if len(raw) > MAX_FILE_SIZE:
        raise VectorError("dosya %d bayttan büyük" % MAX_FILE_SIZE)
    try:
        return raw.decode("utf-8")
    except UnicodeDecodeError as e:
        raise VectorError("dosya UTF-8 değil: %s" % e)


def check(text, networks=NETWORKS):
    """denetle: metin eksiksiz bir vektör olmalı ve betiğin üreteceği baytlarla aynı olmalıdır."""
    doc = parse(text)
    vector = build_vector(doc, networks)
    missing = [key for key in vector if key not in doc]
    if missing:
        raise VectorError("eksik alan: %s" % ", ".join(missing))
    compare(doc, vector)
    if text.replace("\r\n", "\n") != serialize(vector):
        raise VectorError("dosya betiğin üreteceği biçimde değil (alan sırası, girinti ya da satır sonu)")
    return vector


def produce(text, now, networks=NETWORKS):
    """uret: girdi metninden vektörü ve dosyaya yazılacak metni üretir."""
    doc = parse(text)
    vector = build_vector(doc, networks)
    compare(doc, vector)
    # 1. bloğun damgası T_g'den büyük olmalıdır ve düğümlerin saatini en çok 300 sn (FTL)
    # geçebilir: T_g bundan ilerideyse zincir o süre boyunca ilerleyemez. Düğüm böyle bir genesis'le
    # yalnızca uyarı vererek açılır. Olağan neden genesis'i üreten makinenin saatidir.
    if vector["timestamp"] > now + MAX_FUTURE_DRIFT:
        raise VectorError("timestamp: T_g bu makinenin saatinden %d sn ileride (en çok %d); genesis'i "
                          "üreten makinenin ve bu makinenin saatini denetleyin"
                          % (vector["timestamp"] - now, MAX_FUTURE_DRIFT))
    return vector, serialize(vector)


def write_once(path, text):
    """Dosya yoksa yazar; varsa içeriği aynı olmalıdır. Var olan dosyanın üzerine yazılmaz."""
    try:
        with open(path, "x", encoding="ascii", newline="\n") as f:  # "x": dosya varsa açılmaz
            f.write(text)
    except FileExistsError:
        if read_text(path).replace("\r\n", "\n") != text:
            raise VectorError("%s var ve içeriği üretilen vektörden farklı; üzerine yazılmadı" % path)


def iso(timestamp):
    try:
        moment = datetime.datetime.fromtimestamp(timestamp, datetime.timezone.utc)
    except (OverflowError, OSError, ValueError):
        return "takvim dışı"
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def describe(vector, now=None):
    """Vektörün insan için özeti; now verilirse T_g'nin bu makinenin saatine uzaklığını da yazar."""
    lines = [
        "%s genesis %s" % (vector["network"], vector["hash"]),
        "  T_g %d (%s), nonce %d" % (vector["timestamp"], iso(vector["timestamp"]), vector["nonce"]),
        "  başlangıç hedefi %s, taban %s" % (vector["bits"], vector["pow_limit_bits"]),
        "  alıcı %s" % vector["receiver"],
        "  coinbase kimliği %s" % vector["coinbase_txid"],
        "  Merkle kökü %s" % vector["merkle_root"],
        "  imza alanı %s" % vector["sig_domain"],
    ]
    if "bitcoin_hash" in vector:
        lines += [
            "  Bitcoin bloğu %s" % vector["bitcoin_hash"],
            "  t_b %d (%s), T_g - t_b = %d sn" % (vector["bitcoin_time"], iso(vector["bitcoin_time"]),
                                                   vector["timestamp"] - vector["bitcoin_time"]),
            "  onaylayan Bitcoin bloğu %s" % vector["bitcoin_confirmation_hash"],
        ]
    if now is not None:
        delta = now - vector["timestamp"]
        lines.append("  T_g bu makinenin saatine göre %d sn %s" % (abs(delta), "önce" if delta >= 0 else "ileride"))
    return "\n".join(lines)


def main(argv=None):
    for stream in (sys.stdout, sys.stderr):
        # newline: standart çıktıya yazılan vektör her platformda dosyadaki baytlarla aynı olsun.
        stream.reconfigure(encoding="utf-8", errors="replace", newline="\n")
    parser = argparse.ArgumentParser(
        description="Q-BTC genesis vektörünü Go kodunu çağırmadan hesaplar ve denetler.")
    commands = parser.add_subparsers(dest="command", required=True)
    check_parser = commands.add_parser("denetle", help="vektör dosyalarını baştan hesaplayıp karşılaştırır")
    check_parser.add_argument("files", nargs="+", metavar="VEKTOR")
    produce_parser = commands.add_parser("uret", help="girdi dosyasından vektörü üretir")
    produce_parser.add_argument("input", metavar="GIRDI")
    produce_parser.add_argument("-o", dest="output", metavar="VEKTOR",
                                help="vektörün yazılacağı dosya; varsa içeriği aynı olmalıdır")
    args = parser.parse_args(argv)

    failed = False
    if args.command == "denetle":
        for path in args.files:
            try:
                vector = check(read_text(path))
            except (VectorError, OSError) as e:
                print("HATA: %s: %s" % (path, e), file=sys.stderr)
                failed = True
                continue
            print("tamam: %s" % path)
            print(describe(vector))
    else:
        now = int(time.time())
        try:
            vector, text = produce(read_text(args.input), now)
            if args.output is not None:
                write_once(args.output, text)
        except (VectorError, OSError) as e:
            print("HATA: %s: %s" % (args.input, e), file=sys.stderr)
            return 1
        if args.output is None:
            sys.stdout.write(text)
            print(describe(vector, now), file=sys.stderr)
        else:
            print("tamam: %s" % args.output)
            print(describe(vector, now))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
