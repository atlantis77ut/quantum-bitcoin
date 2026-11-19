"""scripts/genesis_vektor.py sınamaları.

Kullanım (depo kökünden): python3 -I scripts/genesis_vektor_test.py

Yalnızca standart kitaplık kullanır. Mainnet şartları, genesis hedefi gevşetilmiş bir kural
kümesiyle ve gerçek Bitcoin başlıklarıyla sınanır: gerçek mainnet hedefinde nonce aramak burada
yapılamayacak kadar uzun sürer ve depoya mainnet görünümlü geçerli bir genesis konmaz. Komut
satırı kuralları gevşetemez; bunu da buradaki bir sınama gösterir.
"""

import importlib.util
import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
SCRIPT = HERE / "genesis_vektor.py"

sys.dont_write_bytecode = True  # betiği yüklemek depoda scripts/__pycache__ bırakmasın
_spec = importlib.util.spec_from_file_location("genesis_vektor", str(SCRIPT))
gv = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(gv)

# Gerçek Bitcoin başlıkları (976600 ve 976601. bloklar; 0 ve 1. bloklar). Özetleri ve iş ispatları
# aşağıda yeniden hesaplanır: yanlış aktarılmış bir başlık bu denetimden geçemez.
BTC_976600 = ("00800e2013576923c649c2b677f84b358bdfb2761a914377515c01000000000000000000"
              "b39d9a5eab3b9edae48949c0e6b48d3a00f04e89972146d055219f711c47cde78918ff6a73160217df4de7ca")
BTC_976601 = ("0040002086eb092bfe0cf222993065c42ade49946ad9da30eafb01000000000000000000"
              "80590567edbccfab0ecc4939f86f5ff690425e8349d61ddf418abcb5a138d03de019ff6a7316021786be3bf6")
BTC_976600_HASH = "00000000000000000001fbea30dad96a9449de2ac465309922f20cfe2b09eb86"
BTC_976601_HASH = "00000000000000000001f07afd7e1e27a7d64535f3d62d4a98bf6156a7ad453d"
BTC_976600_TIME = 1795102857
BTC_0 = ("0100000000000000000000000000000000000000000000000000000000000000000000003ba3edfd7a7b12b27ac72c3e"
         "67768f617fc81bc3888a51323a9fb8aa4b1e5e4a29ab5f49ffff001d1dac2b7c")
BTC_1 = ("010000006fe28c0ab6f1b372c1a6a246ae63f74f931e8365e15a089c68d6190000000000982051fd1e4ba744bbbe680e"
         "1fee14677ba1a3c3540bf7b1cdb606e857233e0e61bc6649ffff001d01e36299")
BTC_0_HASH = "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f"

# core/params_test.go'daki elle yazılmış sabitlerin kopyaları.
GENESIS_HASHES = {
    "regtest": "0c9f0d89a173a869e2293a062ddbf9c2a1c3c71c8dc4f7a0d2d8828ebcf6e79d",
    "testnet": "00009ece2400256262b63f068e80aea26f7f6a5682a6688428783a01ace7b34c",
}
KAT_TXID = "f9ac5dab63d144232a87371c58c0795d67bfd7ba5ec573c2dcb0c708bb07765f"

# Mainnet şartları, genesis hedefi gevşetilmiş olarak: taban kuralı (2^16) ve Bitcoin zorluk tabanı
# gerçek değerlerindedir, yalnızca tavan regtest hedefine çekilmiştir.
EASY_BITS = "200fffff"
EASY_MAX = gv.decode_bits(int(EASY_BITS, 16), "sınama")
EASY_MAINNET = {"mainnet": gv.NETWORKS["mainnet"]._replace(max_target=EASY_MAX)}
INPUT_KEYS = ("network", "timestamp", "message", "bits", "pow_limit_bits", "nonce")
# test_floor_rule_matches_launch_targets ızgarasındaki geçerli mainnet başlangıç hedefi sayısı
# (10'unda taban tavana kırpılır). Sabit durur: ızgara sessizce boşalırsa sınama kırmızıya döner.
EXPECTED_FLOOR_CASES = 135


def vector_text(network):
    return (ROOT / "core" / "testdata" / (network + "_vector.json")).read_text(encoding="utf-8").replace("\r\n", "\n")


def mainnet_input(**changes):
    doc = {
        "network": "mainnet",
        "timestamp": BTC_976600_TIME + 900,
        "message": gv.MAINNET_MESSAGE_PREFIX + BTC_976600_HASH,
        "bits": EASY_BITS,
        "pow_limit_bits": EASY_BITS,
        "nonce": 0,
        "bitcoin_headers": [BTC_976600, BTC_976601],
    }
    doc.update(changes)
    return doc


def mined(doc, networks):
    """Nonce'u genesis özeti hedefi karşılayana kadar artırır (kolay hedefte birkaç deneme).
    Başka her hata olduğu gibi yükselir."""
    doc = dict(doc)
    for nonce in range(100000):
        doc["nonce"] = nonce
        try:
            return doc, gv.build_vector(doc, networks)
        except gv.VectorError as e:
            if not str(e).startswith("genesis özeti"):
                raise
    raise AssertionError("nonce bulunamadı")


class CompactTest(unittest.TestCase):
    def test_known_targets(self):
        cases = {
            0x1D00FFFF: 0xFFFF << 208,
            0x1F00FFFF: 0xFFFF << 224,
            0x200FFFFF: 0xFFFFF << 232,
            0x17021673: 0x021673 << 160,
            0x01010000: 1,
            0x02008000: 0x80,
            0x03008000: 0x8000,
        }
        for bits, target in cases.items():
            self.assertEqual(gv.decode_bits(bits, "sınama"), target, "%08x" % bits)
            self.assertEqual(gv.target_to_compact(target), bits, "%08x" % bits)

    def test_invalid_or_non_canonical(self):
        # Sırayla: işaret biti, sıfır mantis, sıfıra kesilen değer, 256 biti aşan iki değer,
        # baştaki sıfır baytı atılmamış mantis, alt baytı kesilen küçük değer.
        for bits in (0x1D80FFFF, 0x1D000000, 0x01000001, 0x2200FFFF, 0xFF00FFFF, 0x1F000FFF, 0x01010001):
            with self.assertRaises(gv.VectorError, msg="%08x" % bits):
                gv.decode_bits(bits, "sınama")

    def test_limits(self):
        mainnet = gv.NETWORKS["mainnet"]
        self.assertEqual(mainnet.max_target, 0xFFFF << 208)
        self.assertEqual(mainnet.floor_shift, 16)
        # Bitcoin tabanı 976600. bloğun hedefinin tam 16 katıdır.
        self.assertEqual(mainnet.bitcoin_max_target, 16 * gv.decode_bits(0x17021673, "sınama"))
        self.assertEqual(gv.target_to_compact(mainnet.bitcoin_max_target), gv.BITCOIN_MAX_BITS)
        for network in ("testnet", "regtest"):
            self.assertEqual(gv.NETWORKS[network], gv.Rules(gv.POW_LIMIT, None, None))
        self.assertEqual(sorted(gv.NETWORKS), ["mainnet", "regtest", "testnet"])


class VectorFileTest(unittest.TestCase):
    def test_repository_vectors(self):
        for network, genesis_hash in GENESIS_HASHES.items():
            vector = gv.check(vector_text(network))
            self.assertEqual(vector["network"], network)
            self.assertEqual(vector["hash"], genesis_hash)
            self.assertEqual(vector["sig_domain"], "QBTC-SIG-v5|%s|%s" % (network, genesis_hash))
            self.assertEqual(vector["kat_txid"], KAT_TXID)
            self.assertNotIn("bitcoin_headers", vector)

    def test_crlf_is_accepted(self):
        gv.check(vector_text("testnet").replace("\n", "\r\n"))

    def test_format_must_be_canonical(self):
        text = vector_text("testnet")
        doc = json.loads(text)
        reordered = dict(reversed(list(doc.items())))
        for name, changed in (
            ("sıkıştırılmış", json.dumps(doc) + "\n"),
            ("anahtar sırası", json.dumps(reordered, indent=2) + "\n"),
            ("fazladan satır", text + "\n"),
            ("satır sonu yok", text[:-1]),
            ("dört boşluk girinti", json.dumps(doc, indent=4) + "\n"),
        ):
            with self.assertRaisesRegex(gv.VectorError, "biçimde değil", msg=name):
                gv.check(changed)
        with self.assertRaisesRegex(gv.VectorError, "JSON çözülemedi"):
            gv.check("﻿" + text)

    def test_changed_input_misses_target(self):
        doc = json.loads(vector_text("testnet"))
        for key, value in (("nonce", doc["nonce"] + 1), ("timestamp", doc["timestamp"] + 1),
                           ("message", doc["message"] + "!")):
            with self.assertRaisesRegex(gv.VectorError, "^genesis özeti .* karşılamıyor", msg=key):
                gv.check(gv.serialize(dict(doc, **{key: value})))

    def test_easier_bits_is_another_block(self):
        # Hedef başlığın içindedir. Testnet özeti (0x00009ece...) 256 kat kolay 2000ffff hedefini de
        # karşılar: hedef başlığa girmeseydi özet değişmez ve bu dosya geçerli sayılırdı. Girdiği için
        # başlık başka bir bloktur ve yeni özeti hedefi karşılamaz.
        doc = json.loads(vector_text("testnet"))
        self.assertEqual((doc["bits"], doc["pow_limit_bits"]), ("1f00ffff", "2000ffff"))
        self.assertLessEqual(int(doc["hash"], 16), gv.decode_bits(0x2000FFFF, "sınama"))
        with self.assertRaisesRegex(gv.VectorError, r"^genesis özeti .* hedefi \(2000ffff\) karşılamıyor"):
            gv.check(gv.serialize(dict(doc, bits="2000ffff")))

    def test_floor_is_an_input_checked_against_start_target(self):
        doc = json.loads(vector_text("testnet"))
        # Taban başlığa girmez: geçerli başka bir taban özeti değiştirmez, vektör yine tutarlıdır.
        # Değerini ağın parametresine Go testi (TestGenesisVectorFiles) bağlar.
        for limit in ("1f00ffff", "200fffff"):
            self.assertEqual(gv.check(gv.serialize(dict(doc, pow_limit_bits=limit)))["hash"], doc["hash"])
        cases = (
            ("başlangıçtan zor taban", "1e00ffff",
             r"^bits: başlangıç hedefi 1f00ffff tabandan \(1e00ffff\) kolay"),
            ("sınırı aşan taban", "2100ffff", r"^pow_limit_bits: taban 2100ffff .*2\^252 - 1"),
            ("kanonik olmayan taban", "20000fff", "^pow_limit_bits: .* kanonik"),
            ("büyük harfli taban", "2000FFFF", "^pow_limit_bits: 8 küçük harf"),
            ("sayı taban", 0x2000FFFF, "^pow_limit_bits: 8 küçük harf"),
        )
        for name, limit, pattern in cases:
            with self.assertRaisesRegex(gv.VectorError, pattern, msg=name):
                gv.check(gv.serialize(dict(doc, pow_limit_bits=limit)))
        with self.assertRaisesRegex(gv.VectorError, "^pow_limit_bits: 8 küçük harf"):
            gv.check(gv.serialize({k: v for k, v in doc.items() if k != "pow_limit_bits"}))

    def test_changed_derived_field(self):
        doc = json.loads(vector_text("testnet"))
        for key in ("receiver", "coinbase_txid", "merkle_root", "hash", "sig_domain", "kat_txid", "kat_signing_hash"):
            flipped = doc[key][:-1] + ("0" if doc[key][-1] != "0" else "1")
            with self.assertRaisesRegex(gv.VectorError, "^%s: dosyada" % key):
                gv.check(gv.serialize(dict(doc, **{key: flipped})))
            missing = {k: v for k, v in doc.items() if k != key}
            with self.assertRaisesRegex(gv.VectorError, "^eksik alan: %s$" % key):
                gv.check(gv.serialize(missing))

    def test_strict_parsing(self):
        text = vector_text("testnet")
        doc = json.loads(text)
        cases = (
            ("bilinmeyen alan", gv.serialize(dict(doc, height=0)), "bilinmeyen alan: height"),
            ("büyük harfli anahtar", text.replace('"nonce"', '"Nonce"'), "bilinmeyen alan: Nonce"),
            ("yinelenen anahtar", text.replace("{", '{\n  "nonce": 168553,', 1), "yinelenen anahtar"),
            ("kesirli sayı", text.replace("1794096000", "1794096000.0"), "tamsayı olmayan sayı"),
            ("üslü sayı", text.replace("168553", "168553e0"), "tamsayı olmayan sayı"),
            ("NaN", text.replace("168553", "NaN"), "tamsayı olmayan sayı"),
            ("dizi", "[]\n", "JSON nesnesi"),
            ("boş", "", "JSON çözülemedi"),
            ("iki nesne", text + text, "JSON çözülemedi"),
            ("mantıksal nonce", gv.serialize(dict(doc, nonce=True)), "^nonce:"),
            ("metin nonce", gv.serialize(dict(doc, nonce="168553")), "^nonce:"),
            ("negatif nonce", gv.serialize(dict(doc, nonce=-1)), "^nonce:"),
            ("64 biti aşan nonce", gv.serialize(dict(doc, nonce=1 << 64)), "^nonce:"),
            ("sıfır zaman", gv.serialize(dict(doc, timestamp=0)), "^timestamp:"),
            ("63 biti aşan zaman", gv.serialize(dict(doc, timestamp=1 << 63)), "^timestamp:"),
            ("boş mesaj", gv.serialize(dict(doc, message="")), "^message:"),
            ("ASCII dışı mesaj", gv.serialize(dict(doc, message="Q-BTC ğ")), "^message:"),
            ("satır sonlu mesaj", gv.serialize(dict(doc, message="Q-BTC\n")), "^message:"),
            ("uzun mesaj", gv.serialize(dict(doc, message="a" * 257)), "^message:"),
            ("büyük harfli hedef", gv.serialize(dict(doc, bits="1F00FFFF")), "^bits:"),
            ("kısa hedef", gv.serialize(dict(doc, bits="1f00fff")), "^bits:"),
            ("sayı hedef", gv.serialize(dict(doc, bits=0x1F00FFFF)), "^bits:"),
            ("kanonik olmayan hedef", gv.serialize(dict(doc, bits="1f000fff")), "^bits: .* kanonik"),
            ("sınırı aşan hedef", gv.serialize(dict(doc, bits="2100ffff")), "^bits: .* kolay"),
            ("bilinmeyen ağ", gv.serialize(dict(doc, network="Testnet")), "^network:"),
            ("ağ yok", gv.serialize({k: v for k, v in doc.items() if k != "network"}), "^network:"),
            ("testnet'te Bitcoin başlığı", gv.serialize(dict(doc, bitcoin_headers=[BTC_976600, BTC_976601])),
             "^bitcoin_headers: testnet"),
            ("testnet'te Bitcoin özeti", gv.serialize(dict(doc, bitcoin_hash=BTC_976600_HASH)),
             "^bitcoin_hash: testnet vektöründe"),
        )
        for name, changed, pattern in cases:
            with self.assertRaisesRegex(gv.VectorError, pattern, msg=name):
                gv.check(changed)


class MainnetRulesTest(unittest.TestCase):
    def test_fixture_headers(self):
        limit = gv.NETWORKS["mainnet"].bitcoin_max_target
        block = gv.bitcoin_header(BTC_976600, limit, "sınama")
        self.assertEqual(block, gv.BitcoinHeader(BTC_976600_HASH,
                         "000000000000000000015c517743911a76b2df8b354bf877b6c249c623695713", BTC_976600_TIME))
        confirmation = gv.bitcoin_header(BTC_976601, limit, "sınama")
        self.assertEqual((confirmation.hash, confirmation.prev), (BTC_976601_HASH, BTC_976600_HASH))
        # 0 ve 1. bloklar gerçek ve birbirine bağlıdır; yalnızca zorluk tabanı gevşetilince geçerler.
        loose = gv.decode_bits(0x1D00FFFF, "sınama")
        self.assertEqual(gv.bitcoin_header(BTC_0, loose, "sınama").hash, BTC_0_HASH)
        self.assertEqual(gv.bitcoin_header(BTC_1, loose, "sınama").prev, BTC_0_HASH)

    def test_valid(self):
        doc, vector = mined(mainnet_input(), EASY_MAINNET)
        self.assertEqual(list(vector), ["network", "timestamp", "message", "bits", "pow_limit_bits", "nonce",
                                        "bitcoin_headers", "receiver", "coinbase_txid", "merkle_root", "hash",
                                        "sig_domain", "kat_txid", "kat_signing_hash", "bitcoin_hash",
                                        "bitcoin_time", "bitcoin_confirmation_hash"])
        self.assertEqual(vector["bitcoin_headers"], [BTC_976600, BTC_976601])
        self.assertEqual(vector["bitcoin_hash"], BTC_976600_HASH)
        self.assertEqual(vector["bitcoin_time"], BTC_976600_TIME)
        self.assertEqual(vector["bitcoin_confirmation_hash"], BTC_976601_HASH)
        self.assertEqual(vector["sig_domain"], "QBTC-SIG-v5|mainnet|" + vector["hash"])
        self.assertEqual(vector["kat_txid"], KAT_TXID)
        # Üretilen vektör kendi denetiminden geçer; Bitcoin alanlarından biri değişirse geçmez.
        text = gv.serialize(vector)
        self.assertEqual(gv.check(text, EASY_MAINNET), vector)
        for key, value in (("bitcoin_hash", BTC_976601_HASH), ("bitcoin_time", BTC_976600_TIME + 1),
                           ("bitcoin_confirmation_hash", BTC_976600_HASH)):
            with self.assertRaisesRegex(gv.VectorError, "^%s: dosyada" % key):
                gv.check(gv.serialize(dict(vector, **{key: value})), EASY_MAINNET)
            with self.assertRaisesRegex(gv.VectorError, "^eksik alan: %s$" % key):
                gv.check(gv.serialize({k: v for k, v in vector.items() if k != key}), EASY_MAINNET)
        # Girdideki sıra ne olursa olsun çıktı aynıdır.
        shuffled = dict(reversed(list(doc.items())))
        self.assertEqual(gv.produce(json.dumps(shuffled), BTC_976600_TIME + 1000, EASY_MAINNET)[1], text)

    def test_real_targets(self):
        # Gerçek mainnet kuralları. Bu hedeflerde nonce aranmaz: hedef şartlarından geçen girdide
        # hata, özetin hedefi karşılamamasıdır ("genesis özeti"); geçmeyende hedef alanındadır.
        accepted = (
            ("1d00ffff", "1d00ffff"),  # başlangıç tavanda: taban da tavandır
            ("1c00ffff", "1d00ffff"),  # 2^16 katı tavanı aşar, taban tavana kırpılır
            ("1b00ffff", "1d00ffff"),  # 2^16 katı tam tavan
            ("1a00ffff", "1c00ffff"),  # tavanın altında: taban tam 2^16 kat
            ("1b0404cb", "1d00ffff"),
            ("1a0404cb", "1c0404cb"),
            ("1a7fffff", "1c7fffff"),
        )
        for bits, limit in accepted:
            with self.assertRaisesRegex(gv.VectorError, "^genesis özeti", msg=bits):
                gv.build_vector(mainnet_input(bits=bits, pow_limit_bits=limit))
        rejected = (
            ("200fffff", "200fffff", r"^pow_limit_bits: taban 200fffff mainnet .*\(1d00ffff\) kolay"),
            ("200fffff", "1d00ffff", r"^bits: başlangıç hedefi 200fffff tabandan \(1d00ffff\) kolay"),
            ("1d010000", "1d010000", r"^pow_limit_bits: taban 1d010000 mainnet"),
            ("1d010000", "1d00ffff", r"^bits: başlangıç hedefi 1d010000 tabandan"),
            ("1a00ffff", "1d00ffff", r"^pow_limit_bits: taban 1d00ffff; .* = 1c00ffff olmalı"),
            ("1a00ffff", "1b00ffff", r"^pow_limit_bits: taban 1b00ffff; .* = 1c00ffff olmalı"),
            ("1a00ffff", "1a00ffff", r"^pow_limit_bits: taban 1a00ffff; .* = 1c00ffff olmalı"),
            ("1c00ffff", "1c00ffff", r"^pow_limit_bits: taban 1c00ffff; .* = 1d00ffff olmalı"),
            ("1a0404cb", "1c00ffff", r"^pow_limit_bits: taban 1c00ffff; .* = 1c0404cb olmalı"),
        )
        for bits, limit, pattern in rejected:
            with self.assertRaisesRegex(gv.VectorError, pattern, msg="%s/%s" % (bits, limit)):
                gv.build_vector(mainnet_input(bits=bits, pow_limit_bits=limit))

    def test_floor_rule_matches_launch_targets(self):
        # core/target.go LaunchTargets tabanı şöyle üretir: min(başlangıç << 16, tavan) hedefinin compact
        # kodlaması. Her kanonik başlangıç hedefinde bu taban tam kodlanmalı, betik onu kabul etmeli ve
        # bir bayt zorunu ya da kolayını reddetmelidir. Hedef şartlarından geçen girdide ilk hata
        # Bitcoin başlıklarının eksikliğidir.
        ceiling = gv.NETWORKS["mainnet"].max_target
        checked = 0
        for exponent in range(1, 0x1E):
            for mantissa in (1, 0x80, 0xFF, 0x100, 0x7FFF, 0x8000, 0xFFFF, 0x10000, 0x0404CB, 0x7FFFFF):
                bits = exponent << 24 | mantissa
                start = gv.compact_to_target(bits)
                if start is None or gv.target_to_compact(start) != bits or start > ceiling:
                    continue  # kanonik değil ya da tavandan kolay: mainnet başlangıç hedefi olamaz
                floor = min(start << 16, ceiling)
                floor_bits = gv.target_to_compact(floor)
                self.assertEqual(gv.decode_bits(floor_bits, "sınama"), floor, "%08x" % bits)
                doc = mainnet_input(bits="%08x" % bits, pow_limit_bits="%08x" % floor_bits)
                del doc["bitcoin_headers"]
                with self.assertRaisesRegex(gv.VectorError, "^bitcoin_headers:", msg="%08x" % bits):
                    gv.build_vector(doc)
                for other in (floor_bits - 0x01000000, floor_bits + 0x01000000):
                    doc["pow_limit_bits"] = "%08x" % other
                    with self.assertRaisesRegex(gv.VectorError, "^(pow_limit_bits|bits):", msg="%08x" % bits):
                        gv.build_vector(doc)
                checked += 1
        self.assertEqual(checked, EXPECTED_FLOOR_CASES)

    def test_genesis_time_not_before_bitcoin_block(self):
        mined(mainnet_input(timestamp=BTC_976600_TIME), EASY_MAINNET)
        with self.assertRaisesRegex(gv.VectorError, r"^timestamp: T_g \(%d\) .*t_b = %d" % (
                BTC_976600_TIME - 1, BTC_976600_TIME)):
            gv.build_vector(mainnet_input(timestamp=BTC_976600_TIME - 1), EASY_MAINNET)

    def test_message_must_name_the_block(self):
        for name, message in (
            ("onaylayan bloğun özeti", gv.MAINNET_MESSAGE_PREFIX + BTC_976601_HASH),
            ("büyük harf", gv.MAINNET_MESSAGE_PREFIX + BTC_976600_HASH.upper()),
            ("bayt sırası ters", gv.MAINNET_MESSAGE_PREFIX + bytes.fromhex(BTC_976600_HASH)[::-1].hex()),
            ("sonda fazladan karakter", gv.MAINNET_MESSAGE_PREFIX + BTC_976600_HASH + " "),
            ("başka önek", "Q-BTC testnet genesis | BTC " + BTC_976600_HASH),
            ("yükseklik eklenmiş", "Q-BTC mainnet genesis | BTC 976600 " + BTC_976600_HASH),
            ("yalnızca özet", BTC_976600_HASH),
        ):
            with self.assertRaisesRegex(gv.VectorError, "^message: ", msg=name):
                gv.build_vector(mainnet_input(message=message), EASY_MAINNET)

    def test_headers_must_be_linked_and_proven(self):
        def broken(header):
            return header[:-1] + ("0" if header[-1] != "0" else "1")  # nonce'un son yarım baytı

        cases = (
            ("yer değiştirmiş", [BTC_976601, BTC_976600], "bağlanmıyor"),
            ("aynı başlık iki kez", [BTC_976600, BTC_976600], "bağlanmıyor"),
            ("ilk başlığın iş ispatı bozuk", [broken(BTC_976600), BTC_976601],
             "^mesajdaki Bitcoin başlığı: iş ispatı"),
            ("ikinci başlığın iş ispatı bozuk", [BTC_976600, broken(BTC_976601)],
             "^onaylayan Bitcoin başlığı: iş ispatı"),
            ("tek başlık", [BTC_976600], "tam iki başlık"),
            ("üç başlık", [BTC_976600, BTC_976601, BTC_976601], "tam iki başlık"),
            ("liste değil", BTC_976600 + BTC_976601, "tam iki başlık"),
            ("sözlük", {"0": BTC_976600, "1": BTC_976601}, "tam iki başlık"),
            ("büyük harf", [BTC_976600.upper(), BTC_976601], "160 küçük harf hex"),
            ("kısa", [BTC_976600[:-2], BTC_976601], "160 küçük harf hex"),
            ("uzun", [BTC_976600 + "00", BTC_976601], "160 küçük harf hex"),
            ("boşluklu", [BTC_976600[:80] + " " + BTC_976600[81:], BTC_976601], "160 küçük harf hex"),
            ("satır sonlu", [BTC_976600, BTC_976601 + "\n"], "160 küçük harf hex"),
            ("metin değil", [BTC_976600, 1], "160 küçük harf hex"),
        )
        for name, headers, pattern in cases:
            with self.assertRaisesRegex(gv.VectorError, pattern, msg=name):
                gv.build_vector(mainnet_input(bitcoin_headers=headers), EASY_MAINNET)
        missing = mainnet_input()
        del missing["bitcoin_headers"]
        with self.assertRaisesRegex(gv.VectorError, "tam iki başlık"):
            gv.build_vector(missing, EASY_MAINNET)

    def test_low_difficulty_headers_rejected(self):
        low = mainnet_input(message=gv.MAINNET_MESSAGE_PREFIX + BTC_0_HASH, timestamp=BTC_976600_TIME,
                            bitcoin_headers=[BTC_0, BTC_1])
        with self.assertRaisesRegex(gv.VectorError,
                                    "^mesajdaki Bitcoin başlığı: hedef 1d00ffff gerçek Bitcoin zorluğu"):
            gv.build_vector(low, EASY_MAINNET)
        # Reddeden zorluk tabanıdır: taban gevşetilince aynı girdi geçer.
        loose = {"mainnet": EASY_MAINNET["mainnet"]._replace(bitcoin_max_target=gv.decode_bits(0x1D00FFFF, "sınama"))}
        _, vector = mined(low, loose)
        self.assertEqual(vector["bitcoin_hash"], BTC_0_HASH)
        self.assertEqual(vector["bitcoin_time"], 1231006505)
        # Tabanın tam sınırı: 976600. bloğun hedefi kabul edilir, bir fazlası edilmez.
        target = gv.decode_bits(0x17021673, "sınama")
        gv.bitcoin_header(BTC_976600, target, "sınama")
        with self.assertRaisesRegex(gv.VectorError, "gerçek Bitcoin zorluğu"):
            gv.bitcoin_header(BTC_976600, target - 1, "sınama")


class ProduceTest(unittest.TestCase):
    def inputs(self, network):
        doc = json.loads(vector_text(network))
        return {key: doc[key] for key in INPUT_KEYS}

    def test_reproduces_repository_vectors(self):
        for network in GENESIS_HASHES:
            doc = self.inputs(network)
            vector, text = gv.produce(json.dumps(doc), doc["timestamp"])
            self.assertEqual(text, vector_text(network))
            self.assertEqual(vector["hash"], GENESIS_HASHES[network])

    def test_derived_fields_in_input_are_checked(self):
        doc = self.inputs("testnet")
        gv.produce(json.dumps(dict(doc, hash=GENESIS_HASHES["testnet"])), doc["timestamp"])
        with self.assertRaisesRegex(gv.VectorError, "^hash: dosyada"):
            gv.produce(json.dumps(dict(doc, hash=GENESIS_HASHES["regtest"])), doc["timestamp"])

    def test_genesis_from_the_future(self):
        doc = self.inputs("testnet")
        gv.produce(json.dumps(doc), doc["timestamp"] - gv.MAX_FUTURE_DRIFT)
        with self.assertRaisesRegex(gv.VectorError, "^timestamp: T_g bu makinenin saatinden 301 sn ileride"):
            gv.produce(json.dumps(doc), doc["timestamp"] - gv.MAX_FUTURE_DRIFT - 1)
        # denetle saate bakmaz.
        gv.check(vector_text("testnet"))

    def test_write_once(self):
        text = vector_text("testnet")
        with tempfile.TemporaryDirectory() as tmp:
            path = pathlib.Path(tmp) / "v.json"
            gv.write_once(str(path), text)
            self.assertEqual(path.read_bytes(), text.encode("ascii"))
            gv.write_once(str(path), text)
            path.write_bytes(text.replace("\n", "\r\n").encode("ascii"))
            gv.write_once(str(path), text)
            other = vector_text("regtest")
            with self.assertRaisesRegex(gv.VectorError, "üzerine yazılmadı"):
                gv.write_once(str(path), other)
            self.assertEqual(path.read_bytes(), text.replace("\n", "\r\n").encode("ascii"))


class CommandLineTest(unittest.TestCase):
    def run_script(self, *args):
        r = subprocess.run([sys.executable, "-I", str(SCRIPT)] + [str(a) for a in args],
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return r.returncode, r.stdout.decode("utf-8"), r.stderr.decode("utf-8")

    def test_check(self):
        good = ROOT / "core" / "testdata" / "testnet_vector.json"
        code, out, err = self.run_script("denetle", good)
        self.assertEqual((code, err), (0, ""))
        self.assertIn("tamam: ", out)
        self.assertIn(GENESIS_HASHES["testnet"], out)
        with tempfile.TemporaryDirectory() as tmp:
            bad = pathlib.Path(tmp) / "bad.json"
            bad.write_bytes(vector_text("testnet").replace("168553", "168554").encode("ascii"))
            code, out, err = self.run_script("denetle", bad, good, pathlib.Path(tmp) / "yok.json")
            self.assertEqual(code, 1)
            self.assertIn("tamam: ", out)  # geçerli dosya yine de denetlenir
            self.assertEqual(err.count("HATA: "), 2)
            self.assertIn("genesis özeti", err)

    def test_produce(self):
        with tempfile.TemporaryDirectory() as tmp:
            tmp = pathlib.Path(tmp)
            doc = json.loads(vector_text("regtest"))
            inputs = {key: doc[key] for key in INPUT_KEYS}
            source = tmp / "girdi.json"
            source.write_text(json.dumps(inputs), encoding="ascii")
            code, out, err = self.run_script("uret", source)
            self.assertEqual((code, out), (0, vector_text("regtest")))
            self.assertIn("regtest genesis " + GENESIS_HASHES["regtest"], err)
            target = tmp / "vektor.json"
            for _ in range(2):  # ikinci çağrı var olan dosyayı aynı bulur
                code, out, err = self.run_script("uret", source, "-o", target)
                self.assertEqual((code, err), (0, ""))
                self.assertEqual(target.read_bytes(), vector_text("regtest").encode("ascii"))
            target.write_bytes(vector_text("testnet").encode("ascii"))
            code, out, err = self.run_script("uret", source, "-o", target)
            self.assertEqual(code, 1)
            self.assertIn("üzerine yazılmadı", err)
            self.assertEqual(target.read_bytes(), vector_text("testnet").encode("ascii"))

    def test_command_line_cannot_relax_mainnet_rules(self):
        with tempfile.TemporaryDirectory() as tmp:
            doc, _ = mined(mainnet_input(), EASY_MAINNET)
            source = pathlib.Path(tmp) / "girdi.json"
            source.write_text(json.dumps(doc), encoding="ascii")
            code, out, err = self.run_script("uret", source)
            self.assertEqual((code, out), (1, ""))
            self.assertIn("pow_limit_bits: taban 200fffff mainnet", err)

    def test_usage(self):
        self.assertEqual(self.run_script()[0], 2)
        self.assertEqual(self.run_script("denetle")[0], 2)
        self.assertEqual(self.run_script("uret")[0], 2)


if __name__ == "__main__":
    unittest.main()
