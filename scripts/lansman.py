"""Q-BTC mainnet lansman betiği (docs/LANSMAN-KILAVUZU.md; tasarım: docs/LANSMAN.md §3.4 ve §4.5).

Lansman günü adımlarını sırayla ve eşikleriyle yürütür. Genesis'i kazan araç (qbtc -mine-genesis),
bağımsız vektör betiği (genesis_vektor.py) ve lansman commit'i koruması (lansman_commit_denetle.py)
ayrı ayrı denetlenmiştir; bu betik onları bağlar ve onların göremediğini denetler: saat eşitlemesi,
ikinci makine, Bitcoin bloğunun en çok iş taşıyan zincirde olduğu, "yayın anı − T_g" eşiği, atılan
denemeler ve lansman kaydı.

Komutlar:
  basliklar Bitcoin'in uç bloğunu ve ebeveynini iki bağımsız kaynaktan alır, karşılaştırır ve
            yazdırır (mesaja ebeveyn, yani bir onay almış blok girer).
  hazir     İkinci makinede: saat eşitlemesi, temiz ağaç, imzalı taban etiketi, derleme, testler.
  on-ucus   Lansman makinesinde: aynı denetimler, ikilinin derlenmesi, Bitcoin başlıkları,
            qbtc -launch-preflight ve ikinci makinenin hazır olduğu.
  kaz       Genesis'i kazar, vektörü üretir, lansman testlerini çalıştırır, yalnızca iki veri
            dosyasını commit'ler ve korumayı çalıştırır. İmza atmaz, hiçbir şey yayımlamaz.
  dogrula   İkinci makinede: adayı temiz bir klonda doğrular (koruma, Bitcoin bloğu iki kaynakta,
            derleme, bütün testler, T_g ile bu makinenin saati).
  yayimla   Adayı ikinci makinede doğrulatır; ancak ondan sonra etiketi imzalar, eşiği ve uzaktaki
            dalı denetler, dalı ve etiketi tek adımda iter.
  at        Yayımlanmamış denemeyi atar: nedenini kaydeder, etiketi ve commit'i geri alır.
  kayit     Lansman kaydını kurar, imzalı bir etiket olarak yazar ve iter.

Ortak seçenekler: --durum (depo dışında durum dizini), --taban (lansman öncesi commit'i gösteren,
günler önce duyurulmuş imzalı etiket), --etiket (lansman etiketi), --anahtar (duyurulan imza
anahtarının parmak izi).

Eşikler sabittir ve komut satırından gevşetilemez:
  - yayın anı − T_g en çok 600 sn (kullanıcı kararı, LANSMAN.md §7 madde 3); imza ve itme için
    30 sn pay bırakılır.
  - saat eşitlemesi sapması 1 saniyenin altında olmalıdır.
  - T_g, ikinci makinenin saatinden en çok 5 sn ileride olabilir; iki makinenin T_g'ye uzaklığı
    en çok 10 sn farklı olabilir.

Çıkış kodu: 0 tamam; 1 bir denetim düştü ya da adım başarısız; 2 kullanım hatası. Yalnızca standart
kitaplık ve git, lansman için ayrıca Go ile ssh kullanır.
"""

import argparse
import datetime
import hashlib
import importlib.util
import json
import os
import pathlib
import re
import shlex
import shutil
import subprocess
import sys
import time
import urllib.request

HERE = pathlib.Path(__file__).resolve().parent

PUBLISH_LIMIT = 600  # yayın anı − T_g, saniye (LANSMAN.md §7 madde 3)
PUSH_MARGIN = 30  # imza ve itme için eşikten bırakılan pay, saniye
MAX_CLOCK_OFFSET = 1.0  # saat eşitlemesi sapması, saniye
MAX_AHEAD = 5  # T_g ikinci makinenin saatinden en çok bu kadar ileride olabilir, saniye
MAX_CLOCK_DISAGREEMENT = 10  # iki makinenin T_g'ye uzaklığı en çok bu kadar farklı olabilir, saniye
MAX_FIRST_SEEN_LEAD = 7200  # Bitcoin kuralı: blok damgası gerçek zamandan en çok iki saat ileride olabilir
LAUNCH_TESTS = "Launch|Genesis|Mainnet|Network|Params"  # lansman makinesindeki hızlı alt küme
CANDIDATE_REF = "refs/lansman/aday"
STATE_FILE = "durum.json"
RECORD_FILE = "lansman-kaydi.json"
RECORD_SUFFIX = "-kayit"
VERIFIED_MARK = "DOGRULANDI"
# Bitcoin bloğunun en çok iş taşıyan zincirde olduğu iki bağımsız işletmecinin Esplora arayüzünden
# sorulur. Araç ve yükleyici bunu bilemez: yalnızca başlığın iş ispatını doğrularlar.
BITCOIN_SOURCES = ("https://mempool.space/api", "https://blockstream.info/api")
FETCH_TIMEOUT = 15
NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*")
REF_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/-]*")
SSH_TARGET = re.compile(r"[A-Za-z0-9_][A-Za-z0-9_.-]*(@[A-Za-z0-9][A-Za-z0-9.-]*)?")
REMOTE_PATH = re.compile(r"/[A-Za-z0-9_./-]*")
GIT_REDIRECTS = ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_SSH",
                 "GIT_SSH_COMMAND")
OPEN_STATES = ("araniyor", "arac-durdu", "bulundu", "hazir")  # 'at' ile kapatılabilen deneme durumları
# İtme başladıktan sonraki durumlar: sonuç yalnızca uzak depodan okunur; deneme atılamaz, yeniden imza atılmaz.
PUSH_STATES = ("itiliyor", "belirsiz")
# Ağ üzerindeki git ve ssh çağrıları istem beklemez (ana makine anahtarı, parola): pencere bir istemde
# tükenmesin, hemen düşsün. Ana makine anahtarları provada kabul edilir.
SSH_OPTIONS = ("-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3")
# HTTPS uzaklar için de: 15 sn boyunca saniyede 1000 bayttan yavaş aktarım kesilir.
GIT_NET = ("-c", "core.sshCommand=ssh " + " ".join(SSH_OPTIONS), "-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=15")
PUSH_TIMEOUT = 60  # geri alınamayan itmenin en uzun süresi, saniye; aşılırsa sonuç belirsiz sayılır
TIMED_OUT = 124


class LaunchError(Exception):
    """Bir denetim düştü ya da bir adım başarısız oldu."""


_GUARD = []


def load_guard():
    """Korumayı bu dosyanın yanından bir kez yükler (aynı hata sınıfları her yerde aynı kalsın)."""
    if not _GUARD:
        sys.dont_write_bytecode = True  # depoda scripts/__pycache__ kalmasın: izlenmeyen dosya ağacı kirletir
        spec = importlib.util.spec_from_file_location("lansman_commit_denetle", str(HERE / "lansman_commit_denetle.py"))
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        _GUARD.append(module)
    return _GUARD[0]


class Shell:
    """Dış dünyaya açılan kapı: komut çalıştırma, saat ve ağ. Sınamalar sahtesini verir."""

    def run(self, args, cwd=None, live=False, timeout=None):
        """Komutu çalıştırır; (çıkış kodu, stdout, stderr) döner. timeout aşılırsa süreç öldürülür ve kod
        TIMED_OUT olur (live ile birlikte verilmez). live ise çıktı satır satır
        stderr'e de yazılır (uzun adımlar ve imza istemleri görünsün) ve birleşik olarak döner."""
        env = dict(os.environ)
        for name in GIT_REDIRECTS:
            env.pop(name, None)
        env["GIT_TERMINAL_PROMPT"] = "0"
        args = [str(a) for a in args]
        cwd = None if cwd is None else str(cwd)
        try:
            if not live:
                try:
                    r = subprocess.run(args, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=timeout)
                except subprocess.TimeoutExpired:
                    return TIMED_OUT, "", "%s %d sn içinde bitmedi" % (args[0], timeout)
                return r.returncode, r.stdout.decode("utf-8", "replace"), r.stderr.decode("utf-8", "replace")
            lines = []
            with subprocess.Popen(args, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, env=env) as process:
                for raw in process.stdout:
                    line = raw.decode("utf-8", "replace")
                    lines.append(line)
                    sys.stderr.write("  | " + line)
                    sys.stderr.flush()
            return process.returncode, "".join(lines), ""
        except OSError as e:
            return 127, "", "%s çalıştırılamadı: %s" % (args[0], e)

    def now(self):
        return int(time.time())

    def fetch(self, url):
        request = urllib.request.Request(url, headers={"User-Agent": "lansman-denetimi/1"})
        with urllib.request.urlopen(request, timeout=FETCH_TIMEOUT) as response:
            return response.read(1 << 16).decode("utf-8", "replace")


def iso(timestamp):
    return datetime.datetime.fromtimestamp(timestamp, datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def say(text):
    print(text, flush=True)


def candidate_url(second):
    """Adayın itildiği yer: ikinci makinedeki depo. Herkese açık depo değildir."""
    return "ssh://%s%s" % (second["ssh"], second["dizin"])


class Launch:
    """Lansman adımları. repo bu betiğin deposudur; networks yalnızca sınamalarda verilir (genesis
    hedefi gevşetilmiş kurallar)."""

    def __init__(self, shell, repo, state_dir, base_tag, tag, key, networks=None):
        self.shell = shell
        self.repo = pathlib.Path(repo).resolve()
        self.state_dir = pathlib.Path(state_dir).resolve()
        self.base_tag, self.tag, self.key, self.networks = base_tag, tag, key, networks
        self.guard = load_guard()
        self.gv = self.guard.load_vector_module()
        self.rules = self.gv.NETWORKS if networks is None else networks
        self._base = None

    # --- yardımcılar ---

    def run(self, args, cwd=None, what=None, live=False):
        code, out, err = self.shell.run(args, self.repo if cwd is None else cwd, live)
        if code != 0:
            tail = (out + "\n" + err).strip()[-1500:]
            raise LaunchError("%s başarısız (çıkış %d):\n%s" % (what or " ".join(str(a) for a in args[:3]), code, tail))
        return out

    def git(self, *args, cwd=None, live=False):
        return self.run(["git"] + list(args), cwd, "git " + " ".join(args[:2]), live).strip()

    def begin(self):
        """Her komutun başı: durum dizini deponun dışındadır ve taban, günler önce duyurulmuş, duyurulan
        anahtarla imzalı etiketin gösterdiği commit'tir. Taban commit'in özetini döndürür."""
        if self.state_dir == self.repo or self.repo in self.state_dir.parents:
            # İçindeki dosyalar ağacı kirletir ve aracın kirli ağaç denetimini düşürür.
            raise LaunchError("durum dizini deponun içinde olamaz: %s" % self.state_dir)
        if self._base is None:
            for name in (self.base_tag, self.tag):
                if not NAME.fullmatch(name or ""):
                    raise LaunchError("geçersiz etiket adı: %r" % name)
            if self.base_tag in (self.tag, self.tag + RECORD_SUFFIX):
                raise LaunchError("taban etiketi lansman etiketiyle ya da kayıt etiketiyle aynı olamaz: %s" % self.base_tag)
            self._base = self.guard.check_base_tag(str(self.repo), self.base_tag, self.key)
        return self._base

    def load_state(self):
        path = self.state_dir / STATE_FILE
        if not path.exists():
            return {"taban": self.begin(), "etiket": self.tag, "denemeler": [], "yayin": None}
        with open(str(path), encoding="utf-8") as f:
            state = json.load(f)
        if state.get("taban") != self.begin() or state.get("etiket") != self.tag:
            raise LaunchError("durum dizini başka bir lansmana ait (taban %s, etiket %s)" % (state.get("taban"), state.get("etiket")))
        return state

    def save_state(self, state):
        self.state_dir.mkdir(parents=True, exist_ok=True)
        tmp = self.state_dir / (STATE_FILE + ".yeni")
        with open(str(tmp), "w", encoding="utf-8", newline="\n") as f:
            json.dump(state, f, ensure_ascii=False, indent=2)
            f.write("\n")
            f.flush()
            os.fsync(f.fileno())
        os.replace(str(tmp), str(self.state_dir / STATE_FILE))
        try:  # dizin girdisi de diske: elektrik kesintisinde "itiliyor" kaydı kaybolmasın
            fd = os.open(str(self.state_dir), os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        except OSError:
            pass  # Windows ve bazı dosya sistemleri dizin fsync'ini desteklemez

    def head(self):
        return self.git("rev-parse", "HEAD")

    def check_tree(self, at):
        """Çalışma ağacı temiz ve HEAD beklenen commit'te olmalıdır (izlenmeyen dosya da kirlidir)."""
        dirty = self.git("status", "--porcelain", "--untracked-files=all")
        if dirty:
            raise LaunchError("çalışma ağacı temiz değil:\n%s" % dirty[:800])
        head = self.head()
        if head != at:
            raise LaunchError("HEAD %s; beklenen %s" % (head, at))

    def check_clock_sync(self):
        """Saat eşitlemesi: işletim sistemi eşitlendiğini söylemeli ve ölçülen sapma 1 saniyenin altında
        olmalıdır. chrony ya da systemd-timesyncd desteklenir. Sapmayı saniye olarak döndürür."""
        code, out, err = self.shell.run(["timedatectl", "show", "-p", "NTPSynchronized", "--value"])
        if code != 0 or out.strip() != "yes":
            raise LaunchError("saat eşitlenmemiş (timedatectl NTPSynchronized=%s): NTP eşitlemesi kurulmalı ve beklenmeli"
                              % (out.strip() or err.strip()[-200:] or "?"))
        code, out, _ = self.shell.run(["chronyc", "-n", "tracking"])
        offset = None
        if code == 0:
            match = re.search(r"^System time\s*:\s*([0-9.]+) seconds (fast|slow) of NTP time", out, re.M)
            if match:
                offset = float(match.group(1))
        if offset is None:
            code, out, _ = self.shell.run(["timedatectl", "timesync-status"])
            match = re.search(r"^\s*Offset:\s*([+-]?[0-9.]+)(us|µs|μs|ms|s)\s*$", out, re.M) if code == 0 else None
            if match:
                unit = {"ms": 1e-3, "s": 1.0}.get(match.group(2), 1e-6)
                offset = abs(float(match.group(1))) * unit
        if offset is None:
            raise LaunchError("saat sapması ölçülemedi: chronyc tracking ya da timedatectl timesync-status okunamadı")
        if not offset < MAX_CLOCK_OFFSET:
            raise LaunchError("saat sapması %.3f sn; %.1f sn'nin altında olmalı" % (offset, MAX_CLOCK_OFFSET))
        return offset

    def binary(self):
        return self.state_dir / "qbtc"

    def current_attempt(self, state, wanted):
        """Son denemeyi döndürür; durumu wanted içinde değilse hata verir."""
        if not state["denemeler"]:
            raise LaunchError("henüz deneme yok: önce kaz")
        attempt = state["denemeler"][-1]
        if attempt["durum"] not in wanted:
            raise LaunchError("son denemenin durumu %r; bu adım için %s olmalı" % (attempt["durum"], " ya da ".join(wanted)))
        return attempt

    def fetch(self, url):
        try:
            return self.shell.fetch(url).strip()
        except Exception as e:  # ağ hatası, zaman aşımı, HTTP hatası
            raise LaunchError("%s alınamadı: %s" % (url, e))

    def bitcoin_header(self, text, label):
        try:
            return self.gv.bitcoin_header(text, self.rules["mainnet"].bitcoin_max_target, label)
        except self.gv.VectorError as e:
            raise LaunchError(str(e))

    def check_bitcoin_chain(self, block_hex, confirmation_hex):
        """Mesajdaki bloğun iki bağımsız kaynakta da en çok iş taşıyan zincirde olduğunu ve onaylayan
        bloğun onun üstünde durduğunu denetler. Başlığın iş ispatı bunu göstermez: bayat (yetim) bir
        blok da geçerli iş ispatı taşır."""
        block = self.bitcoin_header(block_hex, "mesajdaki Bitcoin başlığı")
        confirmation = self.bitcoin_header(confirmation_hex, "onaylayan Bitcoin başlığı")
        if confirmation.prev != block.hash:
            raise LaunchError("onaylayan Bitcoin başlığı mesajdaki bloğa bağlanmıyor")
        heights = set()
        for source in BITCOIN_SOURCES:
            if self.fetch("%s/block/%s/header" % (source, block.hash)) != block_hex:
                raise LaunchError("%s mesajdaki blok için başka bir başlık veriyor: %s" % (source, block.hash))
            try:
                status = json.loads(self.fetch("%s/block/%s/status" % (source, block.hash)))
                in_best_chain, next_best, height = status["in_best_chain"], status.get("next_best"), status.get("height")
            except (ValueError, KeyError, TypeError) as e:
                raise LaunchError("%s blok durumunu anlaşılır biçimde vermedi: %s" % (source, e))
            if in_best_chain is not True:
                raise LaunchError("%s: mesajdaki blok %s en çok iş taşıyan zincirde değil (bayat blok)" % (source, block.hash))
            if next_best != confirmation.hash:
                raise LaunchError("%s: mesajdaki bloğun üstündeki blok %s; verilen onaylayan blok %s"
                                  % (source, next_best, confirmation.hash))
            if type(height) is not int or height <= 0:
                raise LaunchError("%s blok yüksekliğini vermedi" % source)
            heights.add(height)
        if len(heights) != 1:
            raise LaunchError("kaynaklar mesajdaki blok için farklı yükseklik veriyor: %s" % sorted(heights))
        return {"hash": block.hash, "time": block.time, "height": heights.pop(), "confirmation_hash": confirmation.hash,
                "confirmation_time": confirmation.time}

    def remote(self, second, command):
        """İkinci makinede bu betiğin bir komutunu ssh ile çalıştırır; çıktısını döndürür."""
        parts = ["python3", "-I", "scripts/lansman.py", command, "--durum", second["durum"], "--taban", self.base_tag,
                 "--etiket", self.tag, "--anahtar", self.key]
        line = "cd %s && %s" % (shlex.quote(second["dizin"]), " ".join(shlex.quote(p) for p in parts))
        return self.run(["ssh"] + list(SSH_OPTIONS) + [second["ssh"], line], what="ikinci makinede %s" % command, live=True)

    def push_url(self, remote):
        """İtmenin gideceği tek adres. ls-remote ile itme aynı depoya bakmalıdır: pushurl tanımlıysa o
        kullanılır; birden çok adres varsa (atomik olmayan çoklu itme) durulur."""
        urls = self.git("remote", "get-url", "--push", "--all", remote).splitlines()
        if len(urls) != 1 or not urls[0].strip():
            raise LaunchError("%s uzağının tek bir itme adresi olmalı: %s" % (remote, urls))
        return urls[0].strip()

    def remote_refs(self, remote, branch):
        """Uzak depoda dalın ve lansman etiketinin gösterdiği commit'ler: (dal, etiketin commit'i,
        etiket nesnesi); olmayan None."""
        names = ("refs/heads/" + branch, "refs/tags/" + self.tag + "^{}", "refs/tags/" + self.tag)
        refs = {}
        for line in self.git(*GIT_NET, "ls-remote", remote, *names).splitlines():
            sha, _, name = line.partition("\t")
            refs[name.strip()] = sha.strip()
        return tuple(refs.get(name) for name in names)

    def check_age(self, t_g, what):
        age = self.shell.now() - t_g
        if age > PUBLISH_LIMIT - PUSH_MARGIN:
            raise LaunchError("%s: T_g'den bu yana %d sn geçti; yayın eşiği %d sn (pay %d sn). Hiçbir şey itilmedi: denemeyi atın"
                              % (what, age, PUBLISH_LIMIT, PUSH_MARGIN))
        if age < 0:
            raise LaunchError("%s: T_g bu makinenin saatinden %d sn ileride" % (what, -age))
        return age

    # --- komutlar ---

    def basliklar(self):
        """Uç bloğu ve ebeveynini iki kaynaktan alır; iki kaynak aynı başlıkları vermelidir."""
        seen = []
        for source in BITCOIN_SOURCES:
            tip = self.fetch(source + "/blocks/tip/hash")
            confirmation_hex = self.fetch("%s/block/%s/header" % (source, tip))
            confirmation = self.bitcoin_header(confirmation_hex, "uç blok (%s)" % source)
            if confirmation.hash != tip:
                raise LaunchError("%s uç blok için özeti tutmayan bir başlık verdi" % source)
            block_hex = self.fetch("%s/block/%s/header" % (source, confirmation.prev))
            if self.bitcoin_header(block_hex, "uç bloğun ebeveyni (%s)" % source).hash != confirmation.prev:
                raise LaunchError("%s ebeveyn için özeti tutmayan bir başlık verdi" % source)
            seen.append((block_hex, confirmation_hex))
        if len(set(seen)) != 1:
            raise LaunchError("iki kaynak farklı uç blok gösteriyor (yeni blok yayılıyor olabilir); birkaç saniye sonra yeniden deneyin")
        block_hex, confirmation_hex = seen[0]
        chain = self.check_bitcoin_chain(block_hex, confirmation_hex)
        now = self.shell.now()
        say("mesaja girecek blok (bir onaylı): %s, yükseklik %d, zamanı %s (%d sn önce)"
            % (chain["hash"], chain["height"], iso(chain["time"]), now - chain["time"]))
        say("onaylayan uç blok: %s, zamanı %s (%d sn önce)"
            % (chain["confirmation_hash"], iso(chain["confirmation_time"]), now - chain["confirmation_time"]))
        say("--blok %s" % block_hex)
        say("--onay %s" % confirmation_hex)
        return block_hex, confirmation_hex

    def hazir(self, tags=()):
        """İki makinede de: saat, temiz ağaç, imzalı taban etiketi, derleme ve bütün testler."""
        base = self.begin()
        started = self.shell.now()
        offset = self.check_clock_sync()
        self.check_tree(base)
        self.state_dir.mkdir(parents=True, exist_ok=True)
        build = ["go", "build"] + (["-tags", ",".join(tags)] if tags else []) + ["-o", self.binary(), "."]
        self.run(build, what="derleme (go build)", live=True)
        self.run(["go", "test", "-count=1", "./..."], what="testler (go test ./...)", live=True)
        self.check_tree(base)
        say("hazır: saat sapması %.3f sn, ağaç temiz, taban %s (%s), derleme ve testler geçti (%d sn)"
            % (offset, self.base_tag, base, self.shell.now() - started))

    def on_ucus(self, block, confirmation, report, second):
        """Lansman makinesinde ön-uçuş; ikinci makinenin hazır olduğu da görülür."""
        self.hazir(tags=("opencl",))
        self.check_bitcoin_chain(block, confirmation)
        self.run([self.binary(), "-network", "mainnet", "-launch-preflight", "-launch-block", block,
                  "-launch-confirmation", confirmation, "-launch-report", pathlib.Path(report).resolve()],
                 what="aracın ön-uçuşu (qbtc -launch-preflight)", live=True)
        self.check_tree(self.begin())
        self.remote(second, "hazir")
        say("ön-uçuş geçti: lansman makinesi ve ikinci makine hazır")

    def kaz(self, block, confirmation, report):
        """Genesis'i kazar ve adayı kurar: iki veri dosyası, imzasız commit, koruma. Etiket atılmaz."""
        base = self.begin()
        state = self.load_state()
        if state["yayin"]:
            raise LaunchError("lansman zaten yayımlandı")
        if state["denemeler"] and state["denemeler"][-1]["durum"] != "atildi":
            raise LaunchError("önceki deneme atılmadan yenisi başlatılamaz: önce at")
        self.check_tree(base)
        if not self.binary().exists():
            raise LaunchError("ikili yok (%s): önce on-ucus" % self.binary())
        report = pathlib.Path(report).resolve()
        with open(str(report), "rb") as f:
            report_sum = hashlib.sha256(f.read()).hexdigest()
        chain = self.check_bitcoin_chain(block, confirmation)
        number = len(state["denemeler"]) + 1
        attempt_dir = self.state_dir / ("deneme-%d" % number)
        attempt_dir.mkdir(parents=True)
        record_path = attempt_dir / "qbtc-genesis.json"
        attempt = {"no": number, "durum": "araniyor", "basladi": self.shell.now(), "bitcoin_basliklari": [block, confirmation],
                   "bitcoin": chain["hash"], "t_b": chain["time"], "bitcoin_yukseklik": chain["height"]}
        state["denemeler"].append(attempt)
        self.save_state(state)

        code, out, err = self.shell.run([self.binary(), "-network", "mainnet", "-mine-genesis", "-launch-block", block,
                                         "-launch-confirmation", confirmation, "-launch-report", report,
                                         "-launch-record", record_path], self.repo, True)
        log_path = attempt_dir / "arac-gunlugu.txt"
        log_path.write_text(out + "\n" + err, encoding="utf-8")
        if code != 0:
            # Araç genesis bulmadan ya da bulduğunu kullanmadan durdu; günlüğü saklanır, deneme atılır.
            attempt.update(durum="arac-durdu", cikis_kodu=code)
            self.save_state(state)
            raise LaunchError("genesis kazım aracı durdu (çıkış %d); günlük: %s\n%s\nDenemeyi 'at' ile kapatın."
                              % (code, log_path, (out + "\n" + err).strip()[-1200:]))
        with open(str(record_path), encoding="utf-8") as f:
            record = json.load(f)
        genesis, t_g = record.get("genesis_hash"), record.get("timestamp")
        if type(genesis) is not str or not re.fullmatch("[0-9a-f]{64}", genesis) or type(t_g) is not int:
            raise LaunchError("aracın kaydı okunamadı: %s" % record_path)
        attempt.update(durum="bulundu", kayit=record, genesis=genesis, T_g=t_g)
        self.save_state(state)
        say("genesis bulundu: %s (T_g %d, %s)" % (genesis, t_g, iso(t_g)))
        if record.get("commit") != base or record.get("network") != "mainnet" or record.get("report_sha256") != report_sum:
            raise LaunchError("aracın kaydı bu lansmana ait değil: commit %s (taban %s), ağ %s, rapor özeti %s (verilen rapor %s)"
                              % (record.get("commit"), base, record.get("network"), record.get("report_sha256"), report_sum))

        # Bağımsız betik (genesis_vektor.py uret ile aynı kod): vektörü lansman dosyasından üretir.
        try:
            launch_text = self.gv.read_text(str(self.repo / self.guard.LAUNCH_PATH))
            _, vector_text = self.gv.produce(launch_text, self.shell.now(), self.rules)
            self.gv.write_once(str(self.repo / self.guard.VECTOR_PATH), vector_text)
        except self.gv.VectorError as e:
            raise LaunchError("bağımsız vektör betiği lansman dosyasını kabul etmedi: %s" % e)
        self.run(["go", "test", "-count=1", "-run", LAUNCH_TESTS, "./core/", "."], what="lansman testleri (go test)", live=True)
        subject = self.guard.SUBJECT_PREFIX + genesis
        self.git("add", "--", self.guard.LAUNCH_PATH, self.guard.VECTOR_PATH)
        self.git("commit", "-q", "--no-gpg-sign", "-m", subject)
        commit = self.head()
        info = self.guard.check_launch_commit(str(self.repo), base, commit, self.networks)
        if (info["genesis_hash"], info["timestamp"], info["launch_file_sha256"]) != (genesis, t_g, record.get("launch_file_sha256")):
            raise LaunchError("commit'teki lansman dosyası aracın kaydındaki dosya değil")
        if info["bitcoin_headers"] != [block, confirmation]:
            raise LaunchError("commit'teki Bitcoin başlıkları verilen başlıklar değil")
        self.check_tree(commit)
        attempt.update(durum="hazir", commit=commit)
        self.save_state(state)
        say("aday hazır: commit %s (imzasız, etiketsiz); T_g'den bu yana %d sn (eşik %d sn). Sıradaki: yayimla"
            % (commit, self.shell.now() - t_g, PUBLISH_LIMIT))
        return attempt

    def dogrula(self):
        """İkinci makinede: adayı temiz bir klonda doğrular ve tek satırlık sonucu yazdırır. Aday henüz
        imzalanmamıştır: gerçek anahtarla imza ancak bu doğrulamadan sonra atılır."""
        base = self.begin()
        started = self.shell.now()
        offset = self.check_clock_sync()
        self.check_tree(base)  # doğrulayan kod, duyurulmuş taban commit'in kodudur
        self.state_dir.mkdir(parents=True, exist_ok=True)
        clone = self.state_dir / ("klon-%d" % started)
        if clone.exists():
            raise LaunchError("klon dizini zaten var: %s" % clone)
        self.git("clone", "-q", "--no-checkout", "--no-hardlinks", str(self.repo), str(clone), cwd=self.state_dir)
        self.git("fetch", "-q", "origin", "+%s:%s" % (CANDIDATE_REF, CANDIDATE_REF), cwd=clone)
        commit = self.git("rev-parse", "--verify", CANDIDATE_REF + "^{commit}", cwd=clone)
        info = self.guard.check_launch_commit(str(clone), base, commit, self.networks)
        ahead = info["timestamp"] - self.shell.now()
        if ahead > MAX_AHEAD:
            raise LaunchError("T_g bu makinenin saatinden %d sn ileride (en çok %d): iki makinenin saati tutmuyor" % (ahead, MAX_AHEAD))
        self.check_bitcoin_chain(*info["bitcoin_headers"])
        self.git("checkout", "-q", "--detach", commit, cwd=clone)
        self.run(["go", "build", "-o", self.state_dir / "qbtc-aday", "."], clone, "adayın derlenmesi (go build)", live=True)
        # Bütün testler: mainnet'in "tanımsız"dan "tanımlı"ya döndüğü ağaçta ilk tam koşu budur.
        self.run(["go", "test", "-count=1", "./..."], clone, "adayın testleri (go test ./...)", live=True)
        shutil.rmtree(str(clone), ignore_errors=True)
        now = self.shell.now()
        age = now - info["timestamp"]
        if age > PUBLISH_LIMIT - PUSH_MARGIN:
            raise LaunchError("T_g'den bu yana %d sn geçti; yayın eşiği %d sn (pay %d sn): deneme atılmalı"
                              % (age, PUBLISH_LIMIT, PUSH_MARGIN))
        say("%s commit=%s genesis=%s T_g=%d saat_farki=%d saat_sapmasi=%.3f sure=%d"
            % (VERIFIED_MARK, commit, info["genesis_hash"], info["timestamp"], age, offset, now - started))
        return {"commit": commit, "genesis": info["genesis_hash"], "saat_farki": age}

    def yayimla(self, second, remote="origin", branch="main"):
        """Adayı ikinci makinede doğrulatır; sonra etiketi imzalar, eşiği ve uzaktaki dalı denetler,
        dalı ve etiketi tek adımda iter. İkinci makine doğrulamadan gerçek anahtarla imza atılmaz."""
        base = self.begin()
        state = self.load_state()
        if state["yayin"]:
            raise LaunchError("lansman zaten yayımlandı")
        attempt = self.current_attempt(state, ("hazir",) + PUSH_STATES)
        commit, genesis, t_g = attempt["commit"], attempt["genesis"], attempt["T_g"]
        if attempt["durum"] in PUSH_STATES:
            # Önceki itme başladı ama sonucu kaydedilmedi (kesinti, bağlantı): yeniden doğrulama ve imza
            # yok, yalnızca sonuç uzak depodan okunur.
            return self.settle(state, attempt, base, None, None, "önceki itmenin sonucu yeniden okundu")
        url = self.push_url(remote)
        head, tag_commit, _ = self.remote_refs(url, branch)
        if head == commit and tag_commit == commit:
            # Durum dosyası bayat (kayıp yazım, eşzamanlı çalıştırma): aday zaten yayımlanmış. Yalnızca kaydedilir.
            attempt["itme"] = attempt.get("itme") or {"uzak": url, "dal": branch, "basladi": None}
            return self.settle(state, attempt, base, None, None, "aday uzak depoda zaten yayımlanmış bulundu")
        self.check_tree(commit)
        info = self.guard.check_launch_commit(str(self.repo), base, commit, self.networks)

        self.git(*GIT_NET, "push", "-q", "--no-follow-tags", candidate_url(second), "+%s:%s" % (commit, CANDIDATE_REF))
        out = self.remote(second, "dogrula")
        local_age = self.shell.now() - t_g
        match = re.search(r"^%s commit=(\S+) genesis=(\S+) T_g=(\d+) saat_farki=(-?\d+) saat_sapmasi=(\S+) sure=(\d+)\s*$"
                          % VERIFIED_MARK, out, re.M)
        if not match or match.group(1) != commit or match.group(2) != genesis or int(match.group(3)) != t_g:
            raise LaunchError("ikinci makine bu adayı doğrulamadı:\n%s" % out.strip()[-800:])
        remote_age = int(match.group(4))
        attempt["ikinci_makine"] = {"dogrulandi": True, "saat_farki": remote_age, "bu_makinede": local_age,
                                    "saat_sapmasi": match.group(5), "sure": int(match.group(6))}
        self.save_state(state)
        if abs(local_age - remote_age) > MAX_CLOCK_DISAGREEMENT:
            raise LaunchError("iki makinenin saati tutmuyor: T_g'ye uzaklık burada %d sn, ikinci makinede %d sn (en çok %d sn fark)"
                              % (local_age, remote_age, MAX_CLOCK_DISAGREEMENT))

        # İmzadan önce: eşik, ve uzaktaki dal hâlâ duyurulmuş taban commit'te mi, etiket yok mu.
        self.check_age(t_g, "imzadan önce")
        head, _, tag_object = self.remote_refs(url, branch)
        if head != base:
            raise LaunchError("uzaktaki %s dalı taban commit'te (%s) değil: %s" % (branch, base, head))
        if tag_object is not None:
            raise LaunchError("uzakta %s etiketi zaten var" % self.tag)
        # Yarıda kesilmiş bir önceki çalıştırmadan kalan yerel etiket yayımlanmamıştır (uzakta yok, dal
        # tabanda): silinir ve yeniden imzalanır.
        self.shell.run(["git", "tag", "-d", self.tag], self.repo)
        say("etiket imzalanıyor (%s): belirteç onay istiyorsa şimdi verin" % self.tag)
        try:
            self.git("tag", "-s", "-m", info["subject"], self.tag, commit, live=True)
            self.guard.check_tag(str(self.repo), self.tag, commit, info["subject"], self.key)
            self.check_age(t_g, "imzadan sonra")
        except BaseException:  # Ctrl-C dahil: yayımlanmayan imzalı etiket yerelde bırakılmaz
            self.shell.run(["git", "tag", "-d", self.tag], self.repo)
            raise
        # İtmeden önce kaydedilir: bundan sonra kesinti olursa deneme atılamaz ve yeniden çalıştırma
        # yalnızca sonucu okur.
        attempt["durum"] = "itiliyor"
        attempt["itme"] = {"uzak": url, "dal": branch, "basladi": self.shell.now()}
        self.save_state(state)
        code, out, err = self.shell.run(["git"] + list(GIT_NET) + ["push", "--atomic", "--no-follow-tags", url,
                                         "%s:refs/heads/%s" % (commit, branch), "refs/tags/%s:refs/tags/%s" % (self.tag, self.tag)],
                                        self.repo, timeout=PUSH_TIMEOUT)
        attempt["itme"].update(bitti=self.shell.now(), cikis_kodu=code)
        self.save_state(state)
        return self.settle(state, attempt, base, code, attempt["itme"]["bitti"] if code == 0 else None,
                           (out + "\n" + err).strip()[-600:])

    def settle(self, state, attempt, base, code, finished, detail):
        """İtmenin sonucunu uzak depodan okur ve denemeyi ona göre kapatır. İtme komutunun çıkış kodu
        tek başına yetmez: bağlantı, uzak depo güncellendikten sonra da kopabilir. code itmenin çıkış
        kodudur (bu çalıştırmada itme yapılmadıysa None); finished, itme başarıyla döndüyse o andır.
        Deneme yalnızca itme açıkça reddedildiyse (kod 0 değil, zaman aşımı değil) ve uzak depo tabanda
        görünüyorsa yeniden "hazir" olur; öbür her durumda "belirsiz" kalır."""
        push, commit, t_g = attempt["itme"], attempt["commit"], attempt["T_g"]
        if finished is None and code is None and push.get("cikis_kodu") == 0:
            finished = push.get("bitti")  # önceki çalıştırmada itme başarıyla dönmüştü
        try:
            head, tag_commit, tag_object = self.remote_refs(push["uzak"], push["dal"])
        except LaunchError as e:
            attempt["durum"] = "belirsiz"
            self.save_state(state)
            raise LaunchError("itmenin sonucu uzak depodan okunamadı; imzalı etiket yerelde bırakıldı. Bağlantı gelince "
                              "yayimla yeniden çalıştırılır (yalnızca sonucu okur).\n%s\n%s" % (detail, e))
        if head == commit and tag_commit == commit:
            published = self.shell.now() if finished is None else finished
            attempt["durum"] = "yayimlandi"
            state["yayin"] = {"an": published, "itme_basladi": push.get("basladi"), "an_kesin": finished is not None,
                              "uzak": push["uzak"], "dal": push["dal"], "commit": commit, "T_g": t_g,
                              "yayin_eksi_T_g": published - t_g}
            self.save_state(state)
            say("YAYIMLANDI: %s (genesis %s); yayın anı %s, yayın anı − T_g = %d sn"
                % (commit, attempt["genesis"], iso(published), published - t_g))
            if finished is None:
                say("UYARI: itme komutunun sonucu okunamamıştı; yayın anı %s ile %s arasındadır, kayda üst sınır yazılır."
                    % (iso(push["basladi"]) if push.get("basladi") else "bilinmiyor", iso(published)))
            if published - t_g > PUBLISH_LIMIT:
                say("UYARI: yayın anı − T_g eşiği (%d sn) aşıldı; bu, lansman kaydına yazılır." % PUBLISH_LIMIT)
            return state["yayin"]
        if code is not None and code not in (0, TIMED_OUT) and head == base and tag_commit is None and tag_object is None:
            self.shell.run(["git", "tag", "-d", self.tag], self.repo)  # yayımlanmayan imzalı etiket yerelde bırakılmaz
            attempt["durum"] = "hazir"
            self.save_state(state)
            raise LaunchError("itme başarısız; hiçbir şey yayımlanmadı ve imzalı etiket silindi. Süre varsa yayimla yeniden "
                              "çalıştırılır, yoksa deneme atılır.\n%s" % detail)
        attempt["durum"] = "belirsiz"
        self.save_state(state)
        raise LaunchError("uzak depo beklenmeyen durumda: %s dalı %s, %s etiketi %s (beklenen ikisi de %s ya da dal %s ve etiket "
                          "yok). Elle inceleyin; imzalı etiket yerelde bırakıldı.\n%s"
                          % (push["dal"], head, self.tag, tag_commit or tag_object, commit, base, detail))

    def at(self, reason, remote="origin", branch="main"):
        """Yayımlanmamış son denemeyi atar; ağaç tabana döner. Atılan deneme kayıtta kalır. Uzak depo
        okunur: aday ya da etiket orada görünüyorsa (ya da okunamazsa) atılmaz."""
        base = self.begin()
        if not reason or not reason.strip():
            raise LaunchError("atma nedeni verilmeli")
        state = self.load_state()
        attempt = self.current_attempt(state, OPEN_STATES)
        head, tag_commit, tag_object = self.remote_refs(self.push_url(remote), branch)
        if (attempt.get("commit") and head == attempt["commit"]) or tag_commit or tag_object:
            raise LaunchError("uzak depoda %s dalı %s, %s etiketi %s: deneme yayımlanmış olabilir, atılmaz; yayimla ile sonucu kaydedin"
                              % (branch, head, self.tag, tag_commit or tag_object))
        record_path = self.state_dir / ("deneme-%d" % attempt["no"]) / "qbtc-genesis.json"
        if not attempt.get("kayit") and record_path.exists():
            # Araç kaydı yazıp sıfırdan farklı kodla çıktıysa ya da kesinti olduysa: bulunan genesis de kayda girer.
            try:
                with open(str(record_path), encoding="utf-8") as f:
                    record = json.load(f)
                attempt.update(kayit=record, genesis=record.get("genesis_hash"), T_g=record.get("timestamp"))
            except ValueError:
                attempt["kayit_okunamadi"] = str(record_path)
        allowed = {self.guard.LAUNCH_PATH, self.guard.VECTOR_PATH}
        changed = set(self.git("diff", "--name-only", "HEAD").splitlines())
        changed |= set(self.git("ls-files", "--others", "--exclude-standard").splitlines())
        head = self.head()
        if head != base:
            # Yalnızca tabanın hemen üstündeki, yayımlanmamış lansman commit'i geri alınır.
            if self.git("rev-list", "--parents", "-n", "1", head).split()[1:] != [base]:
                raise LaunchError("HEAD (%s) tabanın hemen üstünde değil; elle inceleyin" % head)
            changed |= set(self.git("diff", "--name-only", base, head).splitlines())
        extra = sorted(path for path in changed if path and path not in allowed)
        if extra:
            raise LaunchError("lansman dosyaları dışında değişiklik var; elle inceleyin: %s" % ", ".join(extra))
        code, _, _ = self.shell.run(["git", "show-ref", "--verify", "--quiet", "refs/tags/" + self.tag], self.repo)
        if code == 0:
            self.git("tag", "-d", self.tag)
        self.git("reset", "-q", "--hard", base)
        for leftover in (self.guard.VECTOR_PATH, self.guard.LAUNCH_PATH + ".yeni"):
            if (self.repo / leftover).exists():
                (self.repo / leftover).unlink()
        self.check_tree(base)
        attempt.update(durum="atildi", neden=reason.strip(), atildi=self.shell.now())
        self.save_state(state)
        say("deneme %d atıldı: %s" % (attempt["no"], reason.strip()))

    def kayit(self, first_seen, remote="origin"):
        """Lansman kaydını kurar, imzalı bir etiket olarak yazar ve iter."""
        self.begin()
        state = self.load_state()
        if not state["yayin"]:
            raise LaunchError("lansman yayımlanmadı: kayıt yayından sonra kurulur")
        attempt, publication = state["denemeler"][-1], state["yayin"]
        sources = {source for source, _ in first_seen}
        if len(sources) < 2 or len(sources) != len(first_seen):
            raise LaunchError("mesajdaki Bitcoin bloğunun ilk görülme zamanı iki ayrı bağımsız kaynaktan verilmeli")
        for source, when in first_seen:
            if not attempt["t_b"] - MAX_FIRST_SEEN_LEAD <= when <= publication["an"]:
                raise LaunchError("%s: ilk görülme zamanı %d, bloğun zamanı (%d) ile yayın anı (%d) arasında değil"
                                  % (source, when, attempt["t_b"], publication["an"]))
        earliest = min(when for _, when in first_seen)
        record = {
            "schema": 1,
            "network": "mainnet",
            "genesis_hash": attempt["genesis"],
            "launch_commit": attempt["commit"],
            "base_commit": state["taban"],
            "base_tag": self.base_tag,
            "tag": self.tag,
            "signing_key": self.key,
            "timestamp": attempt["T_g"],
            "bitcoin_hash": attempt["bitcoin"],
            "bitcoin_height": attempt["bitcoin_yukseklik"],
            "bitcoin_time": attempt["t_b"],
            "bitcoin_first_seen": [{"source": source, "time": when} for source, when in first_seen],
            "bitcoin_sources": list(BITCOIN_SOURCES),
            "publish_time": publication["an"],
            "publish_time_utc": iso(publication["an"]),
            "publish_time_exact": publication["an_kesin"],
            "push_started": publication["itme_basladi"],
            "publish_minus_genesis_seconds": publication["an"] - attempt["T_g"],
            "publish_minus_bitcoin_time_seconds": publication["an"] - attempt["t_b"],
            "publish_minus_first_seen_seconds": publication["an"] - earliest,
            "publish_limit_seconds": PUBLISH_LIMIT,
            "second_machine": attempt.get("ikinci_makine"),
            "tool_record": attempt["kayit"],
            "discarded_attempts": [
                {"number": a["no"], "state": a["durum"], "reason": a.get("neden"), "started": a.get("basladi"),
                 "discarded": a.get("atildi"), "bitcoin_hash": a.get("bitcoin"), "bitcoin_headers": a.get("bitcoin_basliklari"),
                 "genesis_hash": a.get("genesis"), "timestamp": a.get("T_g"), "tool_exit_code": a.get("cikis_kodu"),
                 "tool_record": a.get("kayit")}
                for a in state["denemeler"][:-1]
            ],
        }
        text = json.dumps(record, ensure_ascii=True, indent=2) + "\n"
        path = self.state_dir / RECORD_FILE
        with open(str(path), "w", encoding="ascii", newline="\n") as f:
            f.write(text)
        record_tag = self.tag + RECORD_SUFFIX
        code, _, _ = self.shell.run(["git", "show-ref", "--verify", "--quiet", "refs/tags/" + record_tag], self.repo)
        if code != 0:
            say("lansman kaydı imzalanıyor (%s): belirteç onay istiyorsa şimdi verin" % record_tag)
            self.git("tag", "-s", "--cleanup=verbatim", "-F", str(path), record_tag, attempt["commit"], live=True)
        record_oid = self.guard.resolve_tag(str(self.repo), record_tag)
        target, kind, message = self.guard.read_tag(str(self.repo), record_tag, record_oid)
        if target != attempt["commit"] or kind != "commit" or not message.startswith(text.encode("ascii")):
            raise LaunchError("%s etiketi bu kaydı taşımıyor; elle inceleyin" % record_tag)
        self.guard.verify_tag_signature(str(self.repo), record_tag, self.key, record_oid)
        self.git(*GIT_NET, "push", remote, "refs/tags/%s:refs/tags/%s" % (record_tag, record_tag))
        say("lansman kaydı %s etiketiyle imzalandı ve itildi; kopyası: %s" % (record_tag, path))
        return record


def parse_first_seen(values):
    result = []
    for value in values or ():
        source, sep, when = value.partition("=")
        if not sep or not source.strip() or not re.fullmatch("[0-9]{9,11}", when.strip()):
            raise LaunchError("--ilk-gorulme KAYNAK=UNIX_SANIYESI biçiminde olmalı: %r" % value)
        result.append((source.strip(), int(when)))
    return result


def main(argv=None, shell=None, repo=None, networks=None):
    """shell, repo ve networks yalnızca sınamalarda verilir; komut satırı kuralları gevşetemez."""
    for stream in (sys.stdout, sys.stderr):
        if hasattr(stream, "reconfigure"):
            stream.reconfigure(encoding="utf-8", errors="replace", newline="\n")
    parser = argparse.ArgumentParser(description="Q-BTC mainnet lansman betiği (docs/LANSMAN-KILAVUZU.md).")
    commands = parser.add_subparsers(dest="command", required=True)

    def command(name, help_text, common=True):
        sub = commands.add_parser(name, help=help_text)
        if common:
            sub.add_argument("--durum", required=True, metavar="DIZIN", help="durum dizini (deponun dışında)")
            sub.add_argument("--taban", required=True, metavar="ETIKET", help="lansman öncesi commit'i gösteren imzalı etiket")
            sub.add_argument("--etiket", required=True, metavar="AD", help="lansman etiketi")
            sub.add_argument("--anahtar", required=True, metavar="PARMAK_IZI", help="duyurulan imza anahtarının parmak izi")
        return sub

    def second_machine(sub):
        sub.add_argument("--ikinci-ssh", required=True, metavar="[KULLANICI@]MAKINE", help="ikinci makinenin ssh adresi")
        sub.add_argument("--ikinci-dizin", required=True, metavar="DIZIN", help="ikinci makinedeki depo dizini (mutlak yol)")
        sub.add_argument("--ikinci-durum", required=True, metavar="DIZIN", help="ikinci makinedeki durum dizini (mutlak yol)")

    def headers(sub):
        sub.add_argument("--blok", required=True, metavar="HEX", help="mesaja girecek Bitcoin bloğunun başlığı")
        sub.add_argument("--onay", required=True, metavar="HEX", help="onu onaylayan bloğun başlığı")
        sub.add_argument("--rapor", required=True, metavar="DOSYA", help="yayımlanan ölçüm raporu")

    def public_remote(sub):
        sub.add_argument("--uzak", default="origin", metavar="AD", help="herkese açık deponun git uzak adı (varsayılan origin)")

    command("basliklar", "Bitcoin'in uç bloğunu ve ebeveynini iki kaynaktan al", common=False)
    command("hazir", "ikinci makinede hazırlık denetimleri")
    sub = command("on-ucus", "lansman makinesinde ön-uçuş")
    headers(sub)
    second_machine(sub)
    headers(command("kaz", "genesis'i kaz ve adayı kur"))
    command("dogrula", "ikinci makinede adayı doğrula")
    sub = command("yayimla", "adayı doğrulat, imzala ve yayımla")
    second_machine(sub)
    public_remote(sub)
    sub.add_argument("--dal", default="main", metavar="AD", help="yayımlanacak dal (varsayılan main)")
    sub = command("at", "yayımlanmamış denemeyi at")
    sub.add_argument("--neden", required=True, metavar="METIN", help="denemenin atılma nedeni (kayda yazılır)")
    public_remote(sub)
    sub.add_argument("--dal", default="main", metavar="AD", help="yayın dalı (varsayılan main); aday orada görünüyorsa atılmaz")
    sub = command("kayit", "lansman kaydını kur, imzala ve it")
    sub.add_argument("--ilk-gorulme", action="append", metavar="KAYNAK=UNIX",
                     help="mesajdaki Bitcoin bloğunun bir kaynaktaki ilk görülme zamanı (en az iki ayrı kaynak)")
    public_remote(sub)
    args = parser.parse_args(argv)

    second = None
    if getattr(args, "ikinci_ssh", None) is not None:
        if not SSH_TARGET.fullmatch(args.ikinci_ssh):
            parser.error("--ikinci-ssh [KULLANICI@]MAKINE biçiminde olmalı")
        for value in (args.ikinci_dizin, args.ikinci_durum):
            if not REMOTE_PATH.fullmatch(value) or ".." in value:
                parser.error("--ikinci-dizin ve --ikinci-durum mutlak, yalın yollar olmalı: %r" % value)
        second = {"ssh": args.ikinci_ssh, "dizin": args.ikinci_dizin, "durum": args.ikinci_durum}
    for value in (getattr(args, "uzak", None), getattr(args, "dal", None)):
        if value is not None and not REF_NAME.fullmatch(value):
            parser.error("geçersiz uzak ya da dal adı: %r" % value)

    launch = Launch(shell or Shell(), repo or HERE.parent, getattr(args, "durum", None) or ".", getattr(args, "taban", None),
                    getattr(args, "etiket", None), getattr(args, "anahtar", None), networks)
    try:
        if args.command == "basliklar":
            launch.basliklar()
        elif args.command == "hazir":
            launch.hazir()
        elif args.command == "on-ucus":
            launch.on_ucus(args.blok, args.onay, args.rapor, second)
        elif args.command == "kaz":
            launch.kaz(args.blok, args.onay, args.rapor)
        elif args.command == "dogrula":
            launch.dogrula()
        elif args.command == "yayimla":
            launch.yayimla(second, args.uzak, args.dal)
        elif args.command == "at":
            launch.at(args.neden, args.uzak, args.dal)
        else:
            launch.kayit(parse_first_seen(args.ilk_gorulme), args.uzak)
    except (LaunchError, launch.guard.GuardError, OSError, ValueError, KeyError) as e:
        print("HATA: %s: %s" % (args.command, e), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
