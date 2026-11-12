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

Kullanıcı kararı bekleyen uzlaşma maddeleri (karar verilmeden uygulama yapma; önce `devils-advocate` ile sına):
1. **Zorluk ayarlaması yok (KRİTİK).** Zorluk 4'te sabit; saldırgan hesaplama gücü kadar hızlı blok üretip sınırsız QBTC basabilir. Hedef blok süresi ve yeniden hedefleme algoritması seçilmeli. Bu karar verilmeden madencilik döngüsü `main`'e eklenmeyecek.
2. **Genesis deterministik değil.** `time.Now()` yüzünden her düğüm farklı zincir başlatıyor; sabit genesis zaman damgası ve özeti gerekli. Genesis alıcısı `QBTC_SATOSHI_QUANTUM` geçerli bir adres değil, 21M QBTC harcanamaz durumda. Genesis sabitlenince özeti `ChainID` ile birlikte imza alanına eklenmeli.
3. **Zincir seçimi / reorg / senkronizasyon yok (YÜKSEK).** Düğüm aynı yükseklikte ilk gördüğü bloğu kalıcı kabul ediyor, eşlerden eksik blokları çekemiyor, aldığı bloğu yaymıyor. Borsaya karşı çift harcamaya açık; en çok iş (most-work) kuralı ve onay sayısı gerekli.

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
