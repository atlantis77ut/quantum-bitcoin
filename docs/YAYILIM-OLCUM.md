# Blok yayılım ölçümü (60/30 sn blok süresi kararı için)

Blok süresi 60 sn olarak donduruldu. Yayılım ve doğrulama ≤1–2 sn ölçülürse lansmandan önce 30 sn'ye inebilirdi. **Ölçümden sonra karar kesinleşti (kullanıcı, 2026-11-16): 60 sn ve 250 işlem kalır** (aşağıda "Eşit hacim ölçümü"; [MAINNET-ONCESI.md](MAINNET-ONCESI.md)). Bu araç o ölçümün girdilerini üretir. Araç kararı kendisi vermez; belirleyici profili ve kararı kullanıcı verir.

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

Yerelde Go ve liboqs yoksa matris "Yayılım ölçümü" iş akışıyla ([.github/workflows/yayilim-olcum.yml](../.github/workflows/yayilim-olcum.yml)) çalıştırılır. İş akışı elle tetiklenir (`runs`: hücre başına ölçüm, 1..20, varsayılan 5; `profile`: `full` önerilen matris, `quick` yalnızca 20 Mbit/sn ve 50 ms ya da `hub` yalnızca 8 yapraklı star-hub, 2 ve 10 Mbit/sn, 50 ms ya da `doluluk` yalnızca 3 atlamalı hat, 10 ve 20 Mbit/sn, 50 ms, 250 ve 125 işlemlik blok). Önce iki kalibrasyon ölçümünü, sonra aşağıdaki matrisi [scripts/yayilim-matris.sh](../scripts/yayilim-matris.sh) ile çalıştırır. Her hücrenin JSON raporu, logu ve [scripts/yayilim-ozet.py](../scripts/yayilim-ozet.py) ile üretilen Markdown özeti (`OZET.md`, iş akışı özetinde de görünür) `yayilim-olcum` artifact'ında 90 gün durur.

```bash
gh workflow run yayilim-olcum.yml -f runs=5 -f profile=full
```

Doğrulama süreleri (`accept_ms`) GitHub'ın paylaşımlı makinesinin CPU'suyla ölçülür (çekirdek sayısı raporun `cpus` alanında). Gerçek düğüm donanımından yavaş olabilir. Ağ değerleri her yerde olduğu gibi benzetimdir.

## Topolojiler

- **`line`:** 0 → 1 → … → h. Depola-ilet gecikmesinin atlama başına birikimini gösterir.
- **`star-hub`:** merkez kazar, h yaprağa iter. Paylaşılan hattı ve iki itme yuvasını gösterir (yuva teslime kadar tutulduğundan, "İtme yuvası teslim ölçümü"); ilk ölçüm bu değişiklikten önce yapıldı.
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

## İlk ölçüm sonuçları (2026-11-16)

GitHub Actions, "Yayılım ölçümü" iş akışı, çalıştırma 43837222599, `profile: full`, hücre başına 5 ölçüm. Commit 38730f1, go1.22.3, linux/amd64, 4 CPU. Ağ değerleri benzetimdir (proxy, döngü arayüzü); gerçek ağ ölçümü değildir. Hücreler en geç düğümün ortanca süresidir (sn).

**Blok (A katmanı, gerçek bileşenler):**

| Blok | Bayt | Çözümleme | Doğrulama |
|---|---|---|---|
| Dolu (250 işlem) | 1.809.263 | 6,4 ms | 22,2 ms |
| 100 işlem | 724.014 | 2,8 ms | 9,4 ms |
| Boş | 622 | ~0 ms | 0,5 ms |

Doğrulama ve çözümleme yayılım süresinin yanında küçüktür; süreyi yukarı hat belirler.

**Kalibrasyon:** 0,8 Mbit/sn 18,24 sn, 2 Mbit/sn 7,34 sn. İkisi de bayt / hızla uyuşur (14,47 Mbit / R). Bu çalıştırmadaki 0,8 Mbit/sn beklentisi ("ulaşmamalı") eski blok boyutundan kalmaydı; sonraki çalıştırmalar 0,3 Mbit/sn kullanır.

**line, 3 atlama** (her atlama tam bloğu bir kez gönderir):

| Mbit/sn \ tek yön | 25 ms | 50 ms | 75 ms |
|---|---|---|---|
| 2 | 22,01 | 22,24 | 22,46 |
| 10 | 4,65 | 4,87 | 5,10 |
| 20 | 2,47 | 2,70 | 2,93 |
| 50 | 1,17 | 1,40 | 1,62 |
| 100 | 0,74 | 0,97 | 1,19 |

**star-hub, 8 yaprak** (merkezin yukarı hattı 8 kopyayı taşır; itme yuvası teslim değişikliğinden önce, 2 Mbit/sn hücresi o zamanki ölçüm yapıntısıdır, "İtme yuvası teslim ölçümü"):

| Mbit/sn \ tek yön | 25 ms | 50 ms | 75 ms |
|---|---|---|---|
| 2 | ulaşmadı (yapıntı) | ulaşmadı (yapıntı) | ulaşmadı (yapıntı) |
| 10 | 11,34 | 11,66 | 11,66 |
| 20 | 5,89 | 5,96 | 6,04 |
| 50 | 2,44 | 2,50 | 2,57 |
| 100 | 1,26 | 1,34 | 1,41 |

**Ek ölçümler (50 ms):**
- star-leaf, 8 yaprak: 20 Mbit/sn'de 6,08 sn, 100 Mbit/sn'de 1,44 sn.
- line 3 atlama, 20 Mbit/sn: 100 işlemlik blok 1,36 sn, boş blok 0,47 sn.

**Okuma:**
- Süre neredeyse tamamen bayt / yukarı hat: line'da ≈ 3 · S / U, star-hub'da ≈ 8 · S / U. Gecikme (25 → 75 ms) yalnızca ~0,2–0,5 sn ekler.
- "Yayılım + doğrulama ≤ 1–2 sn" eşiğini dolu blokla yalnızca 50 Mbit/sn ve üstü line hücreleri ile 100 Mbit/sn star-hub hücreleri karşılar. 20 Mbit/sn'de 2,5–6 sn, 10 Mbit/sn'de 5–12 sn.
- Kaba bayat blok tahmini (en geç düğüm için, p ≈ 1 − e^(−T / blok süresi); düğüm sayısı, hashrate dağılımı ve kısmi yayılım hesaba katılmaz):

| T (hücre) | 60 sn | 30 sn |
|---|---|---|
| 0,97 sn (line, 100 Mbit/sn) | %1,6 | %3,2 |
| 1,40 sn (line, 50 Mbit/sn) | %2,3 | %4,6 |
| 2,70 sn (line, 20 Mbit/sn) | %4,4 | %8,6 |
| 4,87 sn (line, 10 Mbit/sn) | %7,8 | %15,0 |
| 5,96 sn (star-hub, 20 Mbit/sn) | %9,5 | %18,0 |
| 11,66 sn (star-hub, 10 Mbit/sn) | %17,7 | %32,2 |
| 22,24 sn (line, 2 Mbit/sn) | %31,0 | %52,4 |

**Belirleyici profil (kullanıcı kararı, 2026-11-16):** 20 Mbit/sn yukarı hat, 3 atlamalı hat, 50 ms tek yön; en geç düğüm 2,70 sn. Bu, 30 sn için konan ≤1–2 sn eşiğini karşılamaz; dondurulmuş kurala göre blok süresi 60 sn kalır. 60/30 kararının son hâli `devils-advocate` sonrası kullanıcınındır.

**`devils-advocate` (2026-11-16): "60 sn'de kal" kararı ZAYIF.**
- Ana itiraz: `MaxTxPerBlock` (250) `TargetSpacing`'den türetilmez; 30 sn'yi 250 işlemle ölçmek "30 sn + iki kat hacim" demektir. Eşit hacimde 30 sn blok ~125 işlem, ~0,9 MB'tır. 100 işlemlik blok seçilen profilde 1,36 sn ölçüldü, 125 işlem için doğrusal tahmin ~1,6 sn (ölçülmedi); bu ≤1–2 sn eşiğinin içindedir. 250 işlem sınırı da dondurulmuş bir karardır, 125'e inmek ayrıca kullanıcı kararı ister.
- Diğer itirazlar: blok ağda JSON + base64 gider (ikili kodlama süreyi kabaca ~2,1 sn'ye indirir, tahmin); işlemler önceden yayıldığı için kompakt blok dürüst bloğu boş bloğun ~0,47 sn'sine yaklaştırabilir. Geri alınamaz uzlaşma parametresi geri alınabilir taşıma seçimlerine bağlanmış olur.
- 60 sn lehine: benzetim iyimserdir (yeni bağlantı başına TCP yavaş başlangıcı, gerçekte muhtemelen ~4 sn); ücretsiz işlemle doldurulmuş blokta kompakt blok yardım etmez; gömülü liste işletmecisinin star-leaf avantajı (20 Mbit/sn'de 6,08 sn) 30 sn'de iki kat kötüleşir; emisyon ve iş/zamana bağlı onay güvenliği iki seçenekte de aynıdır.
- Kararı değiştirecek ölçümler: (a) `-bench-prop-txs 125`, 20 Mbit/sn, 3 atlama, 50 ms; ≤2 sn çıkarsa soru "30 sn + 125 işlem" olarak yeniden açılır; (b) kıtalar arası 3 makineli gerçek ölçüm (>4 sn çıkarsa 60 sn kesinleşir); (c) lansmandan önce ikili kodlama ya da kompakt blok yapılacaksa ölçüm onunla yenilenir.

**Eşit hacim ölçümü (2026-11-16, `profile: doluluk`, Actions çalıştırması 43872087301, 5 ölçüm, 3 atlamalı hat, 50 ms):**

| Blok | Bayt | Doğrulama | 10 Mbit/sn | 20 Mbit/sn |
|---|---|---|---|---|
| 250 işlem (60 sn'de saniyede ~4,2 işlem) | 1.809.264 | 38,6 ms | 4,93 sn | 2,76 sn |
| 125 işlem (30 sn'de aynı hacim) | 904.889 | 18,9 ms | 2,69 sn | 1,61 sn |

- Ölçümler kararlıdır: 20 Mbit/sn'de beş ölçümün yayılımı 1 ms içinde.
- Doğrulama bu çalıştırmada ilkinden yavaş çıktı (aynı makine sınıfı, 22 → 38 ms); yayılımın yanında yine küçüktür.
- Seçilen profilde 125 işlemlik blok 1,61 sn ile ≤2 sn eşiğinin içindedir. (a) koşulu sağlandı: soru "30 sn + 125 işlem" olarak yeniden açılır.
- Kaba bayat tahmini: 30 sn + 125 işlem %5,2; 60 sn + 250 işlem %4,5 (10 Mbit/sn'de %8,6 ve %7,9). Aynı işlem hacminde iki seçenek benzer bayat oranı, 30 sn yarı ilk onay süresi verir.
- Kullanıcı kararı (2026-11-16): 60 sn ve 250 işlem kalır. 30 sn'ye inmek ayrıca blok süresi kararının yanında `MaxTxPerBlock`'u (250, dondurulmuş) 125'e indirmeyi ya da `TargetSpacing`'den türetmeyi isterdi. (b) gerçek çok makineli ölçüm ve (c) taşıma iyileştirmeleri açıktır.

## Kalanlar ve sınırlar

- **feat/push-stall-slot'tan sonra benzetim sapması:** hız sınırlayan vekil (`throttleProxy`) göndericiden okuyup kendi kuyruğunda tuttuğundan baytlar hattan geçmeden onaylanır; Linux'ta teslim bekleyen itmenin kuyruğu erken boşalır ve düşük hızda (ör. 2 Mbit/sn hub) yuva teslimden önce devralınabilir. Vekil düzeltilene kadar (bayt hatta çıkacağı an okunur) bu değişiklikten sonraki Linux ölçümleri düşük hızlarda 2026-11-16 ölçümüyle karşılaştırılamaz ([MAINNET-ONCESI.md](MAINNET-ONCESI.md), teslim ilerlemesi).

- **İtme yuvası teslim ölçümü (feat/push-slot-delivery, Actions 43893936454, `profile: hub`, 5 ölçüm, 50 ms).** Yuva artık alıcı bloğu işleyip kapatınca boşalır ([MAINNET-ONCESI.md](MAINNET-ONCESI.md), blok süresi maddesi). 10 Mbit/sn'de yapraklar ikişer gelir: 3,10 / 3,10 / 6,23 / 6,25 / 9,28 / 9,32 / 12,41 / 12,42 sn; en geç yaprağın ortancası 12,37 sn (önceki 11,66 sn; fark kopyalar arası bekleme ve kapatma turudur). 2 Mbit/sn'de 5 ölçümün 4'ünde yapraklar ikişer ~14,6 / 29 / 44 / 58 sn'de gelir; 40 varıştan 39'u gerçekleşti, en geç yaprağın ortancası 45,9 sn. Bir ölçümde 7. yaprak 120 sn içinde almadı: o ölçümde son çiftin diğer yaprağı 53,2 sn'de, yani tek kopyanın bütün hattı kullanacağı sürede geldi; 7. yaprağın itmesi aktarımdan önce başarısız oldu ya da iptal edildi. Hız sınırıyla devralma değil: `sendChunk` 14 sn olduğundan pay ~13 sn'dir. Kesin neden kayıtlardan belirlenemedi (Debug kayıtları yok). Bu hücre kopya başına ~1 Mbit/sn ile alıcının asgari hızının (≈1,05 Mbit/sn) sınırındadır.
- **Önceki ölçümde star-hub iki itme yuvasını ölçmüyordu (giderildi).** Döngü arayüzünde MSS ~32–64 KB olduğundan Linux gönderen soketin tamponunu ~2,6 MB'a büyütür ve ~1,8 MB'lık bloğun tamamı çekirdek tamponuna sığar. Düğümün yazması blok kısıtlı hattan geçmeden biter, itme yuvası erken boşalır ve 8 kopya birlikte gider. Kanıt: 10 Mbit/sn'de 8 yaprak ikişer ikişer (~2,9 / 5,8 / 8,7 / 11,6 sn) değil, hepsi ~11,6 sn'de geldi. 2 Mbit/sn'de her kopya 0,25 Mbit/sn'ye düşer, alıcının asgari hızının altında kalır ve hiçbir yaprağa ulaşmaz; bu hücre ölçüm yapıntısıdır, düğüm hatası değildir.
  - Proxy tarafında çözüm yok. WSL'de 4 MB'ı 2 MB/sn okuyan bir alıcıya yazma denemesinde çekirdeğin emdiği miktar: varsayılan ~2,26 MB; dinleyicide 32 KiB alma tamponu ~2,42 MB; dinleyicide MSS 1460 ~1,22 MB; ikisi birden ~0,58 MB. Yalnızca gönderenin (düğümün) tamponunu 64 KiB'a sabitlemek ~0,19 MB'a indirir; bu düğüm kodu değişikliğidir.
  - Hub hücrelerinde en yavaş düğüm süresi her iki durumda da ≈ 8·S/U olduğundan (yuvalar sırayla gönderse de hepsi birlikte gönderse de uplink aynı baytı taşır) ≥10 Mbit/sn sonuçları en yavaş yaprak için geçerlidir; ilk yaprakların erken varışı ve 2 Mbit/sn hücresi geçerli değildir.
  - Gerçek ağda da benzer bir risk olabilir: MSS 1460'ta başlangıç tamponu küçüktür, ama otomatik ayar ve tamponu şişkin bir uplink yuvanın erken boşalmasına ve yavaş bir kazıcının ikiden çok kopyayı asgari hızın altında göndermesine yol açabilir. Gönderen tamponunu sınırlamak (yüksek gecikmeli hatlarda verimi tampon / RTT ile sınırlar) ya da teslim edilen baytı saymak bir düğüm politikası kararıydı; kullanıcı yuvayı teslime kadar tutmayı seçti (2026-11-16, yukarıdaki madde).

- **Gerçek çok makineli ölçüm yapılmadı.** Benzetim kayıp, TCP yavaş başlangıcı ve gerçek RTT'deki pencere büyümesi içermez (TCP döngü arayüzündedir, gecikmeyi proxy ekler); gerçek yayılımı iyimser gösterebilir. Karar öncesi en az bir kıtalar arası 3 makineli ölçüm önerilir.
- **Bütün düğümler 127.0.0.1'dedir:** her alıcı gelen bütün bağlantıları tek adres grubundan görür (grup başına 4 bağlantı, 1 çözümleme yuvası). Bu topolojilerde her alıcının tek kaynağı olduğu için sonuç değişmez; çok kaynaklı gerçek ağdaki grup davranışı benzetilmez.
- **Doğrulama aynı CPU'yu paylaşır:** `star-hub`'da yapraklar aynı anda doğrular; çok çekirdekli makinede sorun değildir, az çekirdekte V şişer. A katmanının `accept_ms`'i tek başına ölçümdür.
- **Yalnızca itme:** kompakt blok ya da işlem tekrarını atlayan bir aktarım yoktur; blok gövdesi her atlamada tam gider. Kompakt bloklar ayrı bir karardır.
- Sonuçlar ve kullanıcının seçtiği belirleyici profil MAINNET-ONCESI'ne yazılır; 60/30 kararı `devils-advocate` sonrası kullanıcınındır.
