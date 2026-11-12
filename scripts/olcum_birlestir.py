"""Ölçüm parçalarını tek rapora birleştirir ve doğrular (scripts/gpu-olcum.sh çağırır).

Kullanım: python3 -I olcum_birlestir.py CIKTI GPU_JSON CPU_JSON AB_JSON "UNAME -SR" LIBOQS_SURUMU

Doğrulama başarısızsa rapor yazılmaz ve çıkış kodu 1 olur: yanlış biçimli ya da anlamsız bir
sayı sessizce yayımlanmasın.
"""

import json
import math
import re
import sys

HEX_BITS = re.compile(r"^0x[0-9a-f]{8}$")


def fail(msg):
    print("HATA: " + msg, file=sys.stderr)
    sys.exit(1)


def load(path):
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError) as e:
        fail("%s okunamadı: %s" % (path, e))


def positive(name, v):
    if not isinstance(v, (int, float)) or isinstance(v, bool) or not math.isfinite(v) or v <= 0:
        fail("%s pozitif sonlu bir sayı değil: %r" % (name, v))


def check_bench(name, r, backend):
    if r.get("schema") != 1:
        fail("%s: bilinmeyen şema %r" % (name, r.get("schema")))
    if r.get("backend") != backend:
        fail("%s: arka uç %r, beklenen %r" % (name, r.get("backend"), backend))
    runs = r.get("runs_hash_per_sec")
    if not isinstance(runs, list) or not runs:
        fail("%s: ölçüm listesi boş" % name)
    for i, v in enumerate(runs):
        positive("%s ölçüm %d" % (name, i), v)
    for k in ("median_hash_per_sec", "min_hash_per_sec", "max_hash_per_sec"):
        positive("%s %s" % (name, k), r.get(k))
    if not r["min_hash_per_sec"] <= r["median_hash_per_sec"] <= r["max_hash_per_sec"]:
        fail("%s: en düşük ≤ ortanca ≤ en yüksek değil" % name)
    for k in ("mainnet_start_bits", "mainnet_floor_bits"):
        if not HEX_BITS.match(str(r.get(k, ""))):
            fail("%s: %s 0x ile 8 hex hane değil: %r" % (name, k, r.get(k)))
    if not r.get("commit") or not r.get("reproduce"):
        fail("%s: commit ya da yeniden üretme komutu yok" % name)


def main():
    if len(sys.argv) != 7:
        fail("kullanım: olcum_birlestir.py CIKTI GPU_JSON CPU_JSON AB_JSON UNAME LIBOQS")
    out, gpu_path, cpu_path, ab_path, uname, liboqs = sys.argv[1:]
    gpu, cpu, ab = load(gpu_path), load(cpu_path), load(ab_path)

    check_bench("GPU", gpu, "gpu")
    check_bench("CPU", cpu, "cpu")
    dev = gpu.get("device")
    if not isinstance(dev, dict):
        fail("GPU raporunda aygıt bilgisi yok")
    if dev.get("type") != "GPU" and not gpu.get("non_gpu_allowed"):
        fail("aygıt türü %r, GPU değil ve izin verilmemiş" % dev.get("type"))
    if not re.match(r"^[0-9a-f]{64}$", str(gpu.get("kernel_sha256", ""))):
        fail("çekirdek özeti eksik")
    for k in ("loop_hash_per_sec", "unrolled_hash_per_sec", "ratio", "ratio_min", "ratio_max"):
        positive("A/B " + k, ab.get(k))
    if ab.get("device") != dev.get("index"):
        fail("A/B aygıtı %r, ölçülen aygıt %r" % (ab.get("device"), dev.get("index")))
    if gpu.get("commit") != cpu.get("commit"):
        fail("GPU ve CPU ölçümleri farklı commit'lerden")

    report = {
        "schema": 1,
        "not": "Q-BTC hashrate ölçümü (docs/GPU-OLCUM.md). Rakamlar ortanca; ilk ölçüm ısınma olarak atıldı.",
        "gpu_hashrate_mi": dev.get("type") == "GPU" and not gpu.get("non_gpu_allowed"),
        "temiz_agac": not (gpu.get("modified") or cpu.get("modified")),
        "kernel": uname,
        "liboqs": liboqs,
        "gpu": gpu,
        "cpu": cpu,
        "kernel_ab": ab,
        "kernel_ab_karar": "açılmış çekirdek kalır" if ab["ratio"] >= 1 else
        "oran 1'in altında: döngülü çekirdeğe dönülmeli (PR #29 karar kuralı)",
    }
    with open(out, "w", encoding="utf-8") as f:
        json.dump(report, f, ensure_ascii=False, indent=2)
        f.write("\n")
    print("GPU ortanca: %.4g hash/sn (yayılım %%%.1f), CPU ortanca: %.4g hash/sn, A/B oranı %.3f"
          % (gpu["median_hash_per_sec"], gpu.get("spread_pct", 0), cpu["median_hash_per_sec"], ab["ratio"]))
    print("Mainnet önerisi: başlangıç %s, taban %s" % (gpu["mainnet_start_bits"], gpu["mainnet_floor_bits"]))
    if not report["temiz_agac"]:
        print("UYARI: derlenen ağaçta commit edilmemiş değişiklik vardı; bu rapor yayımlanmamalı, temiz commit'te yeniden ölçün.")
    if gpu.get("spread_pct", 0) > 10:
        print("UYARI: GPU ölçümleri arasında yayılım %%%.1f; makineyi boşaltıp yeniden ölçün." % gpu["spread_pct"])


if __name__ == "__main__":
    main()
