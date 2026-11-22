"""scripts/lansman_commit_denetle.py sınamaları.

Kullanım (depo kökünden): python3 -I scripts/lansman_commit_denetle_test.py

Yalnızca standart kitaplık ve git kullanır. Her sınama geçici bir depoda, nesneleri git'in alt düzey
komutlarıyla kurar (çalışma ağacı ve satır sonu dönüşümü yoktur). Geçerli örnek, core/testdata/launch
altındaki kolay hedefli lansman dosyası ve vektörüdür; koruma ona genesis hedefi gevşetilmiş
kurallarla uygulanır (gerçek hedefte nonce aramak burada yapılamaz). Komut satırı kuralları
gevşetemez; bunu da bir sınama gösterir.
"""

import importlib.util
import itertools
import json
import os
import re
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
SCRIPT = HERE / "lansman_commit_denetle.py"

sys.dont_write_bytecode = True
_spec = importlib.util.spec_from_file_location("lansman_commit_denetle", str(SCRIPT))
guard = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(guard)
gv = guard.load_vector_module()

EASY = {"mainnet": gv.NETWORKS["mainnet"]._replace(max_target=gv.decode_bits(0x200FFFFF, "sınama"))}
LAUNCH = (ROOT / "core" / "testdata" / "launch" / "easy_mainnet.json").read_bytes().replace(b"\r\n", b"\n")
VECTOR = (ROOT / "core" / "testdata" / "launch" / "easy_mainnet_vector.json").read_bytes().replace(b"\r\n", b"\n")
GENESIS_HASH = json.loads(LAUNCH.decode("ascii"))["hash"]
SUBJECT = guard.SUBJECT_PREFIX + GENESIS_HASH
MESSAGE = (SUBJECT + "\n").encode("ascii")
BASE_FILES = {"core/mainnet.json": b"{}\n", "README.md": b"Q-BTC\n"}
LAUNCH_FILES = dict(BASE_FILES, **{"core/mainnet.json": LAUNCH, "core/testdata/mainnet_vector.json": VECTOR})
TAG = "v-mainnet-sinama"
_counter = itertools.count()


class Repo:
    """Geçici bir git deposu; nesneler alt düzey komutlarla kurulur."""

    def __init__(self, path):
        self.path = str(path)
        os.makedirs(self.path)
        self.git("init", "-q", "-b", "main")
        for key, value in (("user.name", "Sinama"), ("user.email", "sinama@example.invalid"),
                           ("commit.gpgsign", "false"), ("tag.gpgsign", "false"), ("core.autocrlf", "false")):
            self.git("config", key, value)

    def git(self, *args, stdin=None, env=None, check=True):
        full = dict(os.environ)
        full.update({"GIT_AUTHOR_DATE": "1795103800 +0000", "GIT_COMMITTER_DATE": "1795103800 +0000"})
        full.update(env or {})
        r = subprocess.run(["git", "-C", self.path] + list(args), input=stdin, stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE, env=full)
        if check and r.returncode != 0:
            raise AssertionError("git %s: %s" % (" ".join(args), r.stderr.decode("utf-8", "replace")))
        return r.stdout.decode("utf-8", "replace").strip()

    def tree(self, files):
        """files: {yol: bayt} ya da {yol: (kip, bayt)}."""
        index = os.path.join(self.path, ".git", "sinama-index-%d" % next(_counter))
        env = {"GIT_INDEX_FILE": index}
        for path, value in files.items():
            mode, data = value if isinstance(value, tuple) else ("100644", value)
            sha = self.git("hash-object", "-w", "--stdin", stdin=data)
            self.git("update-index", "--add", "--cacheinfo", "%s,%s,%s" % (mode, sha, path), env=env)
        tree = self.git("write-tree", env=env)
        os.remove(index)
        return tree

    def commit(self, files, parents=(), message=b"taban\n"):
        args = ["commit-tree", self.tree(files)]
        for parent in parents:
            args += ["-p", parent]
        return self.git(*args, stdin=message)

    def launch(self, files=None, message=MESSAGE, base_files=None):
        """Taban commit'ini ve onun üstüne lansman commit'ini kurar; (taban, commit) döner."""
        base = self.commit(BASE_FILES if base_files is None else base_files)
        commit = self.commit(LAUNCH_FILES if files is None else files, [base], message)
        return base, commit


class GuardTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="qbtc-koruma-"))
        self.addCleanup(shutil.rmtree, str(self.tmp), True)
        self.repo = Repo(self.tmp / "depo")

    def check(self, base, commit, networks=EASY):
        return guard.check_launch_commit(self.repo.path, base, commit, networks)

    def rejected(self, pattern, base, commit, networks=EASY):
        with self.assertRaisesRegex(guard.GuardError, pattern):
            self.check(base, commit, networks)


class LaunchCommitTest(GuardTestCase):
    def test_valid(self):
        base, commit = self.repo.launch()
        info = self.check(base, commit)
        self.assertEqual(info["commit"], commit)
        self.assertEqual(info["base"], base)
        self.assertEqual(info["genesis_hash"], GENESIS_HASH)
        self.assertEqual(info["subject"], SUBJECT)
        self.assertEqual(info["timestamp"] - info["bitcoin_time"], 900)
        # Commit bir dal adıyla ya da HEAD ile de verilebilir.
        self.repo.git("update-ref", "refs/heads/main", commit)
        self.assertEqual(self.check(base, "main")["commit"], commit)
        self.assertEqual(self.check(base, "HEAD")["commit"], commit)
        self.assertIn(GENESIS_HASH, guard.describe(info))

    def test_real_rules_reject_the_example(self):
        # Örnek mainnet biçimlidir ama gerçek kurallarla geçersizdir (tabanı gerçek tavandan kolay).
        base, commit = self.repo.launch()
        self.rejected("bağımsız betikten geçmedi: pow_limit_bits: taban 200fffff mainnet", base, commit, None)

    def test_parent_must_be_the_base(self):
        base, commit = self.repo.launch()
        other = self.repo.commit(dict(BASE_FILES, **{"README.md": b"baska\n"}))
        self.rejected("ebeveyni .* taban %s olmalı" % other, other, commit)
        # Taban ile lansman commit'i arasına giren bir commit: taban artık büyük ebeveyndir.
        middle = self.repo.commit(BASE_FILES, [base], b"araya giren\n")
        late = self.repo.commit(LAUNCH_FILES, [middle], MESSAGE)
        self.rejected("ebeveyni %s; taban %s olmalı" % (middle, base), base, late)
        # Birleştirme commit'i ve ebeveynsiz commit.
        merge = self.repo.commit(LAUNCH_FILES, [base, other], MESSAGE)
        self.rejected("tek ebeveyni olmalı, 2 var", base, merge)
        root = self.repo.commit(LAUNCH_FILES, [], MESSAGE)
        self.rejected("tek ebeveyni olmalı, 0 var", base, root)
        self.rejected("commit bulunamadı", base, "yok-boyle-bir-dal")
        self.rejected("geçersiz commit adı", base, "--all")

    def test_only_the_two_data_files_may_change(self):
        cases = (
            ("fazladan dosya", dict(LAUNCH_FILES, **{"core/params.go": b"package core\n"}), "fazladan: core/params.go"),
            ("değişen başka dosya", dict(LAUNCH_FILES, **{"README.md": b"degisti\n"}), "fazladan: README.md"),
            ("silinen dosya", {k: v for k, v in LAUNCH_FILES.items() if k != "README.md"}, "fazladan: README.md"),
            ("büyük harfli yol", dict(LAUNCH_FILES, **{"core/Mainnet.json": LAUNCH}), "fazladan: core/Mainnet.json"),
            (".gitattributes", dict(LAUNCH_FILES, **{".gitattributes": b"* text=auto\n"}), "fazladan: .gitattributes"),
            ("vektör yok", {k: v for k, v in LAUNCH_FILES.items() if k != guard.VECTOR_PATH}, "mainnet_vector.json dosyasını eklemiyor"),
            ("lansman dosyası değişmemiş", dict(BASE_FILES, **{guard.VECTOR_PATH: VECTOR}), "mainnet.json dosyasını değiştirmiyor"),
            ("lansman dosyası silinmiş", {"README.md": b"Q-BTC\n", guard.VECTOR_PATH: VECTOR}, "core/mainnet.json: kip 100644 → 000000"),
            ("çalıştırılabilir lansman dosyası", dict(LAUNCH_FILES, **{guard.LAUNCH_PATH: ("100755", LAUNCH)}),
             "core/mainnet.json: kip 100644 → 100755"),
            ("sembolik bağ vektör", dict(LAUNCH_FILES, **{guard.VECTOR_PATH: ("120000", b"../mainnet.json")}),
             "mainnet_vector.json: kip 000000 → 120000"),
            ("sembolik bağ lansman dosyası", dict(LAUNCH_FILES, **{guard.LAUNCH_PATH: ("120000", b"testdata/launch/easy_mainnet.json")}),
             "core/mainnet.json: kip 100644 → 120000"),
        )
        for name, files, pattern in cases:
            base, commit = self.repo.launch(files)
            with self.assertRaisesRegex(guard.GuardError, pattern, msg=name):
                self.check(base, commit)

    def test_base_must_hold_the_empty_launch_file(self):
        for name, base_launch in (("satır sonu yok", b"{}"), ("CRLF", b"{}\r\n"), ("dolu", LAUNCH.replace(b'"nonce": 6', b'"nonce": 7')),
                                  ("boşluklu", b"{ }\n")):
            base, commit = self.repo.launch(base_files=dict(BASE_FILES, **{guard.LAUNCH_PATH: base_launch}))
            with self.assertRaisesRegex(guard.GuardError, "tabandaki core/mainnet.json boş lansman dosyası", msg=name):
                self.check(base, commit)
        # Tabanda vektör zaten varsa commit onu ekleyemez, yalnızca değiştirebilir.
        base, commit = self.repo.launch(base_files=dict(BASE_FILES, **{guard.VECTOR_PATH: b"{}\n"}))
        self.rejected("mainnet_vector.json: kip 100644 → 100644, durum M", base, commit)

    def test_launch_file_must_be_canonical_and_reproduce_the_vector(self):
        doc = json.loads(LAUNCH.decode("ascii"))
        reordered = json.dumps(dict(reversed(list(doc.items()))), indent=2).encode("ascii") + b"\n"
        extra = json.dumps(dict(doc, receiver="QBTC" + "0" * 40), indent=2).encode("ascii") + b"\n"
        missing = json.dumps({k: v for k, v in doc.items() if k != "hash"}, indent=2).encode("ascii") + b"\n"
        cases = (
            ("CRLF lansman dosyası", {guard.LAUNCH_PATH: LAUNCH.replace(b"\n", b"\r\n")}, "CR karakteri"),
            ("CRLF vektör", {guard.VECTOR_PATH: VECTOR.replace(b"\n", b"\r\n")}, "CR karakteri"),
            ("ASCII olmayan", {guard.LAUNCH_PATH: LAUNCH.replace(b"Q-BTC", "Q-BTÇ".encode("utf-8"))}, "ASCII değil"),
            ("alan sırası", {guard.LAUNCH_PATH: reordered}, "kanonik biçimde değil"),
            ("sıkıştırılmış", {guard.LAUNCH_PATH: json.dumps(doc).encode("ascii") + b"\n"}, "kanonik biçimde değil"),
            ("sonda fazladan satır", {guard.LAUNCH_PATH: LAUNCH + b"\n"}, "kanonik biçimde değil"),
            ("fazladan alan", {guard.LAUNCH_PATH: extra}, "alanları tam olarak"),
            ("eksik alan", {guard.LAUNCH_PATH: missing}, "alanları tam olarak"),
            ("boş nesne", {guard.LAUNCH_PATH: b"{}\n"}, "mainnet.json dosyasını değiştirmiyor"),
            ("JSON değil", {guard.LAUNCH_PATH: b"genesis\n"}, "bağımsız betikten geçmedi: JSON çözülemedi"),
            ("başka nonce", {guard.LAUNCH_PATH: LAUNCH.replace(b'"nonce": 6', b'"nonce": 7')},
             "bağımsız betikten geçmedi: genesis özeti"),
            ("başka özet", {guard.LAUNCH_PATH: LAUNCH.replace(GENESIS_HASH.encode("ascii"), b"0" * 64)},
             "bağımsız betikten geçmedi: hash: dosyada"),
            ("vektörde başka alıcı", {guard.VECTOR_PATH: VECTOR.replace(b'"receiver": "QBTC', b'"receiver": "QBTD')},
             "bağımsız betikten geçmedi: receiver: dosyada"),
            ("vektör biçimi", {guard.VECTOR_PATH: json.dumps(json.loads(VECTOR.decode("ascii"))).encode("ascii") + b"\n"},
             "bağımsız betikten geçmedi: dosya betiğin üreteceği biçimde değil"),
            ("vektör yerine lansman dosyası", {guard.VECTOR_PATH: LAUNCH}, "bağımsız betikten geçmedi: eksik alan"),
        )
        for name, changed, pattern in cases:
            base, commit = self.repo.launch(dict(LAUNCH_FILES, **changed))
            with self.assertRaisesRegex(guard.GuardError, pattern, msg=name):
                self.check(base, commit)

    def test_commit_message_is_fixed(self):
        for name, message in (("başka metin", b"genesis\n"), ("fazladan satır", MESSAGE + b"\nek aciklama\n"),
                              ("satır sonu yok", MESSAGE[:-1]), ("başka özet", (guard.SUBJECT_PREFIX + "0" * 64 + "\n").encode("ascii")),
                              ("sonda boşluk", (SUBJECT + " \n").encode("ascii"))):
            base, commit = self.repo.launch(message=message)
            with self.assertRaisesRegex(guard.GuardError, "commit mesajı tam olarak", msg=name):
                self.check(base, commit)

    def test_history_rewriting_state_is_rejected(self):
        base, commit = self.repo.launch()
        self.repo.git("update-ref", "refs/heads/main", commit)
        # Sığ klon: ebeveyn zinciri doğrulanamaz.
        shallow = self.tmp / "sig"
        subprocess.run(["git", "clone", "-q", "--depth", "1", pathlib.Path(self.repo.path).as_uri(), str(shallow)],
                       check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        with self.assertRaisesRegex(guard.GuardError, "depo sığ"):
            guard.check_launch_commit(str(shallow), base, commit, EASY)
        # replace başvurusu: lansman commit'i başka bir commit'le değiştirilmiş görünür.
        other = self.repo.commit(LAUNCH_FILES, [base], MESSAGE + b"\n")
        self.repo.git("replace", commit, other)
        self.rejected("replace başvurusu", base, commit)
        self.repo.git("replace", "-d", commit)
        self.check(base, commit)
        # grafts dosyası.
        grafts = pathlib.Path(self.repo.path) / ".git" / "info" / "grafts"
        grafts.parent.mkdir(exist_ok=True)
        grafts.write_text("%s\n" % commit)
        self.rejected("grafts dosyası", base, commit)


class TagTest(GuardTestCase):
    def setUp(self):
        super().setUp()
        self.base, self.commit = self.repo.launch()

    def check_tag(self, tag=TAG, key=None, commit=None):
        return guard.check_tag(self.repo.path, tag, commit or self.commit, SUBJECT, key)

    def test_annotated_tag_on_the_launch_commit(self):
        self.repo.git("tag", "-a", "-m", SUBJECT, TAG, self.commit)
        info = self.check_tag()
        self.assertEqual(info, {"tag": TAG, "signed_by": None})
        self.assertIn("etiket %s, imzalayan doğrulanmadı" % TAG, guard.describe(self.check(self.base, self.commit), info))

    def test_rejected_tags(self):
        self.repo.git("tag", "hafif", self.commit)
        self.repo.git("tag", "-a", "-m", SUBJECT, "tabanda", self.base)
        self.repo.git("tag", "-a", "-m", "baska mesaj", "mesaj", self.commit)
        self.repo.git("tag", "-a", "-m", SUBJECT, "dalla-ayni", self.commit)
        self.repo.git("update-ref", "refs/heads/dalla-ayni", self.base)
        # Etiketi gösteren etiket: iç etiket lansman commit'ini gösterse de dıştaki bir etiket nesnesini gösterir.
        self.repo.git("tag", "-a", "-m", SUBJECT, "ic", self.commit)
        self.repo.git("tag", "-a", "-m", SUBJECT, "dis", "ic")
        cases = (
            ("hafif", "açıklamalı"),
            ("tabanda", "lansman commit'ini .* göstermiyor"),
            ("mesaj", "etiket mesajının ilk satırı"),
            ("dalla-ayni", "aynı adda bir dal var"),
            ("dis", "lansman commit'ini .* göstermiyor: tag"),
            ("yok", "etiket bulunamadı"),
            ("-kotu", "geçersiz etiket adı"),
            ("a/b", "geçersiz etiket adı"),
            ("", "geçersiz etiket adı"),
        )
        for tag, pattern in cases:
            with self.assertRaisesRegex(guard.GuardError, pattern, msg=tag):
                self.check_tag(tag)
        # Başvuru adı ile etiket nesnesindeki ad farklı (etiket başka adla kopyalanmış).
        self.repo.git("tag", "-a", "-m", SUBJECT, "asil", self.commit)
        self.repo.git("update-ref", "refs/tags/kopya", self.repo.git("rev-parse", "refs/tags/asil"))
        with self.assertRaisesRegex(guard.GuardError, "etiket nesnesindeki ad 'asil'"):
            self.check_tag("kopya")

    def signing_setup(self, name):
        """Geçici bir SSH anahtarıyla imzalamayı kurar; parmak izini döner. Araç yoksa sınama atlanır."""
        key = self.tmp / name
        try:
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", str(key)], check=True,
                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            listing = subprocess.run(["ssh-keygen", "-lf", str(key) + ".pub"], check=True, stdout=subprocess.PIPE,
                                     stderr=subprocess.PIPE).stdout.decode("ascii").split()
        except (OSError, subprocess.CalledProcessError) as e:
            if os.environ.get("CI"):  # CI'da imza sınamaları sessizce atlanmaz
                raise AssertionError("ssh-keygen kullanılamıyor: %s" % e)
            self.skipTest("ssh-keygen kullanılamıyor: %s" % e)
        public = pathlib.Path(str(key) + ".pub").read_text().strip()
        allowed = self.tmp / "izinli-imzacilar"
        with open(str(allowed), "a", encoding="ascii", newline="\n") as f:
            f.write('%s@example.invalid namespaces="git" %s\n' % (name, public))
        self.repo.git("config", "gpg.format", "ssh")
        self.repo.git("config", "gpg.ssh.allowedSignersFile", allowed.as_posix())
        return pathlib.Path(str(key) + ".pub").as_posix(), listing[1]

    def test_signature_is_verified_against_the_announced_key(self):
        public, fingerprint = self.signing_setup("baslatici")
        self.repo.git("config", "user.signingkey", public)
        self.repo.git("tag", "-s", "-m", SUBJECT, TAG, self.commit)
        self.assertTrue(fingerprint.startswith("SHA256:"))
        self.assertEqual(self.check_tag(key=fingerprint), {"tag": TAG, "signed_by": fingerprint})
        # Geçerli imza, ama duyurulan anahtarla değil.
        with self.assertRaisesRegex(guard.GuardError, "duyurulan anahtarla .* imzalanmamış; imzalayan: %s" % re.escape(fingerprint)):
            self.check_tag(key="SHA256:" + "A" * 43)
        # İzinli imzacılar dosyasındaki başka bir anahtarla atılmış geçerli imza da reddedilir.
        other_public, other_fingerprint = self.signing_setup("baskasi")
        self.repo.git("config", "user.signingkey", other_public)
        self.repo.git("tag", "-s", "-m", SUBJECT, "baskasinin", self.commit)
        self.assertEqual(self.check_tag("baskasinin", key=other_fingerprint)["signed_by"], other_fingerprint)
        with self.assertRaisesRegex(guard.GuardError, "duyurulan anahtarla"):
            self.check_tag("baskasinin", key=fingerprint)
        # İmzasız etiket, anahtar istendiğinde reddedilir.
        self.repo.git("tag", "-a", "-m", SUBJECT, "imzasiz", self.commit)
        with self.assertRaisesRegex(guard.GuardError, "imzası doğrulanamadı"):
            self.check_tag("imzasiz", key=fingerprint)
        with self.assertRaisesRegex(guard.GuardError, "parmak izi tanınmadı"):
            self.check_tag(key="anahtarim")

    def test_fingerprint_parsing(self):
        gpg = ("[GNUPG:] NEWSIG\n[GNUPG:] GOODSIG 1234567890ABCDEF Baslatici\n"
               "[GNUPG:] VALIDSIG AAAABBBBCCCCDDDDEEEEFFFF0000111122223333 2026-11-20 1795103800 0 4 0 22 10 00 "
               "9999888877776666555544443333222211110000\n")
        self.assertEqual(guard.signature_fingerprints(gpg), {"AAAABBBBCCCCDDDDEEEEFFFF0000111122223333",
                                                             "9999888877776666555544443333222211110000"})
        ssh = 'Good "git" signature for a@example.invalid with ED25519 key SHA256:abcDEF123+/xyz\n'
        self.assertEqual(guard.signature_fingerprints(ssh), {"SHA256:abcDEF123+/xyz"})
        self.assertEqual(guard.signature_fingerprints("gpg: BAD signature\n"), set())
        self.assertEqual(guard.normalize_fingerprint("aaaa bbbb cccc dddd eeee  ffff 0000 1111 2222 3333"),
                         "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333")
        self.assertEqual(guard.normalize_fingerprint(" SHA256:abc "), "SHA256:abc")


class CommandLineTest(GuardTestCase):
    def run_script(self, *args):
        r = subprocess.run([sys.executable, "-I", str(SCRIPT)] + [str(a) for a in args], stdout=subprocess.PIPE,
                           stderr=subprocess.PIPE)
        return r.returncode, r.stdout.decode("utf-8"), r.stderr.decode("utf-8")

    def test_command_line_uses_the_real_rules(self):
        base, commit = self.repo.launch()
        code, out, err = self.run_script("--repo", self.repo.path, "--taban", base, "--commit", commit)
        self.assertEqual((code, out), (1, ""))
        self.assertIn("korumadan geçemedi", err)
        self.assertIn("pow_limit_bits: taban 200fffff mainnet", err)
        # Başka bir dosyaya dokunan commit, vektör denetimine gelmeden düşer.
        base, commit = self.repo.launch(dict(LAUNCH_FILES, **{"main.go": b"package main\n"}))
        code, out, err = self.run_script("--repo", self.repo.path, "--taban", base, "--commit", commit)
        self.assertEqual(code, 1)
        self.assertIn("fazladan: main.go", err)

    def test_usage(self):
        self.assertEqual(self.run_script()[0], 2)
        self.assertEqual(self.run_script("--taban", "HEAD", "--anahtar", "SHA256:abc")[0], 2)
        # Bu deponun kendisi: HEAD bir lansman commit'i değildir.
        code, _, err = self.run_script("--taban", "HEAD", "--commit", "HEAD")
        self.assertEqual(code, 1)
        self.assertIn("korumadan geçemedi", err)


if __name__ == "__main__":
    unittest.main()
