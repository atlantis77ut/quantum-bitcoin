"""Lansman commit'inin mekanik koruması (docs/LANSMAN.md §4.2, docs/LANSMAN-KILAVUZU.md).

Lansman commit'i yalnızca veridir: core/mainnet.json (lansman dosyası) ve
core/testdata/mainnet_vector.json (bağımsız betiğin o dosyadan ürettiği vektör). Yayın anı − T_g
eşiği yüzünden bu commit ajan denetiminden ve PR'dan geçmez; yerine bu betik, lansmandan önce
denetlenmiş kodla, lansman makinesinde ve ikinci makinede şunları denetler:

  1. Depo sığ değildir; replace başvurusu ya da grafts dosyası yoktur.
  2. Commit'in tek ebeveyni vardır ve o, verilen taban commit'tir (günler önce duyurulmuş lansman
     öncesi commit).
  3. Tabana göre fark tam olarak iki girdidir: core/mainnet.json değişmiştir, core/testdata/
     mainnet_vector.json eklenmiştir; ikisi de olağan dosyadır (kip 100644). Başka yol, kip
     değişikliği, sembolik bağ ya da alt modül yoktur.
  4. Tabandaki lansman dosyası boş nesnedir ("{}" ve satır sonu).
  5. Yeni lansman dosyası kanoniktir (alan sırası, girinti, satır sonu) ve bağımsız betik
     (genesis_vektor.py), gerçek mainnet kurallarıyla, ondan commit'teki vektörü bayt bayt üretir.
  6. Commit mesajı tam olarak "launch: Q-BTC mainnet genesis <özet>" satırıdır.
  7. --etiket verilmişse etiket açıklamalıdır, bu commit'i gösterir, adı bir dal adıyla çakışmaz ve
     mesajı commit mesajıyla aynıdır. --anahtar verilmişse etiketin imzası doğrulanır ve imzalayan
     anahtarın parmak izi verilen parmak izidir.

Betik saate bakmaz ve Go kodunu çağırmaz; Go derlemesi, lansman testleri ve saat eşikleri lansman
betiğinin (lansman.py) işidir. Kuralları gevşeten bir seçenek yoktur.

Kullanım (depo kökünden):
  python3 -I scripts/lansman_commit_denetle.py --taban COMMIT [--commit COMMIT] \\
      [--etiket AD [--anahtar PARMAK_IZI]] [--repo DIZIN]

Çıkış kodu: 0 tamam; 1 koruma düştü; 2 kullanım hatası. Yalnızca standart kitaplık ve git kullanır.
"""

import argparse
import importlib.util
import os
import pathlib
import re
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent

LAUNCH_PATH = "core/mainnet.json"
VECTOR_PATH = "core/testdata/mainnet_vector.json"
EMPTY_LAUNCH = b"{}\n"
# Lansman dosyasının alanları, dosyadaki sırayla (core/launch.go launchFile).
LAUNCH_KEYS = ("network", "timestamp", "message", "bits", "pow_limit_bits", "nonce", "hash", "bitcoin_headers")
SUBJECT_PREFIX = "launch: Q-BTC mainnet genesis "
REGULAR_FILE = "100644"
ABSENT = "000000"


class GuardError(Exception):
    """Lansman commit'i korumadan geçemedi."""


def load_vector_module():
    """Bağımsız vektör betiğini bu dosyanın yanından yükler (denetlenen commit'ten değil)."""
    sys.dont_write_bytecode = True  # depoda scripts/__pycache__ kalmasın: izlenmeyen dosya ağacı kirletir
    spec = importlib.util.spec_from_file_location("genesis_vektor", str(HERE / "genesis_vektor.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def git(repo, *args, stdin=None, check=True):
    """git'i replace başvuruları kapalı ve ortamdaki depo yönlendirmeleri temizlenmiş olarak
    çalıştırır; (çıkış kodu, stdout, stderr) döner."""
    env = dict(os.environ)
    for name in ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR",
                 "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_GRAFT_FILE", "GIT_REPLACE_REF_BASE", "GIT_CONFIG",
                 "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"):
        env.pop(name, None)
    for name in list(env):
        if name.startswith(("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_")):
            env.pop(name)
    env["GIT_NO_REPLACE_OBJECTS"] = "1"
    env["LC_ALL"] = "C"
    r = subprocess.run(["git", "-C", str(repo), "--no-replace-objects"] + list(args), input=stdin,
                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
    if check and r.returncode != 0:
        raise GuardError("git %s başarısız (%d): %s" % (" ".join(args[:2]), r.returncode,
                                                         r.stderr.decode("utf-8", "replace").strip()))
    return r.returncode, r.stdout, r.stderr


def check_repository(repo):
    """Geçmişi olduğundan farklı gösterebilecek depo durumlarını reddeder."""
    _, out, _ = git(repo, "rev-parse", "--is-shallow-repository")
    if out.strip() != b"false":
        raise GuardError("depo sığ (shallow): ebeveyn zinciri doğrulanamaz, tam klon gerekir")
    _, out, _ = git(repo, "for-each-ref", "--format=%(refname)", "refs/replace")
    if out.strip():
        raise GuardError("depoda replace başvurusu var: %s" % out.decode("utf-8", "replace").split()[0])
    _, out, _ = git(repo, "rev-parse", "--git-path", "info/grafts")
    grafts = pathlib.Path(out.decode("utf-8").strip())
    if not grafts.is_absolute():
        grafts = pathlib.Path(repo) / grafts
    if grafts.exists():
        raise GuardError("depoda grafts dosyası var: %s" % grafts)


def resolve_commit(repo, rev):
    """Verilen adı commit özetine çevirir."""
    if not rev or rev.startswith("-"):
        raise GuardError("geçersiz commit adı: %r" % rev)
    code, out, _ = git(repo, "rev-parse", "--verify", "--quiet", "--end-of-options", rev + "^{commit}", check=False)
    sha = out.decode("ascii", "replace").strip()
    if code != 0 or not re.fullmatch("[0-9a-f]{40}([0-9a-f]{24})?", sha):
        raise GuardError("commit bulunamadı: %s" % rev)
    return sha


def split_object(raw):
    """Commit ya da etiket nesnesini başlık satırlarına ve mesaja ayırır. Devam satırları (boşlukla
    başlayan, ör. gpgsig) önceki başlığa aittir ve atlanır."""
    head, sep, message = raw.partition(b"\n\n")
    if not sep:
        raise GuardError("nesnede mesaj yok")
    headers = []
    for line in head.split(b"\n"):
        if line.startswith(b" "):
            continue
        key, _, value = line.partition(b" ")
        headers.append((key.decode("ascii", "replace"), value.decode("utf-8", "replace")))
    return headers, message


def read_blob(repo, sha, limit):
    _, size, _ = git(repo, "cat-file", "-s", sha)
    if int(size.strip() or b"0") > limit:
        raise GuardError("dosya %d bayttan büyük" % limit)
    _, out, _ = git(repo, "cat-file", "blob", sha)
    return out


def check_launch_commit(repo, base, commit="HEAD", networks=None):
    """Lansman commit'ini denetler; bilgi sözlüğü döner. networks yalnızca sınamalarda verilir
    (genesis hedefi gevşetilmiş kurallar); komut satırı her zaman gerçek kuralları kullanır."""
    gv = load_vector_module()
    if networks is None:
        networks = gv.NETWORKS
    check_repository(repo)
    base_sha = resolve_commit(repo, base)
    commit_sha = resolve_commit(repo, commit)

    _, raw, _ = git(repo, "cat-file", "commit", commit_sha)
    headers, message = split_object(raw)
    parents = [value for key, value in headers if key == "parent"]
    if len(parents) != 1:
        raise GuardError("lansman commit'inin tek ebeveyni olmalı, %d var" % len(parents))
    if parents[0] != base_sha:
        raise GuardError("lansman commit'inin ebeveyni %s; taban %s olmalı" % (parents[0], base_sha))

    _, out, _ = git(repo, "diff-tree", "-r", "-z", "--raw", "--no-renames", "--no-abbrev", "--ignore-submodules=none",
                    base_sha, commit_sha)
    fields = out.split(b"\0")
    if fields and fields[-1] == b"":
        fields.pop()
    if len(fields) % 2:
        raise GuardError("fark çıktısı çözülemedi")
    changes = {}
    for meta, path in zip(fields[0::2], fields[1::2]):
        parts = meta.decode("ascii", "replace").lstrip(":").split(" ")
        if len(parts) != 5:
            raise GuardError("fark çıktısı çözülemedi: %r" % meta)
        name = path.decode("utf-8", "replace")
        if name in changes:
            raise GuardError("fark çıktısında yinelenen yol: %s" % name)
        changes[name] = parts
    extra = sorted(set(changes) - {LAUNCH_PATH, VECTOR_PATH})
    if extra:
        raise GuardError("lansman commit'i yalnızca %s ve %s dosyalarına dokunabilir; fazladan: %s"
                         % (LAUNCH_PATH, VECTOR_PATH, ", ".join(extra)))
    for path, expected in ((LAUNCH_PATH, (REGULAR_FILE, REGULAR_FILE, "M")), (VECTOR_PATH, (ABSENT, REGULAR_FILE, "A"))):
        if path not in changes:
            raise GuardError("lansman commit'i %s dosyasını %s" % (path, "değiştirmiyor" if expected[2] == "M" else "eklemiyor"))
        old_mode, new_mode, old_sha, new_sha, status = changes[path]
        if (old_mode, new_mode, status) != expected:
            raise GuardError("%s: kip %s → %s, durum %s; beklenen kip %s → %s, durum %s (olağan dosya olmalı; %s)"
                             % (path, old_mode, new_mode, status, expected[0], expected[1], expected[2],
                                "tabanda var ve değişmiş olmalı" if expected[2] == "M" else "tabanda olmamalı, eklenmiş olmalı"))

    if read_blob(repo, changes[LAUNCH_PATH][2], gv.MAX_FILE_SIZE) != EMPTY_LAUNCH:
        raise GuardError("tabandaki %s boş lansman dosyası ({} ve satır sonu) değil" % LAUNCH_PATH)
    launch_raw = read_blob(repo, changes[LAUNCH_PATH][3], gv.MAX_FILE_SIZE)
    vector_raw = read_blob(repo, changes[VECTOR_PATH][3], gv.MAX_FILE_SIZE)
    try:
        launch_text, vector_text = launch_raw.decode("ascii"), vector_raw.decode("ascii")
    except UnicodeDecodeError:
        raise GuardError("lansman dosyası ya da vektör ASCII değil")
    if "\r" in launch_text or "\r" in vector_text:
        raise GuardError("lansman dosyasında ya da vektörde CR karakteri var; satır sonu LF olmalı")

    try:
        doc = gv.parse(launch_text)
        if set(doc) != set(LAUNCH_KEYS):
            raise GuardError("lansman dosyasının alanları tam olarak şunlar olmalı: %s" % ", ".join(LAUNCH_KEYS))
        if gv.serialize({key: doc[key] for key in LAUNCH_KEYS}) != launch_text:
            raise GuardError("lansman dosyası kanonik biçimde değil (alan sırası, girinti ya da satır sonu)")
        if doc["network"] != "mainnet":
            raise GuardError("lansman dosyası mainnet için değil: %r" % (doc["network"],))
        # Saat burada denetlenmez (now = T_g): koruma her makinede aynı sonucu verir. Saat eşikleri
        # lansman betiğindedir.
        vector, produced = gv.produce(launch_text, doc["timestamp"], networks)
        gv.check(vector_text, networks)
    except gv.VectorError as e:
        raise GuardError("lansman dosyası ya da vektör bağımsız betikten geçmedi: %s" % e)
    if produced != vector_text:
        raise GuardError("commit'teki vektör, bağımsız betiğin lansman dosyasından ürettiği vektör değil")

    subject = SUBJECT_PREFIX + vector["hash"]
    if message != (subject + "\n").encode("ascii"):
        raise GuardError("commit mesajı tam olarak %r satırı olmalı; bulunan %r" % (subject, message.decode("utf-8", "replace")))
    return {
        "launch_file_sha256": gv.sha256(launch_raw).hex(), "bitcoin_headers": list(vector["bitcoin_headers"]),
        "commit": commit_sha, "base": base_sha, "subject": subject, "genesis_hash": vector["hash"],
        "timestamp": vector["timestamp"], "nonce": vector["nonce"], "bits": vector["bits"],
        "pow_limit_bits": vector["pow_limit_bits"], "message": vector["message"],
        "bitcoin_hash": vector["bitcoin_hash"], "bitcoin_time": vector["bitcoin_time"],
        "bitcoin_confirmation_hash": vector["bitcoin_confirmation_hash"],
    }


def normalize_fingerprint(text):
    """Parmak izini karşılaştırılabilir biçime getirir: SSH için "SHA256:..." olduğu gibi, OpenPGP için
    boşluksuz büyük harf hex."""
    text = text.strip()
    if text.startswith("SHA256:"):
        return text
    compact = text.replace(" ", "").upper()
    if not re.fullmatch("[0-9A-F]{40}([0-9A-F]{24})?", compact):
        raise GuardError("parmak izi tanınmadı (SSH: SHA256:..., OpenPGP: 40 hex hane): %r" % text)
    return compact


def signature_fingerprints(output):
    """git verify-tag --raw çıktısından imzalayan anahtarın parmak izlerini çıkarır. OpenPGP'de
    VALIDSIG satırı imzalayan (alt) anahtarı ve birincil anahtarı verir; SSH'de tek parmak izi vardır."""
    found = set()
    for line in output.splitlines():
        if line.startswith("[GNUPG:] VALIDSIG "):
            parts = line.split()
            found.add(parts[2].upper())
            if len(parts) >= 12:
                found.add(parts[11].upper())
        match = re.match(r'^Good "git" signature for .* with \S+ key (SHA256:\S+)$', line)
        if match:
            found.add(match.group(1))
    return found


def resolve_tag(repo, tag):
    """Etiket başvurusunu bir kez nesne kimliğine çözer. Sonraki her okuma (tür, içerik, imza) bu kimlikle
    yapılır: başvuru okumalar arasında değiştirilse bile hepsi aynı nesneye bakar."""
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]*", tag or ""):
        raise GuardError("geçersiz etiket adı: %r" % tag)
    code, out, _ = git(repo, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/tags/" + tag, check=False)
    oid = out.decode("ascii", "replace").strip()
    if code != 0 or not re.fullmatch("[0-9a-f]{40}([0-9a-f]{24})?", oid):
        raise GuardError("etiket bulunamadı: %s" % tag)
    return oid


def read_tag(repo, tag, oid=None):
    """Açıklamalı etiketi okur; (gösterdiği nesne, türü, mesajı) döner. Etiket adı yalın olmalı, etiket
    nesnesindeki ad başvuru adıyla aynı olmalı ve aynı adda bir dal olmamalıdır. oid verilmişse başvuru
    yeniden çözülmez."""
    if oid is None:
        oid = resolve_tag(repo, tag)
    code, out, _ = git(repo, "cat-file", "-t", oid, check=False)
    if code != 0:
        raise GuardError("etiket bulunamadı: %s" % tag)
    if out.strip() != b"tag":
        raise GuardError("etiket %s açıklamalı (annotated) değil; imzalı etiket açıklamalı olmalı" % tag)
    _, raw, _ = git(repo, "cat-file", "tag", oid)
    headers, message = split_object(raw)
    fields = dict(headers)
    if fields.get("tag") != tag:
        raise GuardError("etiket nesnesindeki ad %r, başvuru adı %r" % (fields.get("tag"), tag))
    code, _, _ = git(repo, "show-ref", "--verify", "--quiet", "refs/heads/" + tag, check=False)
    if code == 0:
        raise GuardError("etiketle aynı adda bir dal var: %s" % tag)
    return fields.get("object"), fields.get("type"), message


def verify_tag_signature(repo, tag, key, oid=None):
    """Etiketin imzasını doğrular; imzalayan anahtar verilen parmak izi olmalıdır. Anahtarlıktaki
    başka bir anahtarın geçerli imzası yetmez. oid verilmişse o nesne doğrulanır."""
    wanted = normalize_fingerprint(key)
    if oid is None:
        oid = resolve_tag(repo, tag)
    code, out, err = git(repo, "verify-tag", "--raw", oid, check=False)
    text = (out + b"\n" + err).decode("utf-8", "replace")
    if code != 0:
        raise GuardError("etiket %s imzası doğrulanamadı: %s" % (tag, text.strip()[-400:]))
    found = signature_fingerprints(text)
    if wanted not in found:
        raise GuardError("etiket %s duyurulan anahtarla (%s) imzalanmamış; imzalayan: %s"
                         % (tag, wanted, ", ".join(sorted(found)) or "bilinmiyor"))
    return wanted


def check_base_tag(repo, tag, key):
    """Lansman öncesi commit'i gösteren, günler önce duyurulmuş etiketi denetler: açıklamalıdır, bir
    commit'i gösterir ve duyurulan anahtarla imzalıdır. Commit özetini döndürür. Taban böylece lansman
    günü elle yazılan bir özet değil, önceden imzalanmış bir etikettir."""
    check_repository(repo)
    oid = resolve_tag(repo, tag)
    target, kind, _ = read_tag(repo, tag, oid)
    if kind != "commit":
        raise GuardError("taban etiketi %s bir commit'i göstermiyor: %s" % (tag, kind))
    verify_tag_signature(repo, tag, key, oid)
    return resolve_commit(repo, target)


def check_tag(repo, tag, commit_sha, subject, key=None):
    """Lansman etiketini denetler: açıklamalıdır, commit'i gösterir, adı bir dalla çakışmaz, mesajı
    commit mesajıdır. key verilmişse imza doğrulanır ve imzalayan anahtar key'dir."""
    oid = resolve_tag(repo, tag)
    target, kind, message = read_tag(repo, tag, oid)
    if target != commit_sha or kind != "commit":
        raise GuardError("etiket %s lansman commit'ini (%s) göstermiyor: %s %s" % (tag, commit_sha, kind, target))
    first_line = message.split(b"\n", 1)[0].decode("utf-8", "replace")
    if first_line != subject:
        raise GuardError("etiket mesajının ilk satırı %r olmalı; bulunan %r" % (subject, first_line))
    info = {"tag": tag, "signed_by": None}
    if key is not None:
        info["signed_by"] = verify_tag_signature(repo, tag, key, oid)
    return info


def describe(info, tag_info=None):
    lines = [
        "lansman commit'i %s" % info["commit"],
        "  taban %s" % info["base"],
        "  genesis %s" % info["genesis_hash"],
        "  T_g %d, nonce %d, başlangıç hedefi %s, taban %s" % (info["timestamp"], info["nonce"], info["bits"],
                                                                 info["pow_limit_bits"]),
        "  Bitcoin bloğu %s (t_b %d), T_g - t_b = %d sn" % (info["bitcoin_hash"], info["bitcoin_time"],
                                                           info["timestamp"] - info["bitcoin_time"]),
        "  onaylayan Bitcoin bloğu %s" % info["bitcoin_confirmation_hash"],
    ]
    if tag_info is not None:
        lines.append("  etiket %s, imzalayan %s" % (tag_info["tag"], tag_info["signed_by"] or "doğrulanmadı"))
    return "\n".join(lines)


def main(argv=None):
    for stream in (sys.stdout, sys.stderr):
        stream.reconfigure(encoding="utf-8", errors="replace", newline="\n")
    parser = argparse.ArgumentParser(description="Lansman commit'inin mekanik koruması.")
    parser.add_argument("--repo", default=str(HERE.parent), help="depo dizini (varsayılan: bu betiğin deposu)")
    parser.add_argument("--taban", required=True, metavar="COMMIT", help="duyurulmuş lansman öncesi commit")
    parser.add_argument("--commit", default="HEAD", metavar="COMMIT", help="lansman commit'i (varsayılan HEAD)")
    parser.add_argument("--etiket", metavar="AD", help="lansman etiketi")
    parser.add_argument("--anahtar", metavar="PARMAK_IZI",
                        help="duyurulan imza anahtarının parmak izi; verilirse etiketin imzası doğrulanır")
    args = parser.parse_args(argv)
    if args.anahtar and not args.etiket:
        parser.error("--anahtar yalnızca --etiket ile verilir")
    try:
        info = check_launch_commit(args.repo, args.taban, args.commit)
        tag_info = None
        if args.etiket:
            tag_info = check_tag(args.repo, args.etiket, info["commit"], info["subject"], args.anahtar)
    except (GuardError, OSError) as e:
        print("HATA: lansman commit'i korumadan geçemedi: %s" % e, file=sys.stderr)
        return 1
    print("tamam: lansman commit'i korumadan geçti")
    print(describe(info, tag_info))
    return 0


if __name__ == "__main__":
    sys.exit(main())
