# Quantum Bitcoin (Q-BTC)

Kuantuma dayanıklı imzalı, iş ispatı (PoW) tabanlı bir Layer-1 prototipi. Go ile yazılmıştır.

> **Durum: prototip, mainnet başlamadı.** Mainnet genesis'i henüz üretilmedi; düğüm mainnet'te çalışmayı reddeder. Testnet ve regtest geliştirme içindir. Bu yazılımla değeri olan hiçbir şey saklanmamalıdır. Lansman hazırlığının durumu: [docs/LANSMAN.md](docs/LANSMAN.md).

## Özellikler

- **İmza:** ML-DSA-65 (FIPS 204), [liboqs](https://github.com/open-quantum-safe/liboqs) üzerinden (cgo). Düğüm açılışta kütüphanenin ML-DSA-65'i doğru uyguladığını sabit bir bilinen-yanıt vektörüyle denetler; geçemezse çalışmaz.
- **İş ispatı:** SHA256d, Bitcoin'den farklı 96 baytlık blok başlığı.
- **Blok süresi:** 60 saniye. Zorluk her blokta ASERT ile ayarlanır (yarı ömür 2 saat).
- **Emisyon:** premine yok. İlk 20.000 blokta doğrusal artan ödül, sonra blok başına 5 QBTC; 2.100.000 blokta bir yarılanma; blok başına 0,1 QBTC kuyruk ödülü. Kazım ödülü 240 blok sonra harcanabilir.
- **İşlemler:** her işlemin zorunlu bir son kullanma yüksekliği vardır.
- **Zincir seçimi:** en çok birikmiş iş.
- **Depolama:** LevelDB; durum güncellemeleri atomik batch'lerle yazılır.
- **Kazım:** çok çekirdekli CPU kazıcısı ve açık kaynak OpenCL GPU kazıcısı (`-tags opencl`).

Uzlaşma kararları ve gerekçeleri: [CLAUDE.md](CLAUDE.md) ("Dondurulmuş uzlaşma kararları") ve [docs/MAINNET-ONCESI.md](docs/MAINNET-ONCESI.md). Güvenlik varsayımları: [docs/GUVENLIK-VARSAYIMLARI.md](docs/GUVENLIK-VARSAYIMLARI.md).

## Derleme

Gerekenler: Linux, Go 1.22.3 ya da sonrası, C derleyicisi (cgo), CMake ve Ninja. liboqs 0.15.0 `/usr/local` altına kurulmalıdır.

```bash
git clone --branch 0.15.0 --depth 1 https://github.com/open-quantum-safe/liboqs /tmp/liboqs
cmake -S /tmp/liboqs -B /tmp/liboqs/build -GNinja \
  -DBUILD_SHARED_LIBS=ON -DOQS_BUILD_ONLY_LIB=ON -DOQS_USE_OPENSSL=OFF \
  -DOQS_MINIMAL_BUILD="SIG_ml_dsa_65" -DCMAKE_INSTALL_PREFIX=/usr/local
sudo ninja -C /tmp/liboqs/build install && sudo ldconfig

export PKG_CONFIG_PATH="$(go list -m -f '{{.Dir}}' github.com/open-quantum-safe/liboqs-go)/.config:/usr/local/lib/pkgconfig"
go build -o qbtc .
```

GPU kazıcısı için OpenCL geliştirme paketleri (ör. `ocl-icd-opencl-dev`, `opencl-headers`) ve bir OpenCL sürücüsü gerekir: `go build -tags opencl -o qbtc .`

## Çalıştırma

```bash
./qbtc -network regtest                  # yerel deneme ağı
./qbtc -network testnet -peer host:18001 # testnet, bir eşe bağlanarak
./qbtc -network regtest -mine <adres>    # kazım; ödül <adres>'e
./qbtc -gpu-list                         # OpenCL aygıtları (-tags opencl)
./qbtc -bench-hashrate 30s               # Q-BTC başlığında hashrate ölçümü
```

Varsayılan ağ testnet'tir. Bütün bayraklar: `./qbtc -h`. Veri dizini varsayılan olarak `qbtc_<ağ>_db`'dir. Şema sürümü değiştiğinde eski veri dizinleri açılmaz; silinip yeniden başlatılmalıdır.

## Testler

```bash
go vet ./... && go test -race -count=1 ./...
python3 -I scripts/genesis_vektor_test.py
python3 -I scripts/lansman_commit_denetle_test.py
python3 -I scripts/lansman_test.py
```

CI ([.github/workflows/ci.yml](.github/workflows/ci.yml)) bunlara ek olarak biçim denetimi, açılış denetiminin uçtan uca sınaması, GPU çekirdeğinin pocl üzerinde doğruluğu ve genesis vektörlerinin Go kodundan bağımsız Python hesabını çalıştırır.

## Depo düzeni

| Dizin | İçerik |
|---|---|
| `core/` | Blok, zincir, bakiye defteri, zorluk, emisyon, mempool, lansman dosyası |
| `crypto/` | ML-DSA-65 anahtarları ve açılış denetimi |
| `network/` | HTTP API ve P2P |
| `gpu/` | OpenCL kazıcısı ve çekirdek üreteci |
| `scripts/` | Bağımsız vektör betikleri, ölçüm betikleri, lansman betiği |
| `docs/` | Tasarım notları ve kılavuzlar (Türkçe) |

Doküman ve yorumlar Türkçedir; kod, tanımlayıcılar ve commit mesajları İngilizcedir.

## Güvenlik

Bu yazılım bağımsız bir güvenlik denetiminden geçmedi. Özel bir güvenlik bildirim kanalı lansmandan önce duyurulacaktır. O zamana kadar bir açık bulursanız ayrıntısını ve istismar yolunu herkese açık yazmadan, yalnızca "güvenlik bildirimi var" diyen bir issue açın.

## Lisans

[MIT](LICENSE). Bağımlılıklar kendi lisanslarıyla gelir: liboqs ve liboqs-go MIT (liboqs içindeki bazı üçüncü taraf parçalar kendi alt dizinlerindeki lisanslarla), goleveldb BSD-2-Clause, snappy BSD-3-Clause. liboqs depoya dahil değildir; ayrıca kurulur.
