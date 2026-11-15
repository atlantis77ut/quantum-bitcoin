# Blok yayılım ölçümü (60/30 sn blok süresi kararı için)

Blok süresi 60 sn olarak donduruldu. Yayılım ve doğrulama ≤1–2 sn ölçülürse lansmandan önce 30 sn'ye inebilir ([MAINNET-ONCESI.md](MAINNET-ONCESI.md)). Bu araç o ölçümün girdilerini üretir. **Karar bu belgede verilmez:** hangi bağlantı profilinin belirleyici olduğunu kullanıcı seçer, 60/30 kararı da uygulanmadan önce `devils-advocate` ile sınanır.

## Ne ölçülür, ne benzetilir

Araç iki katmanlıdır. Raporda ikisi ayrı tutulur.

- **A — Blok (gerçek, bu makinede):** dolu bloğun ağdaki boyutu (`wire_bytes`), mesajın çözümlenmesi (`decode_ms`) ve taze bir zincirde tam doğrulama (`accept_ms`: ML-DSA-65 imzaları, bakiyeler, LevelDB batch yazımı). Blok, fonlanmış tek bir gönderenden ardışık nonce'lu işlemlerle kurulur.
- **B — Yayılım (benzetim):** aynı süreçte düğümler kurulur. Aralarında, her düğümün yukarı hattını (`-bench-prop-mbit`) ve bağlantı başına tek yön gecikmeyi (`-bench-prop-latency`) uygulayan bir proxy durur. Süre, kazıcının kendi bloğunu kabul etmesinden her düğümün ucunun o bloğa geçmesine kadardır. **Ağ değerleri seçilen profilin sonucudur, gerçek ağ ölçümü değildir.**

Benzetimde blok yalnızca itmeyle gider: alıcıların yukarı akış eşi yoktur, senkronizasyon aralığı 1 saattir, keşif kapalıdır. Taşıma bugünkü itme taşımasıdır ([ES-KESFI.md](ES-KESFI.md) §4.5): tam blok, depola-ilet, aynı anda en çok 2 blok itmesi, alıcıda 10 sn bedava + 128 KiB/sn asgari ortalama hız + 60 sn tavan.

## Aritmetik

S bayt blok, R bit/sn yukarı hat, L tek yön gecikme ve V çözümleme + doğrulama için bir atlamanın süresi yaklaşık:

  t_atlama ≈ 8·S / R + 3·L + V

(TCP el sıkışması 2·L, ilk baytın varışı L. İtme her blokta yeni bağlantı açar; proxy el sıkışmayı da benzetir.) Depola-ilet yüzünden h atlamalık hatta T ≈ h · t_atlama olur. Bir düğüm k eşe iterse, aynı anda en çok 2 kopya gider ve hat paylaşılır. Bu yüzden k'nci eş yaklaşık ⌈k/2⌉ · 2 · 8·S / R sonra bloğu alır.

Örnek: dolu blok (250 işlem) ~1,8 MB, yani ~14,5 Mbit (kesin değer raporun `wire_bytes` alanıdır). 20 Mbit/sn hatta 14,5 / 20 ≈ 0,7 sn, artı 0,15 sn gecikme, artı V. 3 atlamada ~3 sn artı 3·V.

Kaba eskime (stale) oranı p ≈ 1 − e^(−T / blok süresi). T = 3 sn için 60 sn'de ~%5, 30 sn'de ~%10 olur. T ≤ 1–2 sn şartı dolu blokta ancak çok yüksek hatlarda ya da boş/az dolu bloklarda sağlanır. Lansmanda bloklar boştur, sorun orada görünmez.

## Çalıştırma

Geçici dizinde kendi regtest zincirlerini kurar; `-network` ve `-db` kullanılmaz, çalışan düğüme dokunmaz. Ölçüm kesilirse (Ctrl-C) sistemin geçici dizininde `qbtc-yayilim-*` kalabilir; anahtarsız regtest verisidir, silinebilir. liboqs gerekir ([GPU-OLCUM.md](GPU-OLCUM.md) "Ne gerekir").

```bash
go build -o qbtc . && ./qbtc -bench-propagation -bench-runs 5 -bench-out yayilim.json
```

| Bayrak | Varsayılan | Anlamı |
|---|---|---|
| `-bench-prop-txs` | 250 (`MaxTxPerBlock`) | blok başına işlem (coinbase hariç) |
| `-bench-prop-hops` | 3 | kazıcı dışındaki düğüm sayısı |
| `-bench-prop-topology` | `line` | `line`, `star-leaf`, `star-hub` |
| `-bench-prop-mbit` | 20 | her düğümün yukarı hattı, Mbit/sn (en çok 10.000) |
| `-bench-prop-latency` | 50ms | bağlantı başına tek yön gecikme (en çok 1 sn) |
| `-bench-prop-timeout` | 2m | bir ölçümde beklenen en uzun süre |
| `-bench-runs` | 1 | tekrar sayısı (5 önerilir; en çok 100) |
| `-bench-out` | yok | JSON rapor dosyası |

Varsayılanların gerekçesi: 20 Mbit/sn tipik ev yükleme hızı ve ucuz VPS'in alt sınırıdır; 50 ms tek yön, kıta içi (~10–30 ms) ile kıtalar arası (~70–150 ms) arasındadır; 3 atlama gömülü listeden uzak bir düğümün tipik mesafesidir. Varsayılanlar karar değildir.

Rapor makine adı, kullanıcı adı, yol ya da adres içermez; `reproduce` alanı aynı ölçümün komutudur. Her düğüm için ortanca (`median_ms`), en kötü (`max_ms`) ve kaç ölçümde ulaşıldığı (`reached`) yazılır. Ulaşılmayan ölçüm -1'dir ve uyarı loglanır.

## Topolojiler

- **`line`:** 0 → 1 → … → h. Depola-ilet gecikmesinin atlama başına birikimini gösterir.
- **`star-hub`:** merkez kazar, h yaprağa iter. Paylaşılan hattı ve 2 kopya sınırını gösterir.
- **`star-leaf`:** bir yaprak kazar, merkeze iter, merkez diğer yapraklara iter (merkez kazıcıyı eş olarak almaz; yoksa ucunu yoklayıp bloğu ondan çekerdi). Gömülü liste işletmecilerinin yarış avantajını (γ) gösterir: merkez bloğu herkesten önce alır ([ES-KESFI.md](ES-KESFI.md) §6). `nodes` alanında ilk düğüm merkezdir.

## Önerilen matris

Her hücre `-bench-runs 5` ile, `line` 3 atlama ve `star-hub` 8 yaprak:

| Mbit/sn \ tek yön | 25 ms | 50 ms | 75 ms |
|---|---|---|---|
| 2 | | | |
| 10 | | | |
| 20 | | | |
| 50 | | | |
| 100 | | | |

Ek olarak `star-leaf` 8 yaprak, 20 ve 100 Mbit/sn, 50 ms. Dolu blok ile `-bench-prop-txs 100` (az dolu) ve `-bench-prop-txs 0` (boş, lansman) karşılaştırılmalıdır.

## Kalibrasyon (önce bir kez)

Proxy'nin alıcı sınırlarını doğru uyguladığını doğrular (CI için fazla uzun):

```bash
./qbtc -bench-propagation -bench-prop-hops 1 -bench-prop-mbit 0.8 -bench-prop-latency 25ms
```

0,8 Mbit/sn'de (~100 KB/sn) dolu blok 128 KiB/sn asgari hızın altında kalır; alıcının süresi 10 sn + okunan / 128 KiB/sn olduğundan ~40 sn'de keser, `reached` 0 olur ve zaman aşımından sonra uyarı çıkar.

```bash
./qbtc -bench-propagation -bench-prop-hops 1 -bench-prop-mbit 2 -bench-prop-latency 25ms
```

2 Mbit/sn'de blok ~30 sn'de ulaşır. İkisinden biri beklendiği gibi değilse matris sonuçları kullanılmaz.

## Kalanlar ve sınırlar

- **Gerçek çok makineli ölçüm yapılmadı.** Benzetim kayıp, TCP yavaş başlangıcı ve gerçek RTT'deki pencere büyümesi içermez (TCP döngü arayüzündedir, gecikmeyi proxy ekler); gerçek yayılımı iyimser gösterebilir. Karar öncesi en az bir kıtalar arası 3 makineli ölçüm önerilir.
- **Bütün düğümler 127.0.0.1'dedir:** her alıcı gelen bütün bağlantıları tek adres grubundan görür (grup başına 4 bağlantı, 1 çözümleme yuvası). Bu topolojilerde her alıcının tek kaynağı olduğu için sonuç değişmez; çok kaynaklı gerçek ağdaki grup davranışı benzetilmez.
- **Doğrulama aynı CPU'yu paylaşır:** `star-hub`'da yapraklar aynı anda doğrular; çok çekirdekli makinede sorun değildir, az çekirdekte V şişer. A katmanının `accept_ms`'i tek başına ölçümdür.
- **Yalnızca itme:** kompakt blok ya da işlem tekrarını atlayan bir aktarım yoktur; blok gövdesi her atlamada tam gider. Kompakt bloklar ayrı bir karardır.
- Sonuçlar ve kullanıcının seçtiği belirleyici profil MAINNET-ONCESI'ne yazılır; 60/30 kararı `devils-advocate` sonrası kullanıcınındır.
