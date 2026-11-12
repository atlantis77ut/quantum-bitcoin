# Quantum Bitcoin (Q-BTC)

Go ile yazılmış, ML-DSA-65 (liboqs, cgo) imzalı, PoW tabanlı Layer-1 prototipi. Depolama LevelDB.

- `core/` blok, zincir ve bakiye defteri · `crypto/` kuantuma dayanıklı anahtarlar · `network/` HTTP API ve P2P · `main.go` düğüm girişi
- Derleme `liboqs`'un `/usr/local` altında kurulu olmasını gerektirir (cgo).
- Doküman ve yorumlar Türkçe; kod, tanımlayıcılar ve commit mesajları İngilizce.

## Proje kuralları

- Uzlaşma (consensus) kodu kritiktir: `core/` veya `network/p2p.go`'da blok/işlem doğrulama, bakiye, ödül, zorluk ve imza mantığına dokunan her değişiklik test ile gelir ve commit'ten önce `guvenlik-denetimi` ajanından geçer.
- Para miktarları `int64` birimdir; negatif/sıfır tutar, taşma ve aynı blokta çift harcama her zaman test edilir.
- Diske yazılan durum güncellemeleri LevelDB batch ile atomik yapılır.
- Ağdan gelen hiçbir bloğa veya işleme doğrulamadan güvenilmez.

## Mainnet öncesi açık kalanlar

Kapatılanlar (fix/security-hardening): imza ve adres bağı, P2P blok doğrulaması, negatif tutar, aynı blokta çift harcama, replay (hesap nonce'u), atomik yazım, zaman damgası kuralları, P2P başlatma, HTTP sertleştirme, testler + CI.

Kapatılanlar (önkoşul PR'ları): 10^8 taban birim + işlem ücreti (feat/subunit-and-fees); 96 baytlık sabit başlık, SHA256d, Merkle kökü, compact 256 bit hedef (feat/fixed-header); ağ parametreleri (mainnet/testnet/regtest), deterministik genesis, `-network` bayrağı, kimlik ve imza alanının ayrılması (`QBTC-SIG-v4|<ağ>|<genesis özeti>`) (feat/genesis-params); emisyon takvimi `BlockSubsidy`/`SupplyAt` (feat/emission); FTL 300 sn, imzadan önce FTL kontrolü, gelecek tarihli bloklar için sınırlı bekleme kuyruğu (feat/timestamp-rules); blok indeksi, en çok birikmiş iş kuralı, undo kayıtlarıyla atomik reorg, geçersiz dal işaretleme, açılışta kurtarma, derin reorg alarmı (feat/chain-selection); 240 blok ertelenmiş coinbase ödemesi, olgunlaşan atanın indeksten bulunması (feat/coinbase-maturity); başlık öncelikli P2P senkronizasyonu, blok yayını, `/confirmations` API'si, bekleme kuyruğunun kaldırılması (feat/p2p-sync); zorunlu imzalı işlem son kullanma yüksekliği, imza alanı v4, işlem zaman damgasının kaldırılması (feat/tx-expiry); her blokta ASERT zorluk ayarlaması, BCHN test vektörleri, emisyon sapma simülasyonu (feat/lwma).

Kullanıcının verdiği uzlaşma kararları (2026-11-08, `devils-advocate` sonrası):
- Premine yok; genesis sıfır değerli coinbase + mesajdır, alıcısı mesaj özetinden türetilir.
- Emisyon (core/emission.go): 5 QBTC ilk ödül, 2.100.000 blokta (~4 yıl) bir yarılanma, 0,1 QBTC/blok kuyruk tabanı (~24. yıldan itibaren, yılda ~52.596 QBTC), ilk 20.000 blokta doğrusal başlangıç rampası. Yarılanma dönemi arzı 20.621.872,5 QBTC; sonra arz doğrusal büyür. İletişimde "tavan"/"21M max supply" denmez.
- Emisyon değerleri `Params.TargetSpacing`'den türetilir; blok süresi (60 ya da 30 sn) lansmandan önce ölçülüp dondurulur, zincir ortasında değiştirilmez.
- Blok süresi 60 sn; yayılım + doğrulama ölçümü ≤1–2 sn çıkarsa 30 sn'ye inebilir.
- PoW: SHA256d, Bitcoin'den farklı başlık düzeni.
- Zaman: FTL 300 sn mutlak (blok süresinin katı değil), MTP penceresi 11, yalnızca yerel saat (ağ ayarlı zaman yok). ASERT'te tek bloğun damgasını FTL kadar ileri atmanın kazancı 2^(FTL/yarı ömür) − 1 (~%2,9); `Validate` yarı ömrün en az 14·FTL olmasını denetler. Daha düşük FTL ancak senkronizasyon/reorg geldikten sonra.
- Zincir seçimi (core/chain.go): saf en çok birikmiş iş, reorg derinliğine uzlaşma sınırı yok, eşit işte ilk görülen kalır. `ReorgAlarmDepth` (100) üstü reorg zinciri değiştirir ama alarm verir (`DeepReorgAlarm`). Yan blok saklama sınırları (`MaxSideForkDepth`, `MaxBlocksPerHeight`) uzlaşma kuralı değil, DoS sınırıdır.
- Coinbase olgunluğu: 240 blok, ertelenmiş ödeme (H bloğunun ödülü + ücretleri H+M'de alacaklandırılır), `TargetSpacing`'den türetilir.
- İşlem süresi (core/transaction.go, feat/tx-expiry): `ExpiryHeight` zorunlu (>= 1, "süresiz" yok), kapsayıcı (h <= E geçerli), coinbase'de 0. Uzlaşmada azami ufuk yok (ufuk ömrü sınırlamaz, örtük "şu yükseklikten itibaren geçerli" kilidi yaratır; sabit zorlukta blok cinsinden ufuk saat garantisi vermez); azami ömür ve varsayılan süre mempool/cüzdan politikasıdır. Süre imzadan önce, iki geçişte denetlenir. İşlem zaman damgası kaldırıldı.
- Zorluk (core/asert.go, feat/lwma): her blokta ASERT (aserti3-2d tamsayı formülü), yarı ömür 2 saat (zaman cinsinden sabit), çapa 1. blok (1. ve 2. blok `Params.Bits`'i taşır; genesis çapa olsaydı genesis ile kazım başlangıcı arasındaki boşluk instamine verirdi), hedef yalnızca ebeveyne bağlı (bloğun kendi damgasına değil), blok başına ek değişim sınırı yok, powLimit ve 1'e kırpılır. Negatif çözüm süresi sorunu yok; MTP dışında önceki bloğa göre geçmiş sınırı yok. Testnet'te en düşük zorluk kuralı yok; regtest'te yeniden hedefleme yok (`NoRetarget`).
- Mainnet genesis'i şimdi sabitlenmez (erken gizli kazım = gizli premine). `MainnetParams` genesis'i boştur ve düğüm mainnet'i reddeder; genesis lansman anında öngörülemez bir mesajla (ör. o anki bir Bitcoin blok özeti) son uzlaşma kararı olarak üretilir.
- Zorluk tabanı (core/params.go, feat/launch-difficulty, `devils-advocate` sonrası): ağ başına `PowLimitBits`; ASERT ve yetim kapısı buna kırpar/denetler, `Validate` başlangıç hedefinin tabandan kolay olmamasını ister. Mainnet başlangıç hedefi ve tabanı genesis ile birlikte lansmanda seçilir: başlangıç, başlatıcının genesis ile kanıtladığı ve kamuya taahhüt ettiği hashrate'ten `BitsForHashrate` ile (kolay tarafa hata rampa sayesinde ucuz); taban başlangıçtan 2^12–2^16 kat kolay, ama 2^224'ten kolay değil (`mainnetMaxPowLimitBits`, uzlaşmada denetlenir) ve tek ucuz ASIC'in (~10 TH/s) gerektirdiğinin altında. Testnet tabanı başlangıçtan 2^8 kat kolay (0x2000ffff), regtest tabanı başlangıcı. 1. ve 2. bloğun sabit hedefi korunur. Taban spam'e ya da uzun menzilli yeniden yazıma karşı savunma değildir; mutlak ASERT kırpılınca açığı biriktirir.

Sırayla kalan önkoşullar ve açıklar (her biri ayrı PR, test + `guvenlik-denetimi` ile; yukarıda kararı verilmemiş bir uzlaşma seçimi çıkarsa uygulamadan önce `devils-advocate` ile sına ve kullanıcıya sor):
1. **Zorluk tabanı mekanizması kapandı (feat/launch-difficulty).** Kalanlar lansman adımında (madde 6). **En az zincir işi kapandı (feat/min-chain-work, kullanıcı kararı 2026-11-09):** `Params.MinChainWork` düğüm politikasıdır (uzlaşma kuralı değil; bkz. core/minwork.go): eşiğin altındaki başlık zincirinin gövdeleri indirilmez, eşiğin altındaki zincire duyurulan bloklar bırakılır, madenci eşiğin altında bekler ve uyarır, `/confirmations` `below_min_chain_work` döndürür; `AcceptBlock` ve kendi kazdığımız bloklar kapıya tabi değildir. Sürüm politikası: her sürümde ana zincirin 1–2 hafta önceki bir bloğunun birikmiş işi yazılır, bağımsız ikinci kişi doğrular; lansmanda 0. **Başlık ön-senkronizasyonu kapandı (feat/header-presync, core/presync.go):** oturum penceresi (`MaxSyncHeaders`, 20.000) ucun ve en az zincir işinin eşiğini geçmeden dolarsa başlıklar saklanmadan sabit bellekte doğrulanıp sayılır (son başlık, iş, son 12 ata, her pencere/4 başlıkta bir kontrol noktası özeti); eşik geçilince aynı eşten pencere boyunda parçalarla yeniden indirilir ve gövdeler yalnızca eşleşen kontrol noktasına kadar yetkilendirilir (özet tüm öneki bağlar: A'yı gösterip B verilemez); durum eş başına turlar arası korunur, kalıcı retlerde bırakılır; uç ön-senkronizasyonun işini geçerse yetki düşer. **Kalan mainnet engeli:** iş ya da zamana dayalı onay göstergesi; bunlar olmadan taban seçimi neredeyse önemsizdir: duraklama sonrası biriken açık taban maliyetli blok seli olarak boşalır ve blok sayan `/confirmations` saniyeler içinde anlamsızlaşır.
2. **Senkronizasyon kapandı (feat/p2p-sync).** Kalanlar: eş keşfi yok (yalnızca `-peer`), gelen bağlantının eşine geri bağlanılamaz; derin dal sınırı ön-senkronizasyonla kalktı (madde 1); ön-senkronizasyonda süren eş başına durum yalnızca yapılandırılmış eş sayısıyla sınırlı, kontrol noktaları zincir uzunluğu/5.000 kadar büyür; olgunlaşmamış bakiye API'si yok.
3. **Zorluk ayarlaması kapandı (feat/lwma).** Kalanlar: düşük zorluklu başlık spam'i artık belleği şişirmez ve diske yazdıramaz (pencere 20.000, ön-senkronizasyon sabit bellek) ama kötü niyetli yapılandırılmış eş tur başına `syncPeerBudget` kadar CPU harcatabilir; ebeveyni bilinmeyen blok ilan ettiği hedefle `checkHeader`'dan geçer ve senkronizasyonu tetikler (tetik birleşir); `ReorgAlarmDepth` (100) yarı ömre göre dondurulmalı; hızlı DAA selfish mining eşiğini düşürür, kiralık SHA256d hashrate'ine karşı hiçbir DAA %51 koruması vermez (iletişimde söylenmeli).
4. **Kilitsiz kazım kapandı (feat/mining-outside-lock).** `NewTemplate` (okuma kilidi) → `Mine` (kilitsiz, her 65.536 nonce'ta durdurma/uç denetimi ve zaman damgası tazeleme; hedef yalnızca ebeveyne bağlı olduğundan değişmez) → `SubmitBlock` (ağ bloklarıyla aynı doğrulama). `-mine <adres>` bayrağı madenci döngüsünü başlatır; kazılan blok yayılır, 10 sn sonra eşlere `getblock` ile sorulur, hiçbirinde yoksa uyarılır. Kalanlar: mempool olmadığından şablon yalnızca coinbase içerir; kabul denetimi aynı anda tek bloğa bakar (örnekleme); regtest'te sürekli kazım damgaları medyan+1 ile FTL'ye dayandırır (madenci bekleyip yeniden dener).
5. **Eş başına kotalar kapandı (feat/dos-quotas).** P2P'de eş, adres grubuyla tanınır (IPv4 /24, IPv6 /48; `network/quota.go`): grup başına 4 bağlantı, 1 çözümleme (ortak 8), 1 sunum yuvası (ortak 16; en çok 2 sn bekleme). Yanıtlar 64 KiB'lık parçalarla, parça başına 2 sn süreyle yazılır (~260 kbit/sn altında okuyan eş düşer). API'de bir /48 en çok 16 ayrı /64 kaydı alır, fazlası önekin ortak kovasını paylaşır; tablo doluyken yeni istemciler reddedilmez, ortak taşma kovasını (10 kat hız) paylaşır. Kalanlar: tahliye yok; 8 ayrı /24 ortak çözümleme yuvalarını tutup blok duyurularını ve istekleri aksatabilir, 16 grup (ya da tek bir IPv6 /44) sunum yuvalarını ve 64 gelen yuvayı doldurur (giden senkronizasyon etkilenmez; eş puanlama/tahliye gerekir); eşlerin paralel senkronizasyonu yok; Tor/ters vekil arkasındaki tüm gelen eşler tek grup sayılır.
6. Mainnet genesis'i lansmanda üretilip `MainnetParams`'a ve bağımsız test vektörüne eklenecek. Lansman kontrol listesi: (a) başlatıcı genesis'i ve 1.–2. bloğu aynı donanımla kazar, hashrate'ini kamuya açıklar; başlangıç hedefi `BitsForHashrate` ile bu hashrate'ten, taban ona oranla seçilir; (b) T_g ≥ genesis mesajındaki Bitcoin bloğunun zamanı ve yayın anı − T_g ≤ birkaç dakika (1. blok T_g+1'e damgalanabilir; boşluk 3. bloktan itibaren 2^(boşluk/2 sa) kat kolaylaşma ve ~boşluk/60 hızlı blok verir), bağımsız test vektörü bu şartları da sabitler.

Not: şema sürümü değiştiğinde (şu an "8") eski veri dizinleri açılmaz; silinip yeniden başlatılmalı.

Teknik borç:
- `/latestblock` tam bloğu döndürüyor (en fazla ~7 MiB); başlık uç noktası ve sayfalama eklenmeli.
- Mempool ve işlem yayını yok. Mempool geldiğinde: süresinin dolmasına K bloktan az kalan işlemi kabul etmemeli (Zcash 3; derin reorg kararı nedeniyle daha büyük K düşünülmeli), her blok ve geri alışta süreleri `tip+1`'e göre yeniden değerlendirip aynı göndericinin sonraki nonce'lu işlemlerini de çıkarmalı; varsayılan cüzdan süresi `ReorgAlarmDepth` ile tutarlı seçilmeli. Reorg süreli bir işlemi E'nin ötesine iterse işlem ve alıcının ona bağlı harcamaları düşer; alıcı için etkin kesinlik min(onay, E − dahil edilme yüksekliği).

<!-- model-routing:start -->
## Çalışma düzeni: model ve alt ajan yönlendirmesi

Ana oturum Sonnet'tir; Opus danışmandır (`advisorModel`). Ana bağlamı sade tut: ham arama sonuçları, uzun test çıktıları ve büyük dosyalar ana bağlama girmez.

**Opus'a danış (advisor):**
- Çok adımlı bir işe başlamadan önce planı danış ve plan onaylanmadan uygulamaya geçme. Planı doğru kurmak en değerli danışmadır; yanlış plan sonraki her adımı boşa çıkarır.
- Aynı hata iki denemede çözülmediyse veya hatanın nedeni belirsizse.
- İşi bitti ilan etmeden önce son kontrol için.

**Alt ajanlara devret:**
- `kod-arama` (Haiku): "X nerede tanımlı / nereden çağrılıyor / hangi dosyalar Y'yi kullanıyor" türü her arama. Ana oturumda geniş Grep/Glob taraması yapma; tek bir bilinen dosyayı okumak serbest.
- `test-kosucu` (Haiku): kod değişikliğinden sonra derleme, test ve lint. Başarısız test olursa teşhisi ana oturum yapar, gerekirse Opus'a danışır.
- `guvenlik-denetimi` (Opus, salt-okunur): para/bakiye, kimlik doğrulama, kriptografi, ağdan gelen veri veya uzlaşma mantığına dokunan her değişiklikten sonra ve her commit/PR'dan önce. Kodu yazan oturum kendi kodunu denetlemiş sayılmaz. ENGELLEYİCİ karar varsa commit etme.
- `devils-advocate` (Opus, salt-okunur): uzlaşma kuralı, arz/ödül, genesis, mainnet tarihi gibi geri dönüşü zor kararlardan önce kararın aleyhine en güçlü argümanı kurar.

**Alt ajan çıktısı:** Alt ajanlar yalnızca `dosya:satır — tek cümle` biçiminde yapılandırılmış sonuç döndürür; kod bloğu döndürmez. Ana oturum bu sonuçları özetleyip yeniden yapıştırmaz, gereken satırı kendisi okur.

**Bağımsız işler:** Birbirinden bağımsız aramaları veya denetimleri paralel başlat.
## Talimat dosyası ile kod çeliştiğinde

Talimat dosyaları (CLAUDE.md, AGENTS.md) ile mevcut kod bir kodlama geleneğinde (isimlendirme, loglama, kütüphane seçimi, dosya yapısı, hata yönetimi, test stili) çelişirse mevcut kodu esas al, çelişkiyi bana tek satırla bildir ve talimat dosyasını güncellemeyi öner.
İstisna: Bir kural mevcut kodun yanlış olduğunu ve düzeltilmesi gerektiğini açıkça söylüyorsa (güvenlik, para/bakiye mantığı, uzlaşma, DORA kuralları), o kural geçerlidir; kodu ona göre düzelt.
<!-- model-routing:end -->
