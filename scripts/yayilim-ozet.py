"""Yayılım ölçüm matrisinin özeti (scripts/yayilim-matris.sh çıktısından Markdown).

Kullanım: python3 -I scripts/yayilim-ozet.py <rapor dizini>
Hücre: en geç düğümün ortanca süresi (sn) ve en az ulaşılan ölçüm / ölçüm sayısı. Ağ değerleri
benzetimdir; karar bu özette verilmez (docs/YAYILIM-OLCUM.md).
"""
import json
import os
import sys


def load(d, name):
    p = os.path.join(d, name + ".json")
    if not os.path.exists(p):
        return None
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def cell(r):
    if r is None:
        return "yok"
    s = r["simulation"]
    runs = len(s["runs_ms"])
    reached = min(s["reached"]) if s["reached"] else 0
    if reached == 0:
        return f"ulaşmadı (0/{runs})"
    t = s["slowest_node_median_ms"] / 1000
    mark = "" if reached == runs else f" ({reached}/{runs})"
    return f"{t:.2f}{mark}"


def main():
    sys.stdout.reconfigure(encoding="utf-8")
    d = sys.argv[1]
    status = []
    if not os.path.exists(os.path.join(d, "durum.tsv")):
        print("# Yayılım ölçümü özeti\n\nÖlçüm çalışmadı (durum.tsv yok): derleme ya da girdi adımının logüna bakın.")
        return
    with open(os.path.join(d, "durum.tsv"), encoding="utf-8") as f:
        for line in f:
            name, rc, sec, args = line.rstrip("\n").split("\t", 3)
            status.append((name, int(rc), int(sec), args))
    out = []
    first = next((load(d, n) for n, rc, _, _ in status if rc == 0 and load(d, n)), None)
    out.append("# Yayılım ölçümü özeti\n")
    out.append("Ağ değerleri benzetimdir (proxy ile yukarı hat ve gecikme), gerçek ağ ölçümü değildir. "
               "Hücre: en geç düğümün ortanca süresi (sn); parantez, bloğun zaman aşımı içinde ulaştığı en az ölçüm sayısıdır.\n")
    if first:
        out.append(f"- Commit `{first['commit']}` (değiştirilmiş: {first['modified']}), {first['go_version']}, "
                   f"{first['os']}/{first['arch']}, {first['cpus']} CPU")
    for name in ("line3-20mbit-50ms", "line3-20mbit-50ms-100tx", "line3-20mbit-50ms-0tx"):
        r = load(d, name)
        if r:
            b = r["block"]
            out.append(f"- Blok ({b['txs']} işlem): {b['wire_bytes']} bayt, çözümleme ortanca {b['decode_ms_median']:.1f} ms, "
                       f"doğrulama ortanca {b['accept_ms_median']:.1f} ms")
    out.append("")

    out.append("## Kalibrasyon\n")
    for name, want in (("kalibrasyon-0.3mbit", "ulaşmamalı"), ("kalibrasyon-2mbit", "~7,3 sn'de ulaşmalı")):
        out.append(f"- {name}: {cell(load(d, name))} (beklenen: {want})")
    out.append("")

    grid = [n for n, _, _, _ in status if n.startswith(("line3-", "hub8-")) and n.count("-") == 2]
    mbits = sorted({float(n.split("-")[1][:-4]) for n in grid})
    lats = sorted({int(n.split("-")[2][:-2]) for n in grid})
    for topo, title in (("line3", "line, 3 atlama"), ("hub8", "star-hub, 8 yaprak")):
        if not any(n.startswith(topo + "-") for n in grid):
            continue  # bu profilde ızgara hücresi yok (ör. hub, doluluk)
        out.append(f"## {title}\n")
        out.append("| Mbit/sn \\ tek yön | " + " | ".join(f"{l} ms" for l in lats) + " |")
        out.append("|---" * (len(lats) + 1) + "|")
        for m in mbits:
            ms = f"{m:g}"
            out.append(f"| {ms} | " + " | ".join(cell(load(d, f"{topo}-{ms}mbit-{l}ms")) for l in lats) + " |")
        out.append("")

    hub = load(d, "hub8-10mbit-50ms")
    if hub:
        out.append("## Yaprak varışları (star-hub, 10 Mbit/sn, 50 ms, ilk ölçüm, sn)\n")
        out.append("İtme yuvası teslime kadar tutulduğundan yapraklar ikişer ikişer gelmelidir (~2,9 / 5,8 / 8,7 / 11,6 sn "
                   "ve yuva bekleme payı; docs/YAYILIM-OLCUM.md, \"İtme yuvası teslim ölçümü\").\n")
        arr = hub["simulation"]["runs_ms"][0]
        got = sorted(x for x in arr if x >= 0)
        line = ", ".join(f"{x / 1000:.2f}" for x in got) or "hiçbiri"
        if len(got) < len(arr):
            line += f" (ulaşmayan {len(arr) - len(got)} yaprak)"
        out.append("- " + line)
        out.append("")

    out.append("## Ek ölçümler\n")
    for name, _, _, _ in status:
        if name.startswith("leaf8-") or name.endswith("tx"):
            out.append(f"- {name}: {cell(load(d, name))}")
    out.append("")

    out.append("## Çalıştırma durumu\n")
    out.append("| Hücre | Çıkış | Süre (sn) |")
    out.append("|---|---|---|")
    for name, rc, sec, _ in status:
        out.append(f"| {name} | {rc} | {sec} |")
    print("\n".join(out))


if __name__ == "__main__":
    main()
