#!/usr/bin/env bash
# Q-BTC GPU ve CPU hashrate ölçümü (docs/GPU-OLCUM.md, docs/MAINNET-ONCESI.md madde 6a).
#
# Kullanım (depo kökünden ya da herhangi bir yerden):
#   bash scripts/gpu-olcum.sh
#
# Ortam değişkenleri (hepsi isteğe bağlı):
#   QBTC_GPU_DEVICE        ölçülecek OpenCL aygıtı (varsayılan 0; -gpu-list çıktısına bakın)
#   QBTC_OLCUM_SURE        tek ölçümün süresi (varsayılan 60s)
#   QBTC_OLCUM_TEKRAR      ölçüm sayısı, ısınma hariç (varsayılan 5)
#   QBTC_OLCUM_CIKTI       rapor dosyası (varsayılan ./qbtc-olcum-<UTC zaman>.json)
#   QBTC_OLCUM_IZIN_CPU    1 ise GPU olmayan OpenCL aygıtına izin verilir (yalnızca CI/pocl;
#                          sonuç GPU hashrate'i DEĞİLDİR ve raporda öyle işaretlenir)
#
# Betik sistem paketi kurmaz; yalnızca Go modüllerini (go.sum ile doğrulanarak) indirir ve eksik
# önkoşulda Türkçe ipucuyla durur. Rapor
# makine adı, kullanıcı adı ya da yol içermez; kamuya açıklanabilir.
set -euo pipefail

DEV="${QBTC_GPU_DEVICE:-0}"
SURE="${QBTC_OLCUM_SURE:-60s}"
TEKRAR="${QBTC_OLCUM_TEKRAR:-5}"
CIKTI="${QBTC_OLCUM_CIKTI:-$PWD/qbtc-olcum-$(date -u +%Y%m%dT%H%M%SZ).json}"
IZIN_CPU="${QBTC_OLCUM_IZIN_CPU:-}"
case "$CIKTI" in /*) ;; *) CIKTI="$PWD/$CIKTI" ;; esac # depo köküne geçmeden önce mutlak yap

hata() {
	echo "HATA: $*" >&2
	exit 1
}

# --- Önkoşullar (yalnızca denetim) ---
[ "$(uname -s)" = "Linux" ] || hata "yalnızca Linux desteklenir (yerel kurulum, üretici OpenCL sürücüsüyle); Windows/WSL ve macOS desteklenmez."
command -v go >/dev/null || hata "Go bulunamadı. https://go.dev/dl adresinden go.mod'daki sürümü kurun."
command -v gcc >/dev/null || hata "gcc bulunamadı (cgo için). Ubuntu/Debian: sudo apt install build-essential"
command -v python3 >/dev/null || hata "python3 bulunamadı. Ubuntu/Debian: sudo apt install python3"
command -v pkg-config >/dev/null || hata "pkg-config bulunamadı. Ubuntu/Debian: sudo apt install pkg-config"
[ -f /usr/local/include/oqs/oqs.h ] || hata "liboqs /usr/local altında yok. docs/GPU-OLCUM.md'deki liboqs 0.15.0 derleme adımlarını izleyin."
[ -f /usr/include/CL/cl.h ] || [ -f /usr/local/include/CL/cl.h ] ||
	hata "OpenCL başlıkları yok. Ubuntu/Debian: sudo apt install ocl-icd-opencl-dev opencl-headers"
# ldconfig Debian'da kullanıcı PATH'inde olmayabilir; çıktı değişkene alınır (pipefail altında
# grep -q'nun erken çıkışı SIGPIPE ile yanlış hata vermesin).
KUTUPHANELER="$( (ldconfig -p || /sbin/ldconfig -p) 2>/dev/null || true)"
case "$KUTUPHANELER" in
*libOpenCL.so*) ;;
*) hata "OpenCL ICD yükleyicisi (libOpenCL.so) yok. Ubuntu/Debian: sudo apt install ocl-icd-libopencl1" ;;
esac
case "$DEV" in '' | *[!0-9]*) hata "QBTC_GPU_DEVICE negatif olmayan bir tamsayı olmalı: $DEV" ;; esac
case "$TEKRAR" in '' | *[!0-9]*) hata "QBTC_OLCUM_TEKRAR pozitif bir tamsayı olmalı: $TEKRAR" ;; esac

KOK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$KOK"
GECICI="$(mktemp -d)"
trap 'rm -rf "$GECICI"' EXIT
echo "== Go modülleri (go.sum ile doğrulanarak GOPROXY'den; sistem paketi kurulmaz)"
go mod download
OQSGO="$(go list -m -f '{{.Dir}}' github.com/open-quantum-safe/liboqs-go)"
[ -n "$OQSGO" ] && [ -d "$OQSGO/.config" ] || hata "liboqs-go modül dizini bulunamadı: go mod download başarısız mı?"
export PKG_CONFIG_PATH="$OQSGO/.config:/usr/local/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"

echo "== Derleme (-tags opencl)"
go build -tags opencl -o "$GECICI/qbtc" .

echo "== OpenCL aygıtları"
"$GECICI/qbtc" -gpu-list

izin=()
if [ -n "$IZIN_CPU" ]; then
	izin=(-bench-allow-non-gpu)
	echo "UYARI: QBTC_OLCUM_IZIN_CPU ayarlı; GPU olmayan aygıtın sonucu GPU hashrate'i değildir."
fi

echo "== GPU ölçümü: aygıt $DEV, 1 ısınma + $TEKRAR × $SURE"
"$GECICI/qbtc" -network testnet -bench-hashrate "$SURE" -bench-runs "$TEKRAR" \
	-mine-gpu -gpu-device "$DEV" "${izin[@]}" -bench-out "$GECICI/gpu.json"

echo "== CPU ölçümü: tüm çekirdekler, 1 ısınma + $TEKRAR × $SURE"
"$GECICI/qbtc" -network testnet -bench-hashrate "$SURE" -bench-runs "$TEKRAR" -bench-out "$GECICI/cpu.json"

echo "== Çekirdek A/B (döngülü / açılmış, aynı aygıt)"
QBTC_KERNEL_AB=1 QBTC_REQUIRE_OPENCL=1 QBTC_GPU_DEVICE="$DEV" QBTC_KERNEL_AB_OUT="$GECICI/ab.json" \
	go test -tags opencl -count=1 -run '^TestKernelAB$' -v ./gpu/

LIBOQS="$(pkg-config --modversion liboqs 2>/dev/null || echo bilinmiyor)"
python3 -I scripts/olcum_birlestir.py "$CIKTI" "$GECICI/gpu.json" "$GECICI/cpu.json" "$GECICI/ab.json" \
	"$(uname -sr)" "$LIBOQS"
echo "== Rapor: $CIKTI"
echo "Bu dosyayı gönderin. Makine adı, kullanıcı adı ya da yol içermez."
