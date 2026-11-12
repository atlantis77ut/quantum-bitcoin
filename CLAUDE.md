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

## Mainnet öncesi kararlar ve açık kalanlar

Ayrıntılar, gerekçeler, kapatılan PR'lar ve her maddenin "Kalanlar"ı: [docs/MAINNET-ONCESI.md](docs/MAINNET-ONCESI.md). `core/`, `network/`, uzlaşma, arz, zorluk, zaman, mempool ya da P2P'ye dokunmadan önce o dosyayı oku. Yeni kapatılan işler ve kalanlar oraya yazılır, buraya değil; buraya yalnızca yeni dondurulmuş kararın tek satırı eklenir.

Dondurulmuş uzlaşma kararları (kullanıcı, `devils-advocate` sonrası; değiştirmek kullanıcı kararı ister):
- Premine yok; genesis sıfır değerli coinbase + mesaj. Mainnet genesis'i şimdi sabitlenmez (`MainnetParams` genesis'i boş, düğüm mainnet'i reddeder); lansmanda öngörülemez mesajla üretilir.
- Emisyon (core/emission.go): 5 QBTC ilk ödül, 2.100.000 blokta yarılanma, 0,1 QBTC/blok kuyruk, ilk 20.000 blok doğrusal rampa; iletişimde "tavan"/"21M max supply" denmez. Değerler `TargetSpacing`'den türetilir.
- Blok süresi 60 sn (ölçüm ≤1–2 sn ise lansmandan önce 30 sn), zincir ortasında değişmez. PoW SHA256d, Bitcoin'den farklı 96 baytlık başlık.
- Zaman: FTL 300 sn mutlak, MTP penceresi 11, yalnızca yerel saat.
- Zincir seçimi: saf en çok birikmiş iş, reorg derinliğine uzlaşma sınırı yok, eşit işte ilk görülen kalır; derin reorg alarmı ~2 saat (`TargetSpacing`'den), kalıcı, yalnızca `-clear-reorg-alarm` temizler.
- Coinbase olgunluğu 240 blok, ertelenmiş ödeme.
- İşlem süresi: `ExpiryHeight` zorunlu (>= 1), kapsayıcı, coinbase'de 0; uzlaşmada azami ufuk yok (politika mempool/cüzdanda).
- Zorluk: her blokta ASERT (aserti3-2d), yarı ömür 2 saat, çapa 1. blok, hedef yalnızca ebeveyne bağlı; ağ başına `PowLimitBits` tabanı, mainnet başlangıç hedefi ve tabanı lansmanda, hedef cinsinden taban = min(başlangıç · 2^16, 0x1d00ffff ≈ 2^224); referans donanım ölçülmüş açık kaynak GPU kazıcısı (ASIC değil).
- Düğüm politikaları (uzlaşma kuralı değil): `MinChainWork`, mempool kuralları, yan blok saklama sınırları, eş kotaları.

Sırayla kalan önkoşullar ve açıklar (her biri ayrı PR, test + `guvenlik-denetimi` ile; yukarıda kararı verilmemiş bir uzlaşma seçimi çıkarsa uygulamadan önce `devils-advocate` ile sına ve kullanıcıya sor). Madde 1–5 kapandı, kalanları docs dosyasında:
1. Zorluk tabanı, en az zincir işi, başlık ön-senkronizasyonu, iş/zaman onay göstergesi.
2. Senkronizasyon (eş keşfi yok, olgunlaşmamış bakiye API'si yok).
3. Zorluk ayarlaması (yapılandırılmış eş erteleme başına bir tam başlık doğrulaması harcatabilir; selfish mining/%51 iletişim notu: [docs/GUVENLIK-VARSAYIMLARI.md](docs/GUVENLIK-VARSAYIMLARI.md)).
4. Kilitsiz kazım.
5. Eş başına kotalar ve korunan gruplara ayrılmış yuvalar (eş kimliği/puanlama yok).
6. Mainnet genesis'i lansmanda üretilip `MainnetParams`'a ve bağımsız test vektörüne eklenecek. Lansman kontrol listesi: (a) lansmandan önce açık kaynak, Q-BTC başlığında ölçülmüş GPU ve çok çekirdekli CPU kazıcısı yayımlanır; başlatıcı genesis'i ve 1.–2. bloğu yalnızca GPU kazıcısıyla ve aynı donanımla kazar, yeniden üretilebilir hashrate'ini kamuya açıklar; başlangıç hedefi `BitsForHashrate` ile bu hashrate'ten, taban hedef cinsinden min(başlangıç · 2^16, 0x1d00ffff ≈ 2^224) seçilir; (b) T_g ≥ genesis mesajındaki Bitcoin bloğunun zamanı ve yayın anı − T_g ≤ birkaç dakika (1. blok T_g+1'e damgalanabilir; boşluk 3. bloktan itibaren 2^(boşluk/2 sa) kat kolaylaşma ve ~boşluk/60 hızlı blok verir), bağımsız test vektörü bu şartları da sabitler.

Teknik borç (ayrıntı docs dosyasında): `/latestblock` tam blok döndürür, `/block` her istekte bloğu diskten okur, işlem indeksi yok; mempool yer değiştirme spam'ine sınır yok, ceza ve hız sınırı adres grubuna bağlı.

Not: şema sürümü değiştiğinde (şu an "8") eski veri dizinleri açılmaz; silinip yeniden başlatılmalı.

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
