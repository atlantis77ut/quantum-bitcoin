# Eş kimliği ve puanlama: tasarım notu

**Durum:** kararlar verildi (kullanıcı, 2026-11-13, `devils-advocate` sonrası; bkz. §7), uygulama PR'ları bekliyor. Bu not hiçbir uzlaşma kuralını değiştirmez; konu düğüm politikası ve P2P taşımasıdır. Kararlar verilince kapanış ve kalanlar [MAINNET-ONCESI.md](MAINNET-ONCESI.md)'ye yazılır.

## 1. Kimlik neyi yapabilir, neyi yapamaz

**Kimlik yaratmak bedavadır.** Yeni bir anahtar ya da yeni bir bağlantı açmak saldırgana hiçbir şeye mal olmaz. Bu yüzden:

- **Adres grubu dış kaynak sınırı olarak kalır.** Kimlik onun yerini alamaz. Grup başına bağlantı, çözümleme, sunum ve işlem hızı sınırları korunur; kimlik bu sınırların içinde çalışır.
- **Kimliğin kazandırdığı yalnızca iki şey:**
  - (a) Kazanılmış itibar, kötü davranan komşulardan etkilenmez.
  - (b) Ceza daraltılabilir: kötü davranan *yerleşik* kimlik cezalandırılır, bütün grup değil.
- **Yeni (itibarsız) kimlik, grubunun bütçesini ve ceza havuzunu paylaşmak zorundadır.** Aksi hâlde cezayı kimliğe daraltmak, tek bir /24'ten sınırsız yeniden deneme verir.
- **Kimlik, çok sayıda /24 kontrol eden saldırgana karşı bir şey yapmaz.** Bu bir eclipse ve çeşitlilik sorunudur; eş keşfi notunun ([ES-KESFI.md](ES-KESFI.md)) konusudur.
- **Bugün komşudan zarar görenler yapılandırılmamış gelen eşler ve yedek kullanırken istemci olarak düğümün kendisidir:**
  - Yayın etkin eşlere gider: yapılandırılmış eşler ve onlara ulaşılamazken en çok 4 yedek (`ps.activePeers()`, backup.go). Yedek kullanılırken düğüm istemci olarak da etkilenir: aynı gruptaki komşu, yedeğin bizim gruba ayırdığı kotayı doldurabilir ([ES-KESFI.md](ES-KESFI.md) §4.4).
  - Yapılandırılmış grup hiç geri çevrilmez (peers.go:185).

## 2. Bugün adres grubuna (IPv4 /24, IPv6 /48) bağlı olanlar

| Ne | Sınır | Aynı /24'teki ya da aynı Tor çıkışındaki saldırgan bugün ne yapabilir |
|---|---|---|
| `connQuota` (quota.go:18) | Grup başına 4 açık gelen bağlantı. | Grubun bağlantılarını doldurup dürüst komşunun bağlanmasını engeller. |
| `decodeQuota` (quota.go:19) | Grup başına 1 eşzamanlı çözümleme (2 sn bekleme). | Yavaş gönderimle yuvayı tutar; komşunun duyurusu bekler ya da düşer. |
| `serveQuota` (quota.go:20) | Grup başına 1 eşzamanlı sunum. | Komşunun isteklerini geciktirir. |
| `txGate` (quota.go) | 64 işlem/sn, 256 ani (grup); geçersiz işlem 10 dk ceza (iç anahtar, 3 iç anahtarda grup). | Kovayı tüketip komşunun işlemlerini cezasız düşürür; 3 adresle bütün grubu 10 dk keser. Tek geçersiz işlemle komşuyu kesemez (Yol A adım 3). |
| `prot.discourage` (peers.go) | Geçersiz blok ya da bozuk JSON → iç anahtar 1 sa; `ErrBadDifficulty` kademeli; 3 iç anahtarda grup 1 sa. | 3 adresle bütün grubu 1 sa dışarıda bırakır. Tek bozuk mesajla komşuyu ya da sürüm farkıyla dürüst eşi hemen düşüremez (Yol A adım 3). |
| `protection.recent` (peers.go) | Yeni uç bloğunu ilk getiren son 8 eş korunur; grup başına bir yer. | Korumayı bloğu getiren iç anahtar kazanır, komşu yararlanmaz (Yol A adım 3). |
| Ayrılmış yuvalar (peers.go:40-42) | Çözümleme 3/8, sunum 6/16, bağlantı 16/64. | Korunan grubun payını komşu da kullanır. |

**Gruba değil, eş adresine bağlı olanlar:** `presyncBackoff` ve `presyncs` (p2p.go, `P2PServer` alanları), `lastMempoolSync` (mempoolsync.go:33). Gönderen kovası (relaybudget.go) bütün eşler için ortaktır.

**Taşıma:**
- Düz TCP, şifreleme yok (p2p.go `request` ve `broadcast`).
- Her mesaj ayrı bir bağlantıdır.
- `message` yapısında sürüm ya da yetenek alanı yoktu (Yol A'nın 1. adımıyla eklendi).
- `liboqs-go` (go.mod:6) yalnızca ML-DSA imzası için kullanılıyor; KEM kullanılmıyor.

## 3. Taşıma: neyi gerektirir, neyi gerektirmez

**Anahtar tabanlı bir kimliği mesajlara güvenle bağlamak, bir oturum katmanı gerektirir.** Şifrelenmemiş bağlantıda taşıyıcı jeton, yol üstündeki saldırgana açıktır. Mesaj başına ML-DSA imzası pahalıdır ve yeni bir DoS yüzeyi açar. KEM ile türetilmiş anahtar ve MAC ise zaten bir oturum katmanı demektir.

**Ancak §2 tablosundaki her satırın tehdidi, yol dışındaki komşu kiracıdır.** Bu tehdit için oturum gerekmez: /24 (/48) dış sınırının altında **tam adres (/32; IPv6'da /64) iç anahtar** olarak kullanılırsa ceza ve koruma yeni bir taşıma olmadan daraltılabilir.
- Ek bir IP, saldırgana para ve aynı /24'te yer bulma zahmeti demektir.
- Çözmediği durumlar: Tor çıkışı ve CGNAT. Ancak orada da yeni kimlik zaten grubun itibarsız payını paylaşırdı (§1).

**Kalıcı oturumların sanıldığı kadar çözmedikleri** (`devils-advocate` incelemesi, kodla doğrulandı):
- **Yetim tamponu yine gerekir.** Öncül başka bir eşten, bloktan, POST /tx'ten ya da havuz eşitlemesinden geç gelebilir (orphans.go:11-17). Tek bir TCP akışı yalnızca o oturumun içindeki sırayı korur.
- **Kara delik maliyetinin kökü bağlantı modeli değildir.** `broadcast`, global `sendMu` kilidini tutarken bütün eşleri `wg.Wait` ile beklerdi. Eş başına gönderim kuyruğu bu maliyeti oturumsuz kaldırır (Yol A'nın 2. adımıyla kapandı, network/peersend.go).
- **Tahliye bir kazanım değil, oturumun doğurduğu bir gereksinimdir.** Bugün bir yuva okuma süresi dolunca kendiliğinden boşalır. Kalıcı oturumda yuva süresiz tutulabilir; boşta kalma süresi, asgari hız ve tahliye kuralı bu yüzden gerekir.
- **Paralel senkronizasyon taşımadan bağımsızdır.** `request` durumsuz bir istek/yanıttır.

**Kalıcı oturumların gerçek kazanımları:**
- Gelen eşle ve NAT arkasındaki eşle iki yönlü konuşma.
- Paylaşılan bir IP'de (Tor, CGNAT) /32'nin ayıramadığı eşleri ayıran kimlik.

**Kalıcı oturumların yeni riskleri:**
- **Süresiz yuva tutma ve yavaş okuma.** Bugün bu saldırı sürekli yeniden bağlanma ister; oturumda bedavaya gelir.
- **Kafa hattı engellemesi.** Büyük bir blok yanıtı ya da uzun bir `AcceptBlock`, aynı oturumdaki duyuruları ve ping'leri bekletir.
- **Bellek ve geri basınç.** Oturum başına yazma kuyrukları gerekir. Gelen eşe tam blok geri yayını, inv/getdata olmadan ağır bant genişliği harcar.
- **Kalibrasyonun geçersizleşmesi.** `maxTxsPerMsg`, eş başına tek uçuştaki mesaj (eskiden `sendMu`), gönderen kovasının 7/8 ve 1/2 payları ile kota ve yuva sayıları tek mesajlık modele göre seçildi.
- **Takvim.** Blok süresinin 60 ya da 30 sn olacağı kararı, lansmandan önceki yayılım ölçümüne bağlıdır. Taşıma bu ölçümü değiştirir.

**Lansmandan sonra geçiş mümkündür.** Karar uzlaşma dışıdır ve JSON çözücüsü bilinmeyen alanları yok sayar. Bugün mesaja bir sürüm ya da yetenek alanı eklenirse, oturumlara geçiş daha sonra sürüm pazarlığıyla yapılabilir. Bunu engelleyen somut bir neden bulunmadı.

## 4. Yol A (önerilen): lansmandan önce oturumsuz, küçük PR'lar

Her biri ayrı PR, test ve `guvenlik-denetimi` ile:

1. **Sürüm ve yetenek alanı** (kapandı: feat/message-version; geçiş kuralları `network/p2p.go`'daki `protocolVersion` belgesinde).
   - `message`'a eklenir; eski düğümler alanı yok sayar.
   - Kalıcı oturum seçeneğini lansmandan sonrası için açık tutar.
2. **Eş başına gönderim kuyruğu** (kapandı: feat/peer-send-queue; ayrıntı `network/peersend.go` belgesinde).
   - `sendMu` ve `wg.Wait` yerine geçer.
   - `sendMu`'nun amacı korunmalıdır: karşı düğümün tek çözümleme yuvası (`decodesPerGroup=1`) nedeniyle eş başına aynı anda en çok bir mesaj gider, bloklar işlem gruplarının önüne geçer.
   - Yavaş ya da ulaşılamayan eş için devre kesici eklenir. Kara delik maliyeti diğer eşlere yansımaz.
3. **İç anahtar** (kapandı: feat/inner-key, kademeli `ErrBadDifficulty` ile birlikte; ayrıntı `network/innerkey.go` ve `network/peers.go` belgelerinde).
   - `txGate` cezası, `discourage` ve blokla kazanılan koruma, /24 (/48) dış sınırının altında /32 (/64) iç anahtara bağlanır.
   - Yeni iç anahtar grup bütçesini paylaşır; yalnızca ceza ve koruma daralır.
   - Bir pencerede *k* farklı iç anahtar cezalanırsa bütün grup cezalanır.
   - Bir /48 sahibi bedavaya ~65.000 /64 alır. Bunu *k* yükseltmesi ve dış sınırlar bağlar; durum bugünden kötü değildir.
   - `ErrBadDifficulty` 1 sa geri çevirme yerine kademeli cezalandırılır (sürüm farkıyla dürüst eş düşmesin).
4. **Gelen ve NAT arkasındaki eşe ulaşma** (kapandı: feat/tip-longpoll, uzun yoklama seçildi; ayrıntı `network/tippoll.go` belgesinde). İki seçenek vardı:
   - Yalnızca yapılandırılmış eşlere giden kalıcı bağlantı (seçilmedi).
   - "Yeni uç olunca yanıtla" uzun yoklama isteği (seçildi: `waittip`/`tip`, protokol sürümü 2; bekleyen istekler ayrı, paylı ve grup başına kotalı bir yuva havuzunda durur).
   
   İkisinde de gelen tarafta oturum açılmaz, tahliye gerekmez.

**İtibar kaynağı değişmez.** Koruma, *ucumuzu uzatan yeni bloğu ilk getiren* iç anahtara verilir ("hızlı ileten kazanır"). İşlem iletmek itibar kazandırmaz, çünkü başkasının geçerli işlemini iletmek saldırgana bedavadır. Bu kaynağı genişletmek ayrı bir kullanıcı kararıdır.

## 5. Yol B: kalıcı oturumlar (giriş koşullarıyla)

B seçilirse, oturum açılmadan önce şunlar sağlanmalıdır:

- **Ara durum olmamalı.** Taşıma, yayın ve senkronizasyonun taşınması ve kotaların uyarlanması tek PR olmalı ya da aralarında oturum açılmamalı. Aksi hâlde eski kotalarla süresiz tutulan bağlantılar savunmasız bir ara durum yaratır.
- **İlk günden gelmesi gerekenler:**
  - boşta kalma ve asgari hız süreleri, keepalive;
  - tahliye (en az değerli itibarsız oturum);
  - oturum başına sınırlı yazma kuyruğu ve tek kodlanmış paylaşılan tampon.
- **Kafa hattı engellemesine karşı yazılı tasarım:** inv/getdata ya da kontrol ve veri akışlarının ayrılması.
- **İki yığın birlikte çalışırken:** kotalar ortak olmalı ve sürüm düşürmeye izin verilmemeli.
- **El sıkışma:**
  - Ev yapımı değil, incelenmiş bir kalıp (Noise benzeri) kullanılmalı ve bir anahtar yönetimi politikası olmalı.
  - Mevcut kotaların arkasında çalışmalı; ucuz denetimler önce yapılmalı.
- **Düğüm anahtarı türü** bu yolun alt kararıdır: post-kuantum (ML-DSA-65, ~5 KiB el sıkışma) ya da klasik (yeni bağımlılık).
- **İtibarın kalıcılığı:** bellek mi, LevelDB mi? LevelDB'de atomik batch kuralı geçerli olur ve şema sürümü ("10") artabilir.

## 6. Karar soruları

1. **Yol ve takvim.** Seçenekler:
   - **A, lansmandan önce (önerilen).** 60/30 ölçümü bugünkü taşımayla yapılır; B'ye sürüm alanı sayesinde sonra geçilebilir.
   - **B, lansmandan önce.** Yayılım ölçümü ve dolayısıyla 60/30 kararı yeni taşımayı bekler; lansmanın kritik yoluna en az birkaç büyük PR girer.
   - **B, lansmandan sonra.** 60/30, eski taşımanın ölçümüyle dondurulur.
2. **İç anahtar ayrıntısı ve *k*.** Öneri: /32 (IPv6 /64) ve 10 dk'lık pencerede k = 3 farklı iç anahtar.
3. **İtibar kaynağı.** Mevcut kural (yeni uç bloğunu ilk getiren) aynen kalsın mı? Öneri: evet.
4. **`ErrBadDifficulty` cezası.** 1 sa geri çevirme yerine kademeli olsun mu? Öneri: evet (ilk ihlalde uyarı, tekrarında artan süre).

## 7. Kararlar (kullanıcı, 2026-11-13)

1. **Yol A, lansmandan önce.** Oturumsuz küçük PR'lar §4'teki sırayla yapılır. 60/30 sn yayılım ölçümü bugünkü taşımayla yapılır. Kalıcı oturumlar (Yol B) lansmandan sonra, sürüm alanı üzerinden pazarlıkla ele alınabilir.
2. **İç anahtar ve yükseltme:**
   - İç anahtar /32 (IPv6 /64), dış sınır /24 (/48).
   - 10 dk'lık pencerede 3 farklı iç anahtar cezalanırsa bütün grup cezalanır.
3. **İtibar kaynağı aynen kalır.** Yeni uç bloğunu ilk getiren korunur; işlem iletmek itibar vermez.
4. **`ErrBadDifficulty` cezası kademeli olur:** ilk ihlalde uyarı, tekrarında artan süre.
