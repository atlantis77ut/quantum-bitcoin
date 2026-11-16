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

### GitHub Actions'ta

Yerelde Go ve liboqs yoksa matris "Yayılım ölçümü" iş akışıyla ([.github/workflows/yayilim-olcum.yml](../.github/workflows/yayilim-olcum.yml)) çalıştırılır. İş akışı elle tetiklenir (`runs`: hücre başına ölçüm, 1..20, varsayılan 5; `profile`: `full` önerilen matris, `quick` yalnızca 20 Mbit/sn ve 50 ms ya da `hub` yalnızca 8 yapraklı star-hub, 2 ve 10 Mbit/sn, 50 ms). Önce iki kalibrasyon ölçümünü, sonra aşağıdaki matrisi [scripts/yayilim-matris.sh](../scripts/yayilim-matris.sh) ile çalıştırır. Her hücrenin JSON raporu, logu ve [scripts/yayilim-ozet.py](../scripts/yayilim-ozet.py) ile üretilen Markdown özeti (`OZET.md`, iş akışı özetinde de görünür) `yayilim-olcum` artifact'ında 90 gün durur.

```bash
gh workflow run yayilim-olcum.yml -f runs=5 -f profile=full
```

Doğrulama süreleri (`accept_ms`) GitHub'ın paylaşımlı makinesinin CPU'suyla ölçülür (çekirdek sayısı raporun `cpus` alanında). Gerçek düğüm donanımından yavaş olabilir. Ağ değerleri her yerde olduğu gibi benzetimdir.

## Topolojiler

- **`line`:** 0 → 1 → … → h. Depola-ilet gecikmesinin atlama başına birikimini gösterir.
- **`star-hub`:** merkez kazar, h yaprağa iter. Paylaşılan hattı gösterir; 2 kopya sınırını döngü arayüzünde gösteremez (aşağıda "Kalanlar ve sınırlar").
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
./qbtc -bench-propagation -bench-prop-hops 1 -bench-prop-mbit 0.3 -bench-prop-latency 25ms
```

0,3 Mbit/sn'de dolu blok (~1,8 MB) 128 KiB/sn asgari hızın çok altında kalır; alıcı ~14 sn'de keser, `reached` 0 olur ve zaman aşımından sonra uyarı çıkar. (Eski 0,8 Mbit/sn beklentisi 7,2 MiB'lık bloklardan kalmaydı; ~1,8 MB'lık blok 0,8 Mbit/sn'de ~18 sn'de ulaşır.)

```bash
./qbtc -bench-propagation -bench-prop-hops 1 -bench-prop-mbit 2 -bench-prop-latency 25ms
```

2 Mbit/sn'de blok ~7,3 sn'de ulaşır (bayt / hız + gecikmeler). İkisinden biri beklendiği gibi değilse matris sonuçları kullanılmaz. İlk Actions ölçümünde 0,8 Mbit/sn 18,24 sn, 2 Mbit/sn 7,34 sn verdi; ikisi de bayt / hızla uyuşur, yani proxy hattı doğru sınırlar.

## Kalanlar ve sınırlar

- **Star-hub hücreleri iki itme yuvasını ölçmez.** Döngü arayüzünde MSS ~32–64 KB olduğundan Linux gönderen soketin tamponunu ~2,6 MB'a büyütür ve ~1,8 MB'lık bloğun tamamı çekirdek tamponuna sığar. Düğümün yazması blok kısıtlı hattan geçmeden biter, itme yuvası erken boşalır ve 8 kopya birlikte gider. Kanıt: 10 Mbit/sn'de 8 yaprak ikişer ikişer (~2,9 / 5,8 / 8,7 / 11,6 sn) değil, hepsi ~11,6 sn'de geldi. 2 Mbit/sn'de her kopya 0,25 Mbit/sn'ye düşer, alıcının asgari hızının altında kalır ve hiçbir yaprağa ulaşmaz; bu hücre ölçüm yapıntısıdır, düğüm hatası değildir.
  - Proxy tarafında çözüm yok. WSL'de 4 MB'ı 2 MB/sn okuyan bir alıcıya yazma denemesinde çekirdeğin emdiği miktar: varsayılan ~2,26 MB; dinleyicide 32 KiB alma tamponu ~2,42 MB; dinleyicide MSS 1460 ~1,22 MB; ikisi birden ~0,58 MB. Yalnızca gönderenin (düğümün) tamponunu 64 KiB'a sabitlemek ~0,19 MB'a indirir; bu düğüm kodu değişikliğidir.
  - Hub hücrelerinde en yavaş düğüm süresi her iki durumda da ≈ 8·S/U olduğundan (yuvalar sırayla gönderse de hepsi birlikte gönderse de uplink aynı baytı taşır) ≥10 Mbit/sn sonuçları en yavaş yaprak için geçerlidir; ilk yaprakların erken varışı ve 2 Mbit/sn hücresi geçerli değildir.
  - Gerçek ağda da benzer bir risk olabilir: MSS 1460'ta başlangıç tamponu küçüktür, ama otomatik ayar ve tamponu şişkin bir uplink yuvanın erken boşalmasına ve yavaş bir kazıcının ikiden çok kopyayı asgari hızın altında göndermesine yol açabilir. Gönderen tamponunu sınırlamak (yüksek gecikmeli hatlarda verimi tampon / RTT ile sınırlar) ya da teslim edilen baytı saymak bir düğüm politikası kararıdır; bu belgede açık bırakılmıştır.

- **Gerçek çok makineli ölçüm yapılmadı.** Benzetim kayıp, TCP yavaş başlangıcı ve gerçek RTT'deki pencere büyümesi içermez (TCP döngü arayüzündedir, gecikmeyi proxy ekler); gerçek yayılımı iyimser gösterebilir. Karar öncesi en az bir kıtalar arası 3 makineli ölçüm önerilir.
- **Bütün düğümler 127.0.0.1'dedir:** her alıcı gelen bütün bağlantıları tek adres grubundan görür (grup başına 4 bağlantı, 1 çözümleme yuvası). Bu topolojilerde her alıcının tek kaynağı olduğu için sonuç değişmez; çok kaynaklı gerçek ağdaki grup davranışı benzetilmez.
- **Doğrulama aynı CPU'yu paylaşır:** `star-hub`'da yapraklar aynı anda doğrular; çok çekirdekli makinede sorun değildir, az çekirdekte V şişer. A katmanının `accept_ms`'i tek başına ölçümdür.
- **Yalnızca itme:** kompakt blok ya da işlem tekrarını atlayan bir aktarım yoktur; blok gövdesi her atlamada tam gider. Kompakt bloklar ayrı bir karardır.
- Sonuçlar ve kullanıcının seçtiği belirleyici profil MAINNET-ONCESI'ne yazılır; 60/30 kararı `devils-advocate` sonrası kullanıcınındır.
