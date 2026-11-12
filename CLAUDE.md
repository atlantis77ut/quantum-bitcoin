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

Kapatılanlar (önkoşul PR'ları): 10^8 taban birim + işlem ücreti (feat/subunit-and-fees); 96 baytlık sabit başlık, SHA256d, Merkle kökü, compact 256 bit hedef (feat/fixed-header); ağ parametreleri (mainnet/testnet/regtest), deterministik genesis, `-network` bayrağı, kimlik ve imza alanının ayrılması (`QBTC-SIG-v3|<ağ>|<genesis özeti>`) (feat/genesis-params); emisyon takvimi `BlockSubsidy`/`SupplyAt` (feat/emission); FTL 300 sn, imzadan önce FTL kontrolü, gelecek tarihli bloklar için sınırlı bekleme kuyruğu (feat/timestamp-rules).

Kullanıcının verdiği uzlaşma kararları (2026-11-08, `devils-advocate` sonrası):
- Premine yok; genesis sıfır değerli coinbase + mesajdır, alıcısı mesaj özetinden türetilir.
- Emisyon (core/emission.go): 5 QBTC ilk ödül, 2.100.000 blokta (~4 yıl) bir yarılanma, 0,1 QBTC/blok kuyruk tabanı (~24. yıldan itibaren, yılda ~52.596 QBTC), ilk 20.000 blokta doğrusal başlangıç rampası. Yarılanma dönemi arzı 20.621.872,5 QBTC; sonra arz doğrusal büyür. İletişimde "tavan"/"21M max supply" denmez.
- Emisyon değerleri `Params.TargetSpacing`'den türetilir; blok süresi (60 ya da 30 sn) lansmandan önce ölçülüp dondurulur, zincir ortasında değiştirilmez.
- Blok süresi 60 sn; yayılım + doğrulama ölçümü ≤1–2 sn çıkarsa 30 sn'ye inebilir.
- PoW: SHA256d, Bitcoin'den farklı başlık düzeni.
- Zaman: FTL 300 sn mutlak (blok süresinin katı değil), MTP penceresi 11, yalnızca yerel saat (ağ ayarlı zaman yok). LWMA'nın N'si seçilince N·T/20 sınırına göre yeniden sınanır; daha düşük FTL ancak senkronizasyon/reorg geldikten sonra.
- Zorluk: her blokta LWMA (ASERT kabul edilebilir alternatif), aşağıdaki önkoşullardan sonra.
- Mainnet genesis'i şimdi sabitlenmez (erken gizli kazım = gizli premine). `MainnetParams` genesis'i boştur ve düğüm mainnet'i reddeder; genesis lansman anında öngörülemez bir mesajla (ör. o anki bir Bitcoin blok özeti) son uzlaşma kararı olarak üretilir.

Sırayla kalan önkoşullar ve açıklar (her biri ayrı PR, test + `guvenlik-denetimi` ile; yukarıda kararı verilmemiş bir uzlaşma seçimi çıkarsa uygulamadan önce `devils-advocate` ile sına ve kullanıcıya sor):
1. **Lansman adilliği:** mainnet başlangıç hedefi gerçekçi hashrate tahminine göre sert seçilmeli (rampa tek başına yetmez).
2. **Zincir seçimi / reorg / senkronizasyon yok (YÜKSEK).** Düğüm aynı yükseklikte ilk gördüğü bloğu kalıcı kabul ediyor, eşlerden eksik blokları çekemiyor, aldığı bloğu yaymıyor. Başlık indeksi, en çok birikmiş iş kuralı, onay sayısı ve coinbase olgunluğu gerekli. Gelecek tarihli başlık asla "geçersiz" işaretlenmemeli; geçici bekleme kuyruğu (core/pending.go) senkronizasyonla değiştirilmeli.
3. **Zorluk ayarlaması yok (KRİTİK).** Hedef ağ parametresinde sabit; LWMA 2'den sonra. Negatif çözüm süresi ve önceki bloğa göre geçmiş sınırı LWMA PR'ında karara bağlanmalı. LWMA PR'ına emisyonun takvimden sapma simülasyonu eklenmeli. Bu yapılmadan madencilik döngüsü `main`'e eklenmeyecek.
4. Madencilik yazma kilidinin dışında yapılmalı (`AddBlock` kazım sırasında zinciri kilitliyor); kazım uzarsa zaman damgası periyodik tazelenmeli, eşler bloğu kabul etmezse madenci uyarılmalı.
5. DoS: `network/api.go` IPv6 /48 hız sınırı tablosu şişirilebilir; `network/p2p.go` bağlantı yuvaları tüketilebilir.
6. Mainnet genesis'i lansmanda üretilip `MainnetParams`'a ve bağımsız test vektörüne eklenecek.

Not: şema sürümü değiştiğinde (şu an "3") eski veri dizinleri açılmaz; silinip yeniden başlatılmalı.

Teknik borç:
- `/latestblock` tam bloğu döndürüyor (en fazla ~7 MiB); başlık uç noktası ve sayfalama eklenmeli.
- Mempool ve işlem yayını yok.

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
