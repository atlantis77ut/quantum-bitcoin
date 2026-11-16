#!/usr/bin/env bash
# Blok yayılım ölçüm matrisi (docs/YAYILIM-OLCUM.md "Önerilen matris" ve "Kalibrasyon").
# Her hücre ayrı bir JSON rapor üretir; bir hücrenin başarısızlığı ötekileri durdurmaz, hata
# kodu durum dosyasına yazılır. Ağ değerleri benzetimdir, gerçek ağ ölçümü değildir.
#
# Ortam değişkenleri:
#   QBTC_BIN       derlenmiş düğüm (varsayılan ./qbtc)
#   QBTC_OUT       rapor dizini (varsayılan ./yayilim)
#   QBTC_RUNS      hücre başına ölçüm (varsayılan 5)
#   QBTC_PROFILE   full (önerilen matris), quick (yalnızca 20 Mbit/sn, 50 ms; denemelik) ya da
#                  hub (yalnızca star-hub 8 yaprak, 2 ve 10 Mbit/sn, 50 ms; itme yuvası denetimi)
# Çıkış: bütün hücreler 0 döndüyse 0, yoksa 1 (ulaşılmayan ölçüm hata değildir, özette görünür).
set -u

BIN=${QBTC_BIN:-./qbtc}
OUT=${QBTC_OUT:-./yayilim}
RUNS=${QBTC_RUNS:-5}
PROFILE=${QBTC_PROFILE:-full}
mkdir -p "$OUT"
STATUS="$OUT/durum.tsv"
: >"$STATUS"

# run <ad> <bayraklar...>: tek hücre; rapor $OUT/<ad>.json, log $OUT/<ad>.log.
run() {
	local name=$1
	shift
	echo "== $name: $*"
	local start=$SECONDS
	"$BIN" -bench-propagation -bench-out "$OUT/$name.json" "$@" >"$OUT/$name.log" 2>&1
	local rc=$?
	printf '%s\t%s\t%s\t%s\n' "$name" "$rc" "$((SECONDS - start))" "$*" >>"$STATUS"
	echo "   çıkış $rc, $((SECONDS - start)) sn"
}

# Kalibrasyon: 0,3 Mbit/sn'de dolu blok (~1,8 MB) alıcının asgari hızının (128 KiB/sn) çok
# altında kalır, ~14 sn'de kesilir (ulaşmamalı); 2 Mbit/sn'de ~7,3 sn'de ulaşmalı (8·S/R + 3·L).
# Tek ölçüm yeter.
run kalibrasyon-0.3mbit -bench-runs 1 -bench-prop-hops 1 -bench-prop-mbit 0.3 -bench-prop-latency 25ms
run kalibrasyon-2mbit -bench-runs 1 -bench-prop-hops 1 -bench-prop-mbit 2 -bench-prop-latency 25ms

if [ "$PROFILE" = hub ]; then
	for m in 2 10; do
		run "hub8-${m}mbit-50ms" -bench-runs "$RUNS" -bench-prop-topology star-hub -bench-prop-hops 8 \
			-bench-prop-mbit "$m" -bench-prop-latency 50ms
	done
	if awk -F'\t' '$2 != 0 { bad = 1 } END { exit !bad }' "$STATUS"; then
		exit 1
	fi
	exit 0
fi

if [ "$PROFILE" = quick ]; then
	mbits="20"
	lats="50"
else
	mbits="2 10 20 50 100"
	lats="25 50 75"
fi

for m in $mbits; do
	for l in $lats; do
		run "line3-${m}mbit-${l}ms" -bench-runs "$RUNS" -bench-prop-topology line -bench-prop-hops 3 \
			-bench-prop-mbit "$m" -bench-prop-latency "${l}ms"
		run "hub8-${m}mbit-${l}ms" -bench-runs "$RUNS" -bench-prop-topology star-hub -bench-prop-hops 8 \
			-bench-prop-mbit "$m" -bench-prop-latency "${l}ms"
	done
done

# star-leaf (gömülü liste işletmecisinin yarış avantajı) ve blok doluluğu karşılaştırması.
for m in 20 100; do
	run "leaf8-${m}mbit-50ms" -bench-runs "$RUNS" -bench-prop-topology star-leaf -bench-prop-hops 8 \
		-bench-prop-mbit "$m" -bench-prop-latency 50ms
done
for txs in 100 0; do
	run "line3-20mbit-50ms-${txs}tx" -bench-runs "$RUNS" -bench-prop-topology line -bench-prop-hops 3 \
		-bench-prop-mbit 20 -bench-prop-latency 50ms -bench-prop-txs "$txs"
done
echo "bitti: $OUT"
# Bir hücre bile başarısızsa (çıkış kodu 0 değil) iş akışı kırmızı görünsün; özet yine yazılır.
if awk -F'\t' '$2 != 0 { bad = 1 } END { exit !bad }' "$STATUS"; then
	echo "başarısız hücre var: $STATUS"
	exit 1
fi
