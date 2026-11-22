"""scripts/lansman.py sınamaları.

Kullanım (depo kökünden): python3 -I scripts/lansman_test.py

Yalnızca standart kitaplık, git ve ssh-keygen kullanır (ssh-keygen yoksa imza gerektiren sınamalar
atlanır). git gerçektir: her sınama geçici bir "herkese açık" çıplak depo, lansman makinesi ve ikinci
makine klonları kurar; taban etiketi geçici bir SSH anahtarıyla imzalanır. go, qbtc, ssh, saat
eşitlemesi araçları, saat ve Bitcoin kaynakları sahtedir. Sahte ssh, ikinci makinedeki komutu aynı
süreçte, ikinci makinenin klonu ve kendi sahte kabuğuyla çalıştırır. Lansman dosyası core/testdata/launch
altındaki kolay hedefli örnektir; kurallar genesis hedefi gevşetilmiş kurallardır.
"""

import contextlib
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
ROOT = HERE.parent
SCRIPT = HERE / "lansman.py"

sys.dont_write_bytecode = True
_spec = importlib.util.spec_from_file_location("lansman", str(SCRIPT))
lansman = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(lansman)
guard = lansman.load_guard()
gv = guard.load_vector_module()

EASY = {"mainnet": gv.NETWORKS["mainnet"]._replace(max_target=gv.decode_bits(0x200FFFFF, "sınama"))}
LAUNCH = (ROOT / "core" / "testdata" / "launch" / "easy_mainnet.json").read_bytes().replace(b"\r\n", b"\n")
VECTOR = (ROOT / "core" / "testdata" / "launch" / "easy_mainnet_vector.json").read_bytes().replace(b"\r\n", b"\n")
DOC = json.loads(LAUNCH.decode("ascii"))
T_G = DOC["timestamp"]
BLOCK, CONFIRMATION = DOC["bitcoin_headers"]
BLOCK_HASH, _, BLOCK_TIME = gv.bitcoin_header(BLOCK, gv.NETWORKS["mainnet"].bitcoin_max_target, "b")
CONFIRMATION_HASH = gv.bitcoin_header(CONFIRMATION, gv.NETWORKS["mainnet"].bitcoin_max_target, "c").hash
BASE_TAG, TAG = "lansman-oncesi", "v-mainnet"
SECOND = {"ssh": "dogrulayici@ikinci", "dizin": "/ikinci/depo", "durum": "/ikinci/durum"}
CHRONY = "Reference ID    : C0A80001 (ntp)\nSystem time     : 0.000210 seconds fast of NTP time\nLast offset     : +0.000001 seconds\n"


def git(path, *args, check=True):
    r = subprocess.run(["git", "-C", str(path)] + [str(a) for a in args], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and r.returncode != 0:
        raise AssertionError("git %s: %s" % (" ".join(str(a) for a in args), r.stderr.decode("utf-8", "replace")))
    return r.stdout.decode("utf-8", "replace").strip()


def configure(path, signing=None):
    for key, value in (("user.name", "Sinama"), ("user.email", "sinama@example.invalid"), ("commit.gpgsign", "false"),
                       ("tag.gpgsign", "false"), ("core.autocrlf", "false"), ("gpg.format", "ssh")):
        git(path, "config", key, value)
    if signing is not None:
        git(path, "config", "gpg.ssh.allowedSignersFile", signing["allowed"])
        git(path, "config", "user.signingkey", signing["public"])


class Clock:
    def __init__(self, now):
        self.value = now

    def __call__(self):
        return self.value


class FakeShell:
    """Sahte dış dünya. git gerçek git'e gider (ikinci makinenin ssh adresi yerel klona çevrilir);
    go, qbtc, ssh, timedatectl, chronyc ve ağ sahtedir. Her çağrı calls'a yazılır."""

    def __init__(self, world, machine):
        self.world, self.machine = world, machine
        self.real = lansman.Shell()
        self.calls = []
        self.synchronized, self.chrony = "yes\n", CHRONY
        self.go_test_code = 0
        self.timeouts = []
        self.fail = {}  # komut önekinden (ör. "git push --atomic") (kod, çıktı) sonucuna

    def now(self):
        return self.world.clock()

    def fetch(self, url):
        return self.world.fetch(url)

    def run(self, args, cwd=None, live=False, timeout=None):
        args = [str(a) for a in args]
        self.timeouts.append((args[:4], timeout))
        # Ağ seçenekleri (git -c core.sshCommand=..., ssh -o ...) her ağ çağrısında olmalıdır; kayda ve
        # eşleştirmeye onlarsız girer.
        plain = list(args)
        if plain[:1 + len(lansman.GIT_NET)] == ["git"] + list(lansman.GIT_NET):
            plain = ["git"] + plain[1 + len(lansman.GIT_NET):]
        if plain[:1] == ["ssh"]:
            if plain[1:1 + len(lansman.SSH_OPTIONS)] != list(lansman.SSH_OPTIONS):
                raise AssertionError("ssh BatchMode olmadan çağrıldı: %s" % args)
            plain = ["ssh"] + plain[1 + len(lansman.SSH_OPTIONS):]
        if plain[:2] in (["git", "push"], ["git", "ls-remote"]) and plain == args:
            raise AssertionError("ağ üzerindeki git çağrısı BatchMode olmadan: %s" % args)
        self.calls.append(plain)
        line = " ".join(plain)
        for prefix, result in self.fail.items():
            if line.startswith(prefix):
                return result[0], result[1], ""
        name = os.path.basename(args[0])
        if name == "git":
            url = lansman.candidate_url(SECOND)
            args = [str(self.world.second) if a == url else a for a in args]
            return self.real.run(args, cwd)
        if name == "ssh":
            return self.world.ssh(plain)
        if name == "timedatectl":
            if args[1] == "show":
                return 0, self.synchronized, ""
            return 1, "", "timesync yok"
        if name == "chronyc":
            return (0, self.chrony, "") if self.chrony is not None else (1, "", "chronyd çalışmıyor")
        if name == "go":
            if args[1] == "build":
                pathlib.Path(args[args.index("-o") + 1]).write_bytes(b"ikili")
                return 0, "", ""
            return self.go_test_code, "ok\n" if self.go_test_code == 0 else "FAIL\n", ""
        if name == "qbtc":
            return self.world.qbtc(args, cwd)
        if name == "ssh":
            return self.world.ssh(args)
        raise AssertionError("beklenmeyen komut: %s" % line)


class World:
    """Herkese açık depo, lansman makinesi ve ikinci makine."""

    def __init__(self, tmp, signing):
        self.tmp, self.signing = tmp, signing
        self.clock = Clock(T_G - 120)
        self.public = tmp / "herkese-acik.git"
        source = tmp / "kaynak"
        subprocess.run(["git", "init", "-q", "--bare", "-b", "main", str(self.public)], check=True, stdout=subprocess.PIPE)
        subprocess.run(["git", "-c", "core.autocrlf=false", "init", "-q", "-b", "main", str(source)], check=True, stdout=subprocess.PIPE)
        configure(source, signing)
        (source / "core").mkdir()
        (source / "core" / "mainnet.json").write_bytes(b"{}\n")
        (source / "README.md").write_bytes(b"Q-BTC\n")
        (source / "core" / "testdata").mkdir()
        (source / "core" / "testdata" / "regtest_vector.json").write_bytes(b"{}\n")
        git(source, "add", "-A")
        git(source, "commit", "-q", "-m", "taban")
        self.base = git(source, "rev-parse", "HEAD")
        git(source, "tag", "-s", "-m", "lansman oncesi", BASE_TAG)
        git(source, "push", "-q", str(self.public), "main", "refs/tags/%s" % BASE_TAG)
        self.launcher = self.clone("lansman-makinesi")
        self.second = self.clone("ikinci-makine")
        self.launcher_state = tmp / "durum-lansman"
        self.second_state = tmp / "durum-ikinci"
        self.shells = {"lansman": FakeShell(self, "lansman"), "ikinci": FakeShell(self, "ikinci")}
        self.chain = {BLOCK_HASH: (BLOCK, {"in_best_chain": True, "next_best": CONFIRMATION_HASH, "height": 976600}),
                      CONFIRMATION_HASH: (CONFIRMATION, {"in_best_chain": True, "next_best": None, "height": 976601})}
        self.tip = CONFIRMATION_HASH
        self.fetched = []
        self.mined = []
        self.found_at = T_G + 3

    def clone(self, name):
        path = self.tmp / name
        subprocess.run(["git", "-c", "core.autocrlf=false", "clone", "-q", str(self.public), str(path)], check=True, stdout=subprocess.PIPE,
                       stderr=subprocess.PIPE)
        configure(path, self.signing)
        return path

    def launch(self, machine="lansman"):
        repo, state = (self.launcher, self.launcher_state) if machine == "lansman" else (self.second, self.second_state)
        return lansman.Launch(self.shells[machine], repo, state, BASE_TAG, TAG, self.signing["fingerprint"], EASY)

    def fetch(self, url):
        self.fetched.append(url)
        for source in lansman.BITCOIN_SOURCES:
            if url.startswith(source + "/"):
                path = url[len(source):]
                break
        else:
            raise AssertionError("bilinmeyen kaynak: %s" % url)
        if path == "/blocks/tip/hash":
            return self.tip + "\n"
        parts = path.split("/")
        if len(parts) == 4 and parts[1] == "block" and parts[2] in self.chain:
            header, status = self.chain[parts[2]]
            return header if parts[3] == "header" else json.dumps(status)
        raise OSError("HTTP Error 404: %s" % url)

    def qbtc(self, args, cwd):
        self.mined.append(args)
        if "-launch-preflight" in args:
            return 0, "ön-uçuş tamam\n", ""
        record = pathlib.Path(args[args.index("-launch-record") + 1])
        report = pathlib.Path(args[args.index("-launch-report") + 1])
        self.clock.value = self.found_at
        (pathlib.Path(cwd) / "core" / "mainnet.json").write_bytes(LAUNCH)
        record.write_text(json.dumps({
            "schema": 1, "network": "mainnet", "genesis_hash": DOC["hash"], "timestamp": T_G, "commit": git(cwd, "rev-parse", "HEAD"),
            "report_sha256": hashlib.sha256(report.read_bytes()).hexdigest(),
            "launch_file_sha256": hashlib.sha256(LAUNCH).hexdigest(), "bitcoin_hash": BLOCK_HASH,
        }), encoding="utf-8")
        return 0, "genesis bulundu\n", ""

    def ssh(self, args):
        """ikinci makinede: 'cd DIZIN && python3 -I scripts/lansman.py KOMUT ...'"""
        if args[1] != SECOND["ssh"]:
            raise AssertionError("beklenmeyen ssh hedefi: %s" % args[1])
        words = shlex.split(args[2])
        if words[:3] != ["cd", SECOND["dizin"], "&&"] or words[3:6] != ["python3", "-I", "scripts/lansman.py"]:
            raise AssertionError("beklenmeyen uzak komut: %s" % args[2])
        argv = [str(self.second_state) if w == SECOND["durum"] else w for w in words[6:]]
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = lansman.main(argv, self.shells["ikinci"], self.second, EASY)
        return code, out.getvalue() + err.getvalue(), ""


def make_signing(tmp, name="baslatici"):
    key = tmp / name
    try:
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", name, "-f", str(key)], check=True,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        listing = subprocess.run(["ssh-keygen", "-lf", str(key) + ".pub"], check=True, stdout=subprocess.PIPE,
                                 stderr=subprocess.PIPE).stdout.decode("ascii").split()
    except (OSError, subprocess.CalledProcessError) as e:
        if os.environ.get("CI"):  # CI'da imza sınamaları sessizce atlanmaz
            raise AssertionError("ssh-keygen kullanılamıyor: %s" % e)
        raise unittest.SkipTest("ssh-keygen kullanılamıyor: %s" % e)
    public = pathlib.Path(str(key) + ".pub")
    allowed = tmp / "izinli-imzacilar"
    with open(str(allowed), "a", encoding="ascii", newline="\n") as f:
        f.write('%s@example.invalid namespaces="git" %s\n' % (name, public.read_text().strip()))
    return {"public": public.as_posix(), "allowed": allowed.as_posix(), "fingerprint": listing[1]}


class WorldTestCase(unittest.TestCase):
    def setUp(self):
        self.tmp = pathlib.Path(tempfile.mkdtemp(prefix="qbtc-lansman-"))
        self.addCleanup(shutil.rmtree, str(self.tmp), True)
        self.world = World(self.tmp, make_signing(self.tmp))
        self.report = self.tmp / "qbtc-olcum.json"
        self.report.write_text('{"schema": 1}\n', encoding="ascii")
        self.out = io.StringIO()
        redirect = contextlib.redirect_stdout(self.out)
        redirect.__enter__()
        self.addCleanup(redirect.__exit__, None, None, None)

    def ready(self):
        """Ön-uçuş ve kazım; aday hazır."""
        launch = self.world.launch()
        launch.on_ucus(BLOCK, CONFIRMATION, self.report, SECOND)
        return launch, launch.kaz(BLOCK, CONFIRMATION, self.report)

    def public_refs(self):
        return git(self.world.public, "for-each-ref", "--format=%(refname) %(objectname)")

    def state(self, machine="lansman"):
        path = (self.world.launcher_state if machine == "lansman" else self.world.second_state) / lansman.STATE_FILE
        return json.loads(path.read_text(encoding="utf-8"))


class FlowTest(WorldTestCase):
    def test_full_launch(self):
        launch, attempt = self.ready()
        world = self.world
        commit = attempt["commit"]
        # Aday: tabanın üstünde yalnızca iki veri dosyası, imzasız ve etiketsiz; hiçbir şey yayımlanmadı.
        self.assertEqual(git(world.launcher, "rev-parse", "HEAD~1"), world.base)
        self.assertEqual(git(world.launcher, "diff", "--name-only", world.base, commit).split(), [guard.LAUNCH_PATH, guard.VECTOR_PATH])
        self.assertEqual((world.launcher / guard.VECTOR_PATH).read_bytes(), VECTOR)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertNotIn(commit, self.public_refs())
        self.assertEqual(attempt["durum"], "hazir")
        self.assertEqual((attempt["bitcoin"], attempt["t_b"], attempt["bitcoin_yukseklik"]), (BLOCK_HASH, BLOCK_TIME, 976600))

        world.clock.value = T_G + 100
        published = launch.yayimla(SECOND)
        # İkinci makine doğruladı; ancak ondan sonra imza atıldı ve dal ile etiket birlikte itildi.
        launcher_calls = [" ".join(c) for c in world.shells["lansman"].calls]
        ssh_index = next(i for i, c in enumerate(launcher_calls) if c.startswith("ssh ") and " dogrula " in c)
        sign_index = next(i for i, c in enumerate(launcher_calls) if c.startswith("git tag -s"))
        push_index = next(i for i, c in enumerate(launcher_calls) if c.startswith("git push --atomic"))
        self.assertLess(ssh_index, sign_index)
        self.assertLess(sign_index, push_index)
        second_calls = [" ".join(c) for c in world.shells["ikinci"].calls]
        self.assertIn("go test -count=1 ./...", second_calls)
        self.assertFalse(any(c.startswith("git tag") or c.startswith("git push") for c in second_calls))
        self.assertEqual(git(world.public, "rev-parse", "refs/heads/main"), commit)
        self.assertEqual(git(world.public, "rev-parse", "refs/tags/%s^{commit}" % TAG), commit)
        self.assertEqual(published["yayin_eksi_T_g"], 100)
        self.assertTrue(published["an_kesin"])
        self.assertIn("YAYIMLANDI", self.out.getvalue())
        # Yayımlanan etiket duyurulan anahtarla imzalıdır.
        guard.check_tag(str(world.launcher), TAG, commit, guard.SUBJECT_PREFIX + DOC["hash"], world.signing["fingerprint"])
        second_machine = self.state()["denemeler"][-1]["ikinci_makine"]
        self.assertEqual((second_machine["dogrulandi"], second_machine["saat_farki"]), (True, 100))

        world.clock.value = T_G + 400
        record = launch.kayit([("mempool.space", T_G - 870), ("blockstream.info", T_G - 880)])
        self.assertEqual(record["genesis_hash"], DOC["hash"])
        self.assertEqual(record["launch_commit"], commit)
        self.assertEqual(record["base_commit"], world.base)
        self.assertEqual(record["publish_minus_genesis_seconds"], 100)
        self.assertEqual(record["publish_minus_first_seen_seconds"], 980)
        self.assertEqual(record["discarded_attempts"], [])
        self.assertEqual(record["tool_record"]["genesis_hash"], DOC["hash"])
        record_tag = TAG + lansman.RECORD_SUFFIX
        guard.verify_tag_signature(str(world.launcher), record_tag, world.signing["fingerprint"])
        self.assertEqual(git(world.public, "rev-parse", "refs/tags/%s^{commit}" % record_tag), commit)
        body = git(world.launcher, "cat-file", "tag", record_tag)
        self.assertIn('"genesis_hash": "%s"' % DOC["hash"], body)
        # Kayıt yeniden çalıştırılabilir (etiket varsa yeniden imzalanmaz).
        launch.kayit([("mempool.space", T_G - 870), ("blockstream.info", T_G - 880)])
        with self.assertRaisesRegex(lansman.LaunchError, "zaten yayımlandı"):
            launch.yayimla(SECOND)

    def test_discarded_attempt_is_recorded(self):
        world = self.world
        launch = world.launch()
        launch.on_ucus(BLOCK, CONFIRMATION, self.report, SECOND)
        world.shells["lansman"].fail["%s -network mainnet -mine-genesis" % launch.binary()] = (2, "HATA: blok bayat\n")
        with self.assertRaisesRegex(lansman.LaunchError, "kazım aracı durdu .çıkış 2."):
            launch.kaz(BLOCK, CONFIRMATION, self.report)
        with self.assertRaisesRegex(lansman.LaunchError, "önceki deneme atılmadan"):
            launch.kaz(BLOCK, CONFIRMATION, self.report)
        launch.at("araç durdu: blok bayat")
        world.shells["lansman"].fail.clear()
        attempt = launch.kaz(BLOCK, CONFIRMATION, self.report)
        self.assertEqual(attempt["no"], 2)
        # İkinci makinede testler kırmızı: imza atılmaz, hiçbir şey itilmez.
        world.shells["ikinci"].go_test_code = 1
        world.clock.value = T_G + 60
        with self.assertRaisesRegex(lansman.LaunchError, "ikinci makinede dogrula başarısız(.|\n)*adayın testleri"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertNotIn(attempt["commit"], self.public_refs())
        launch.at("ikinci makinede testler kırmızı")
        self.assertEqual(git(world.launcher, "rev-parse", "HEAD"), world.base)
        self.assertEqual(git(world.launcher, "status", "--porcelain", "--untracked-files=all"), "")
        world.shells["ikinci"].go_test_code = 0
        world.clock.value = T_G - 60
        attempt = launch.kaz(BLOCK, CONFIRMATION, self.report)
        world.clock.value = T_G + 90
        launch.yayimla(SECOND)
        record = launch.kayit([("a", T_G - 900), ("b", T_G - 890)])
        self.assertEqual([(a["number"], a["state"], a["reason"]) for a in record["discarded_attempts"]],
                         [(1, "atildi", "araç durdu: blok bayat"), (2, "atildi", "ikinci makinede testler kırmızı")])
        self.assertEqual(record["discarded_attempts"][0]["tool_exit_code"], 2)
        self.assertEqual(record["discarded_attempts"][1]["genesis_hash"], DOC["hash"])

    def test_threshold_and_remote_checks_stop_before_signing(self):
        launch, attempt = self.ready()
        world = self.world
        # Eşik: imzadan önce T_g'den bu yana 570 sn'den fazla geçmişse imza atılmaz. İkinci makinenin saati
        # 7 sn geride: o eşikten geçer, iki saat de tutar; düşüren lansman makinesinin kendi denetimidir.
        world.clock.value = T_G + lansman.PUBLISH_LIMIT - lansman.PUSH_MARGIN + 2
        world.shells["ikinci"].now = lambda: T_G + lansman.PUBLISH_LIMIT - lansman.PUSH_MARGIN - 5
        with self.assertRaisesRegex(lansman.LaunchError, "imzadan önce: T_g'den bu yana 572 sn geçti"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertFalse(any(c[:3] == ["git", "tag", "-s"] for c in world.shells["lansman"].calls))
        del world.shells["ikinci"].now
        # Eşik imza sırasında aşılırsa (belirteç onayı gecikti) etiket silinir, hiçbir şey itilmez.
        world.clock.value = T_G + 100
        real = lansman.Shell().run

        def slow_signing(args, cwd=None, live=False, timeout=None):
            r = real(args, cwd)
            if args[:3] == ["git", "tag", "-s"]:
                world.clock.value = T_G + lansman.PUBLISH_LIMIT - lansman.PUSH_MARGIN + 1
            return r

        world.shells["lansman"].real = type("Real", (), {"run": staticmethod(slow_signing)})()
        with self.assertRaisesRegex(lansman.LaunchError, "imzadan sonra: T_g'den bu yana 571 sn geçti"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertNotIn(attempt["commit"], self.public_refs())
        world.shells["lansman"].real = lansman.Shell()
        # Uzaktaki dal taban commit'ten ilerlemişse itilmez.
        world.clock.value = T_G + 60
        other = self.tmp / "baskasi"
        subprocess.run(["git", "-c", "core.autocrlf=false", "clone", "-q", str(world.public), str(other)], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        configure(other)
        (other / "README.md").write_bytes(b"degisti\n")
        git(other, "commit", "-q", "-am", "araya giren")
        git(other, "push", "-q", "origin", "main")
        with self.assertRaisesRegex(lansman.LaunchError, "uzaktaki main dalı taban commit'te"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertNotIn(attempt["commit"], self.public_refs())

    def test_failed_push_deletes_the_tag(self):
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        world.shells["lansman"].fail["git push --atomic"] = (1, "bağlantı reddedildi\n")
        with self.assertRaisesRegex(lansman.LaunchError, "itme başarısız; hiçbir şey yayımlanmadı ve imzalı etiket silindi"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertEqual(self.state()["denemeler"][-1]["durum"], "hazir")
        # Süre varsa yeniden denenir.
        world.shells["lansman"].fail.clear()
        world.clock.value = T_G + 200
        self.assertEqual(launch.yayimla(SECOND)["yayin_eksi_T_g"], 200)

    def test_push_that_landed_despite_an_error_counts_as_published(self):
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        real = world.shells["lansman"].real.run

        def push_then_fail(args, cwd=None, live=False):
            r = real(args, cwd)
            return (128, r[1], "bağlantı koptu") if " push --atomic " in " ".join(args) else r

        world.shells["lansman"].real = type("Real", (), {"run": staticmethod(push_then_fail)})()
        published = launch.yayimla(SECOND)
        self.assertFalse(published["an_kesin"])
        self.assertEqual(git(world.public, "rev-parse", "refs/heads/main"), attempt["commit"])
        self.assertIn("UYARI: itme komutunun sonucu okunamamıştı", self.out.getvalue())

    def interrupt_after(self, fragment):
        """fragment'i içeren git komutu çalıştıktan hemen sonra süreç kesilmiş gibi KeyboardInterrupt."""
        real = lansman.Shell().run

        def run(args, cwd=None, live=False):
            r = real(args, cwd)
            if fragment in " ".join(args):
                raise KeyboardInterrupt
            return r

        self.world.shells["lansman"].real = type("Real", (), {"run": staticmethod(run)})()

    def test_interrupted_after_the_push_landed(self):
        # İtme indi, süreç sonucu kaydedemeden kesildi: yayın geri alınamaz. Yeniden çalıştırma
        # doğrulamayı ve imzayı yinelemeden yalnızca sonucu okur; deneme atılamaz.
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        self.interrupt_after(" push --atomic ")
        with self.assertRaises(KeyboardInterrupt):
            launch.yayimla(SECOND)
        self.assertEqual(self.state()["denemeler"][-1]["durum"], "itiliyor")
        self.assertEqual(git(world.public, "rev-parse", "refs/heads/main"), attempt["commit"])
        world.shells["lansman"].real = lansman.Shell()
        relaunch = world.launch()
        with self.assertRaisesRegex(lansman.LaunchError, "son denemenin durumu 'itiliyor'"):
            relaunch.at("yanlışlıkla")
        with self.assertRaisesRegex(lansman.LaunchError, "henüz deneme yok|önceki deneme atılmadan"):
            relaunch.kaz(BLOCK, CONFIRMATION, self.report)
        verifications = sum(1 for c in world.shells["lansman"].calls if c[0] == "ssh" and "dogrula" in c[-1])
        world.clock.value = T_G + 300
        published = relaunch.yayimla(SECOND)
        self.assertFalse(published["an_kesin"])
        self.assertEqual(published["itme_basladi"], T_G + 60)
        self.assertEqual(sum(1 for c in world.shells["lansman"].calls if c[0] == "ssh" and "dogrula" in c[-1]), verifications)
        self.assertEqual(git(world.launcher, "rev-parse", "refs/tags/%s^{commit}" % TAG), attempt["commit"])

    def test_interrupted_after_signing(self):
        # İmza atıldı, itme başlamadan süreç kesildi: yerel etiket silinir; hiçbir şey yayımlanmadı.
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        self.interrupt_after("tag -s")
        with self.assertRaises(KeyboardInterrupt):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")
        self.assertEqual(self.state()["denemeler"][-1]["durum"], "hazir")
        self.assertNotIn(attempt["commit"], self.public_refs())
        # Etiket bir biçimde yerelde kalmışsa (ör. elektrik kesintisi) yeniden çalıştırma onu siler ve yeniden imzalar.
        world.shells["lansman"].real = lansman.Shell()
        git(world.launcher, "tag", "-a", "-m", "kalinti", TAG, attempt["commit"])
        world.clock.value = T_G + 200
        self.assertEqual(world.launch().yayimla(SECOND)["yayin_eksi_T_g"], 200)
        guard.check_tag(str(world.launcher), TAG, attempt["commit"], guard.SUBJECT_PREFIX + DOC["hash"], world.signing["fingerprint"])

    def test_successful_push_is_never_downgraded(self):
        # İtme 0 ile döndü ama uzak depo (gecikme, bayat okuma) henüz tabanı gösteriyor: deneme "hazir"e
        # dönmez, etiket silinmez; durum "belirsiz" kalır ve sonraki çalıştırma yalnızca sonucu okur.
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        real = lansman.Shell().run

        def swallowed_push(args, cwd=None, live=False, timeout=None):
            if " push --atomic " in " ".join(args):
                return 0, "", ""  # sunucu kabul etti diyor, güncelleme henüz görünmüyor
            return real(args, cwd)

        world.shells["lansman"].real = type("Real", (), {"run": staticmethod(swallowed_push)})()
        with self.assertRaisesRegex(lansman.LaunchError, "beklenmeyen durumda"):
            launch.yayimla(SECOND)
        self.assertEqual(self.state()["denemeler"][-1]["durum"], "belirsiz")
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), TAG)
        with self.assertRaisesRegex(lansman.LaunchError, "son denemenin durumu 'belirsiz'"):
            launch.at("yanlışlıkla")
        # Güncelleme sonradan görünür: yayın anı itmenin döndüğü andır.
        world.shells["lansman"].real = lansman.Shell()
        git(world.launcher, "push", "-q", "--atomic", str(world.public), "%s:refs/heads/main" % attempt["commit"],
            "refs/tags/%s:refs/tags/%s" % (TAG, TAG))
        world.clock.value = T_G + 500
        published = launch.yayimla(SECOND)
        self.assertTrue(published["an_kesin"])
        self.assertEqual(published["yayin_eksi_T_g"], 60)

    def test_stale_ready_state_records_the_publication(self):
        # Durum dosyası "hazir"de kalmış ama aday yayımlanmış (kayıp yazım): yayimla doğrulama ve imza
        # yinelemeden yayını kaydeder; at reddeder.
        launch, attempt = self.ready()
        world = self.world
        git(world.launcher, "tag", "-a", "-m", guard.SUBJECT_PREFIX + DOC["hash"], TAG, attempt["commit"])
        git(world.launcher, "push", "-q", "--atomic", str(world.public), "%s:refs/heads/main" % attempt["commit"],
            "refs/tags/%s:refs/tags/%s" % (TAG, TAG))
        with self.assertRaisesRegex(lansman.LaunchError, "yayımlanmış olabilir, atılmaz"):
            launch.at("yanlışlıkla")
        world.clock.value = T_G + 120
        published = launch.yayimla(SECOND)
        self.assertFalse(published["an_kesin"])
        self.assertFalse(any(c[0] == "ssh" for c in world.shells["lansman"].calls if "dogrula" in c[-1]))
        self.assertFalse(any(c[:3] == ["git", "tag", "-s"] for c in world.shells["lansman"].calls))

    def test_push_timeout_is_uncertain(self):
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        world.shells["lansman"].fail["git push --atomic"] = (lansman.TIMED_OUT, "")
        with self.assertRaisesRegex(lansman.LaunchError, "beklenmeyen durumda"):
            launch.yayimla(SECOND)
        self.assertEqual(self.state()["denemeler"][-1]["durum"], "belirsiz")
        self.assertIn((["git"] + list(lansman.GIT_NET[:3]), lansman.PUSH_TIMEOUT),
                      [(a[:4], t) for a, t in world.shells["lansman"].timeouts if t])

    def test_discard_keeps_a_found_genesis(self):
        # Araç kaydı yazıp sıfırdan farklı kodla çıktı: atılan denemede bulunan genesis de kayda girer.
        world = self.world
        launch = world.launch()
        launch.on_ucus(BLOCK, CONFIRMATION, self.report, SECOND)
        original = world.qbtc

        def found_then_failed(args, cwd):
            original(args, cwd)
            (pathlib.Path(cwd) / "core" / "mainnet.json").write_bytes(b"{}\n")
            return 1, "yazım hatası\n", ""

        world.qbtc = found_then_failed
        with self.assertRaisesRegex(lansman.LaunchError, "çıkış 1"):
            launch.kaz(BLOCK, CONFIRMATION, self.report)
        launch.at("araç kaydı yazdıktan sonra durdu")
        attempt = self.state()["denemeler"][0]
        self.assertEqual((attempt["genesis"], attempt["T_g"]), (DOC["hash"], T_G))

    def test_bitcoin_block_not_in_best_chain(self):
        world = self.world
        launch = world.launch()
        world.chain[BLOCK_HASH][1]["in_best_chain"] = False
        world.launcher_state.mkdir()
        launch.binary().write_bytes(b"ikili")
        with self.assertRaisesRegex(lansman.LaunchError, "en çok iş taşıyan zincirde değil"):
            launch.kaz(BLOCK, CONFIRMATION, self.report)
        self.assertFalse((world.launcher_state / lansman.STATE_FILE).exists())

    def test_second_machine_clock(self):
        launch, attempt = self.ready()
        world = self.world
        world.clock.value = T_G + 60
        # İkinci makinenin saati T_g'yi gelecekte görüyor.
        world.shells["ikinci"].now = lambda: T_G - lansman.MAX_AHEAD - 1
        with self.assertRaisesRegex(lansman.LaunchError, "ikinci makinede dogrula başarısız(.|\n)*makinenin saatinden 6 sn ileride"):
            launch.yayimla(SECOND)
        # İkinci makinenin saati 10 sn'den fazla farklı.
        world.shells["ikinci"].now = lambda: T_G + 60 - lansman.MAX_CLOCK_DISAGREEMENT - 1
        with self.assertRaisesRegex(lansman.LaunchError, "iki makinenin saati tutmuyor"):
            launch.yayimla(SECOND)
        self.assertEqual(git(world.launcher, "tag", "-l", TAG), "")


class PrerequisiteTest(WorldTestCase):
    def test_base_tag(self):
        world = self.world
        launch = world.launch()
        self.assertEqual(launch.begin(), world.base)
        # İmzasız ya da başka anahtarla imzalı taban etiketi reddedilir.
        git(world.launcher, "tag", "-a", "-m", "imzasiz", "imzasiz-taban", world.base)
        unsigned = lansman.Launch(world.shells["lansman"], world.launcher, world.launcher_state, "imzasiz-taban", TAG,
                                  world.signing["fingerprint"], EASY)
        with self.assertRaisesRegex(guard.GuardError, "imzası doğrulanamadı"):
            unsigned.begin()
        wrong = lansman.Launch(world.shells["lansman"], world.launcher, world.launcher_state, BASE_TAG, TAG,
                               "SHA256:" + "A" * 43, EASY)
        with self.assertRaisesRegex(guard.GuardError, "duyurulan anahtarla"):
            wrong.begin()
        for base_tag, tag, pattern in ((BASE_TAG, BASE_TAG, "aynı olamaz"), ("-x", TAG, "geçersiz etiket adı"),
                                       (BASE_TAG, "a b", "geçersiz etiket adı")):
            bad = lansman.Launch(world.shells["lansman"], world.launcher, world.launcher_state, base_tag, tag,
                                 world.signing["fingerprint"], EASY)
            with self.assertRaisesRegex(lansman.LaunchError, pattern):
                bad.begin()
        inside = lansman.Launch(world.shells["lansman"], world.launcher, world.launcher / "durum", BASE_TAG, TAG,
                                world.signing["fingerprint"], EASY)
        with self.assertRaisesRegex(lansman.LaunchError, "deponun içinde olamaz"):
            inside.begin()

    def test_check_base_tag_points_to_a_commit(self):
        world = self.world
        tree = git(world.launcher, "rev-parse", "HEAD^{tree}")
        git(world.launcher, "tag", "-s", "-m", "agac", "agac-etiketi", tree)
        with self.assertRaisesRegex(guard.GuardError, "bir commit'i göstermiyor: tree"):
            guard.check_base_tag(str(world.launcher), "agac-etiketi", world.signing["fingerprint"])
        git(world.launcher, "tag", "hafif-taban", world.base)
        with self.assertRaisesRegex(guard.GuardError, "açıklamalı"):
            guard.check_base_tag(str(world.launcher), "hafif-taban", world.signing["fingerprint"])
        self.assertEqual(guard.check_base_tag(str(world.launcher), BASE_TAG, world.signing["fingerprint"]), world.base)

    def test_dirty_tree_and_wrong_head(self):
        world = self.world
        launch = world.launch()
        (world.launcher / "yeni.txt").write_text("x")
        with self.assertRaisesRegex(lansman.LaunchError, "çalışma ağacı temiz değil"):
            launch.hazir()
        (world.launcher / "yeni.txt").unlink()
        (world.launcher / "README.md").write_bytes(b"degisti\n")
        git(world.launcher, "commit", "-q", "-am", "yerel")
        with self.assertRaisesRegex(lansman.LaunchError, "HEAD .*; beklenen %s" % world.base):
            launch.hazir()

    def test_clock_sync(self):
        world = self.world
        launch = world.launch()
        shell = world.shells["lansman"]
        self.assertAlmostEqual(launch.check_clock_sync(), 0.00021)
        shell.synchronized = "no\n"
        with self.assertRaisesRegex(lansman.LaunchError, "saat eşitlenmemiş"):
            launch.check_clock_sync()
        shell.synchronized = "yes\n"
        shell.chrony = "System time     : 1.250000 seconds slow of NTP time\n"
        with self.assertRaisesRegex(lansman.LaunchError, "saat sapması 1.250 sn"):
            launch.check_clock_sync()
        # chrony yoksa systemd-timesyncd'nin ölçtüğü sapma okunur.
        shell.chrony = None
        with self.assertRaisesRegex(lansman.LaunchError, "saat sapması ölçülemedi"):
            launch.check_clock_sync()
        original = shell.run

        def timesync(args, cwd=None, live=False):
            if args[:2] == ["timedatectl", "timesync-status"]:
                return 0, "       Server: 192.0.2.1\n       Offset: -12.345ms\n", ""
            return original(args, cwd, live)

        shell.run = timesync
        self.assertAlmostEqual(launch.check_clock_sync(), 0.012345)

    def test_bitcoin_sources(self):
        world = self.world
        launch = world.launch()
        self.assertEqual(launch.basliklar(), (BLOCK, CONFIRMATION))
        self.assertIn("--blok %s" % BLOCK, self.out.getvalue())
        self.assertEqual(len({url.split("/api")[0] for url in world.fetched}), 2)
        # Onaylayan blok başka: kaynaklarda mesajdaki bloğun üstünde başka bir blok var.
        world.chain[BLOCK_HASH][1]["next_best"] = "0" * 64
        with self.assertRaisesRegex(lansman.LaunchError, "mesajdaki bloğun üstündeki blok 0{64}"):
            launch.check_bitcoin_chain(BLOCK, CONFIRMATION)
        world.chain[BLOCK_HASH][1]["next_best"] = CONFIRMATION_HASH
        # Başlıklar ters verilmiş.
        with self.assertRaisesRegex(lansman.LaunchError, "bağlanmıyor"):
            launch.check_bitcoin_chain(CONFIRMATION, BLOCK)
        # Kaynak ulaşılamaz.
        del world.chain[BLOCK_HASH]
        with self.assertRaisesRegex(lansman.LaunchError, "alınamadı"):
            launch.check_bitcoin_chain(BLOCK, CONFIRMATION)


class RecordInputTest(unittest.TestCase):
    def test_first_seen(self):
        self.assertEqual(lansman.parse_first_seen(["mempool.space=1795103300", "b = 1795103301"]),
                         [("mempool.space", 1795103300), ("b", 1795103301)])
        for bad in ("mempool.space", "=1795103300", "a=abc", "a=12"):
            with self.assertRaises(lansman.LaunchError):
                lansman.parse_first_seen([bad])


class CommandLineTest(unittest.TestCase):
    def run_script(self, *args):
        r = subprocess.run([sys.executable, "-I", str(SCRIPT)] + list(args), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return r.returncode, r.stdout.decode("utf-8"), r.stderr.decode("utf-8")

    def test_usage(self):
        common = ["--durum", "/tmp/d", "--taban", BASE_TAG, "--etiket", TAG, "--anahtar", "SHA256:abc"]
        self.assertEqual(self.run_script()[0], 2)
        self.assertEqual(self.run_script("yayimla", *common)[0], 2)  # ikinci makine zorunlu
        code, _, err = self.run_script("yayimla", *common, "--ikinci-ssh", "a@b", "--ikinci-dizin", "goreli/yol",
                                       "--ikinci-durum", "/d")
        self.assertEqual(code, 2)
        self.assertIn("mutlak", err)
        code, _, err = self.run_script("yayimla", *common, "--ikinci-ssh=-oProxyCommand=x", "--ikinci-dizin", "/a",
                                       "--ikinci-durum", "/d")
        self.assertEqual(code, 2)
        # Eşikleri gevşeten seçenek yoktur.
        code, _, err = self.run_script("yayimla", *common, "--ikinci-ssh", "a@b", "--ikinci-dizin", "/a", "--ikinci-durum", "/d",
                                       "--esik", "3600")
        self.assertEqual(code, 2)
        self.assertIn("unrecognized arguments: --esik", err)

    def test_thresholds_are_pinned(self):
        self.assertEqual((lansman.PUBLISH_LIMIT, lansman.PUSH_MARGIN, lansman.MAX_CLOCK_OFFSET, lansman.MAX_AHEAD,
                          lansman.MAX_CLOCK_DISAGREEMENT), (600, 30, 1.0, 5, 10))
        self.assertEqual(lansman.BITCOIN_SOURCES, ("https://mempool.space/api", "https://blockstream.info/api"))


if __name__ == "__main__":
    unittest.main()
