# GPU ve CPU hashrate ölçümü (madde 6a)

Bu ölçüm, mainnet başlangıç hedefini ve tabanını seçmek için gereken hashrate'i gerçek donanımda üretir ([MAINNET-ONCESI.md](MAINNET-ONCESI.md) madde 6a). CI yalnızca pocl'da, yani CPU üzerinde çalışır. Oradaki rakamlar GPU hashrate'i değildir.

## Ne gerekir

- **Linux**, yerel kurulum (Ubuntu/Debian hedeflenir; betik CI'da Ubuntu üzerinde pocl ile kısa ayarlarla çalıştırılır, gerçek GPU'da henüz çalıştırılmadı). Windows, WSL ve macOS desteklenmez. Bulutta kiralık bir GPU sunucusu da olur.
- **Üreticinin OpenCL sürücüsü:**
  - NVIDIA: sürücü paketi, OpenCL'i içerir; `nvidia-smi` çalışmalı.
  - AMD: ROCm ya da AMDGPU-PRO OpenCL.
  - Intel: `intel-opencl-icd`.
- **Paketler:** `sudo apt install build-essential pkg-config python3 git cmake ninja-build ocl-icd-opencl-dev opencl-headers ocl-icd-libopencl1 clinfo`
  - `clinfo` aygıtı göstermelidir.
  - pocl'u (`pocl-opencl-icd`) kurmayın; kuruluysa ölçülecek aygıtın numarasına dikkat edin.
- **Go:** `go.mod`'daki sürüm, https://go.dev/dl adresinden.
- **liboqs 0.15.0** `/usr/local` altında. CI ile aynı derleme:

  ```bash
  git clone --branch 0.15.0 --depth 1 https://github.com/open-quantum-safe/liboqs /tmp/liboqs
  cmake -S /tmp/liboqs -B /tmp/liboqs/build -GNinja -DBUILD_SHARED_LIBS=ON -DOQS_BUILD_ONLY_LIB=ON -DOQS_USE_OPENSSL=OFF -DOQS_MINIMAL_BUILD="SIG_ml_dsa_65" -DCMAKE_INSTALL_PREFIX=/usr/local
  sudo ninja -C /tmp/liboqs/build install && sudo ldconfig
  ```

  Etiketin commit'i `97f6b86b1b6d109cfd43cf276ae39c2e776aed80` olmalı:

  ```bash
  git -C /tmp/liboqs rev-parse HEAD
  ```

Betik sistem paketi kurmaz. Yalnızca Go modüllerini `go mod download` ile, `go.sum`'a göre doğrulayarak indirir. Eksik bir önkoşul olursa Türkçe ipucuyla durur.

## Çalıştırma

Depo kökünde, makinede başka yük yokken:

```bash
bash scripts/gpu-olcum.sh
```

- **Süre:** yaklaşık 15 dakika. GPU ve CPU için ayrı ayrı 1 ısınma + 5 × 60 sn, ardından çekirdek A/B ölçümü.
- **Birden çok aygıt:** önce `-gpu-list` çıktısına bakın, sonra aygıtı seçin:

  ```bash
  QBTC_GPU_DEVICE=1 bash scripts/gpu-olcum.sh
  ```

- **GPU olmayan aygıt:** betik böyle bir aygıtı reddeder, çünkü sonucu GPU hashrate'i diye yayımlanmamalı.
- **Birden çok GPU:** her kart ayrı ayrı ölçülür ve raporlanır.

## Neyi geri göndermeli

Betik `qbtc-olcum-<UTC zaman>.json` dosyasını yazar. Bu dosyayı gönderin. Rapor makine adı, kullanıcı adı, yol ya da adres içermez; kamuya açıklanabilir. İçinde şunlar var:

- **`gpu`, `cpu`:**
  - ölçümler, ortanca, en düşük ve en yüksek değer;
  - aygıt adı, türü, platform ve sürücü sürümü;
  - tur boyu;
  - commit (`modified` true ise ağaç kirliydi; böyle bir rapor yayımlanmaz, temiz commit'te yeniden ölçülür), Go sürümü, çekirdek kaynağının SHA-256'sı;
  - sabit tur boyuyla yeniden üretme komutu (`reproduce`);
  - mainnet önerisi: `mainnet_start_bits` (hashrate'ten, 0x1d00ffff'e kırpılmış) ve `mainnet_floor_bits`, hedef cinsinden min(başlangıç · 2^16, 0x1d00ffff).
- **`kernel_ab`:** açılmış ve döngülü çekirdeğin aynı aygıttaki hız oranı.
  - Karar kuralı (PR #29): oran 1'in altındaysa döngülü çekirdeğe dönülür.
- **`gpu_hashrate_mi`:** yalnızca gerçek bir GPU ölçüldüyse true.

## Yorum

- Ortanca yayımlanır, tek ölçüm değil. GPU ölçümleri arasındaki yayılım %10'u aşarsa betik uyarır; makineyi boşaltıp yeniden ölçün.
- Lansmanda başlatıcı genesis'i ve 1.–2. bloğu aynı donanım ve aynı kazıcıyla kazar. Açıklanan hashrate bu rapordaki `reproduce` komutuyla yeniden üretilebilmelidir.
- Bu ölçüm madde 6a'yı tek başına kapatmaz. Başlangıç hedefi ve taban kullanıcı kararıyla `MainnetParams`'a ve bağımsız test vektörüne girer.
