# Eş keşfi: tasarım notu

**Durum:** kararlar verildi (kullanıcı, 2026-11-14, `devils-advocate` sonrası; bkz. §7). Önkoşul olan itme taşıması düzeltmesi kapandı (§4.5); adres öğrenme PR'ı sırada (§5). Bu not hiçbir uzlaşma kuralını değiştirmez; konu düğüm politikası ve P2P taşımasıdır. Kapanışlar ve kalanlar [MAINNET-ONCESI.md](MAINNET-ONCESI.md)'ye yazılır.

## 1. Sorun

- **Düğüm bugün yalnızca `-peer` ile verilen eşleri bilir.** Ağ elle yapılandırmayla büyür. Yeni bir düğüm, birinin adresini bilmeden katılamaz. Yapılandırılmış eşlerinin hepsine ulaşamayan düğümün başka yolu yoktur.
- **Taşıma oturumsuzdur.** Bağlantı başına tek mesaj gider; "eş", istek açtığımız adrestir (Yol A, [ES-KIMLIGI.md](ES-KIMLIGI.md) §7). Keşif bu modelin içinde kalır.

## 2. Tehdit modeli

- **Eclipse.** Düğümün bütün giden eşleri saldırgandır.
  - Zincir seçimi en çok birikmiş iştir ve senkronizasyon her eşin başlıklarını tam doğrular. Bu yüzden tek bir dürüst giden eş nihai doğruluk için yeter.
  - Zamanında olmak için yetmez. Döngü eşleri sırayla gezdiği için araya giren kötü eşler dürüst eşi seyrek gösterir; çift harcama penceresi bu ölçektedir.
- **Adres zehirleme.** Çok adresi olan saldırgan tabloyu doldurur ve giden eş seçimini ele geçirir.
  - Kova sınırları sayıyı değil oranı korur. Lansmanda dürüst dinleyici nüfusu küçükken birkaç /16'lık saldırgan, Bitcoin benzeri bir tabloda keşfedilmiş yuvaların çoğunu kazanabilir (8 /16 ile ~%40, 32 /16 ile ~%90; tahmin).
- **Üçüncü taraf yansıması, topoloji çıkarımı, kaynak tüketimi.**
  - Sahte duyurularla başkasının IP'sini arattırmak.
  - `getaddr` kazıyarak kim kime bağlı çıkarmak.
  - Adres mesajı seli.
- **Senkronizasyon döngüsünü meşgul etmek.**
  - Döngü sıralıdır. Kötü niyetli bir eş kendi sırasını PoW harcamadan ~1–3 dk tutabilir (bağlanıp susmak, başlık aşamasının süre bütçesi).
  - Uç beklemesinde ebeveyni bilinmeyen başlık PoW'dan önce, bedavaya reddedilir. Bu yüzden "öncelik" ya da tetik olarak kullanılamaz.
- **Tohumun gücü.** Tohumu işleten, yeni düğümlerin ilk eş kümesini belirler.
  - Lansmanda bağımsız işletmeci yoktur. Tohumu başlatıcı işletirse, genesis'i ve ilk blokları kazan, en büyük erken madenci olan taraf aynı zamanda topolojinin merkezi olur (yarışlarda γ, [GUVENLIK-VARSAYIMLARI.md](GUVENLIK-VARSAYIMLARI.md)).
  - DNS kimlik doğrulamasızdır. DORA kapsamındaki bir borsa için varsayılan açık bir DNS tohumu belgelenmesi gereken bir dış bağımlılıktır.
- **NAT arkasındaki madenci.** Ona kimse bağlanamaz; bloğu yalnızca ittiği eşlere ulaşır. İtme taşıması zayıfsa blok hiç yayılmaz (§3.1).

## 3. `devils-advocate` bulguları (tasarımı değiştirenler)

### 3.1 İtme taşıması (keşiften bağımsız; feat/push-transport ile kapandı, §4.5)

- **İtilen mesaja iki tarafta da tek bir 10 sn süre verilir.**
  - Gönderende `sendTo` mesajın tamamını tek bir yazma süresiyle yazar.
  - Alıcıda `handleConnection` tek bir okuma süresiyle çözümler; çözümleme yuvası için de ayrıca 10 sn bekletebilir.
- **Sonuç: 7,2 MiB'lık dolu blok, eş başına ~6 Mbit/sn'den yavaş bağlantıdan itilemez.**
  - Kopyalar paralel gider. Ev bağlantısından birkaç eşe itilen dolu blok hiçbirine ulaşmayabilir.
  - Başarısız blok itmesi yeniden kuyruğa girmez.
- **Saldırgan dürüst blokları ucuza doldurabilir.**
  - Bedeli: en düşük ücretle 1.000 işlem, blok başına ~0,01 QBTC; gönderen başına havuz sınırı yüzünden ~40 fonlu hesap gerekir.
  - Sonuç: ev madencilerinin blokları seçici olarak kaybolur.
- **Korumasız alım payı küçüktür.** Açık çözümleme payı 5'tir, grup başına 1. 5 grup yavaş gönderimle korumasız itmeleri cezasız düşürebilir.
- Lansmanda bloklar boş olduğu için sorun testte görünmez. 60/30 sn yayılım ölçümü dolu blokla ve bu düzeltmeden sonra yapılmalıdır.

### 3.2 Birincil keşif rolünün bedeli

- **Keşfedilmiş eşler taşımanın pahalı yollarına bağlanır:** blok başına bir gövde itmesi, bir senkronizasyon sırası, bir havuz çekmesi. Maliyet eş sayısıyla doğrusal büyür; Bitcoin'deki kalıcı bağlantıdaki gibi bedava değildir.
- **Eş değiştirme ölçütü taşıma hatalarını saymamalıdır.**
  - Dolu sunucu yanıt vermeden kapatır. Eski sürüm bilinmeyen türü yanıtsız bırakır. Uç beklemesinde "meşgul" hata sayılmaz.
  - Bu durumları sayan bir ölçüt, saldırgana dürüst eşleri kendi kayıtlarıyla değiştirtir ve sürüm geçişinde eski düğümleri bozuk işaretletir.
  - Bozuk işaret yalnızca durumdan bağımsız, kanıtlı ihlalde konur: `neverHonestBlock`, `malformedMessage`, yanlış ağ. `headerRejected` kullanılmaz; o `ErrBadDifficulty`, `ErrBadPrevHash` ve `ErrKnownInvalid`'i de içerir.
- **Ön-senkronizasyon durumu eşin adres dizgisine bağlıdır.**
  - Eş değiştikçe her yeni eşle ayrı bir ön-senkronizasyon başlar.
  - Değiştirilen eşin ertelemesi budanırsa, "erteleme başına bir tam doğrulama" sınırı operatörün seçtiği eş sayısından tablodaki kayıt sayısına kayar.
- **30 dk'lık bayat uç kuralı eclipse'i yakalamaz.** Azınlık dalını besleyen saldırgan ucu ilerletmeye devam eder; ASERT o dalı saatler içinde kolaylaştırır.
- **Çapalar var olan bir eclipse'i yeniden başlatmaya taşır.** Zincir dizininde dururlarsa kopyalanan her dizinle çoğalırlar.
- **"Sende var mı" sorusu oyunlanabilir.**
  - Hep "yok" diyen eş her blokta 7,2 MiB çeker.
  - Hep "var" diyen eş, madencinin "blok hiçbir eşte yok" uyarısını susturur.
  - Madencinin kabul denetiminde gövde kanıtı (`PeerHasBlock`) kalmalıdır.

### 3.3 Kalıcılık

- Zincir dizini şema değişiminde silinir (son PR'larda 8→9→10) ve en çok kopyalanan dizindir (yedek, imaj, hızlı eşitleme).
- Adres tablosu orada durursa her şema değişiminde herkes aynı gün tohuma döner. Gizli anahtar ve çapalar da her kopyada aynı olur.

## 4. Karara bağlanan tasarım: yedek rol

### 4.1 Güven kümeleri

- **Yapılandırılmış eşler** birincildir: korunur, her turda senkronize edilir, itme alır, `protectPeer`/`addConfigured` yalnızca onlar içindir.
  - `-peer` verilmezse gömülü başlangıç listesi (§4.6) yapılandırılmış eş yerine geçer.
  - `-peer` verilirse gömülü liste yedek havuzuna katılır.
- **Keşfedilmiş eşler** yedektir: korumasızdır, az sayıdadır (en çok 4, /16 ve IPv6 /32 başına bir).
  - Yalnızca yapılandırılmış eşlerin hepsine ulaşılamıyorken devreye girerler. Ölçüt: belirli bir süre boyunca hiçbir yapılandırılmış eşe hiçbir isteğin başarılı olmaması.
  - Biri yeniden ulaşılır olunca, gecikmeli bir eşikle (histerezis) çıkarılırlar.
  - Etkinken senkronizasyon, uç beklemesi, blok ve işlem itmesi ve havuz eşitlemesi onları da kapsar. NAT arkasındaki madencinin bloğu bu durumda da yayılır.
- **`-discover=0` keşfi tamamen kapatır** (borsa profili): yalnızca yapılandırılmış eşler kullanılır, `getaddr` sorulmaz.

### 4.2 Adres kaynağı

- **Adres yalnızca güvenilen eşlerden öğrenilir:** yapılandırılmış eşler ve gömülü liste. Kendini duyurma, besleyici yoklaması ve `new`/`tried` tablosu yoktur (lansmandan sonra, birincil rol için; §6).
- **`getaddr` → `addr`** (istek/yanıt, protokol sürümü 3).
  - Yanıtlayan, kendi yapılandırılmış eşlerinden son dönemde başarıyla ulaştıklarını verir: operatörlerin seçtiği adresler, bir atlama.
  - Yanıt sınırlıdır (en çok 32 adres) ve genesis özeti taşır. Yanlış ağın yanıtı reddedilir, eşi bozuk işaretlenir.
  - Kendiliğinden gelen `addr` itmeleri yok sayılır. Yalnızca yönlendirilebilir adresler alınır (regtest hariç). Kaynak başına kayıt sayısı sınırlıdır.
- **Ağ başına varsayılan port** ayrılır; testnet ve mainnet düğümleri birbirinin tablosuna karışmaz.

### 4.3 Tablo ve kalıcılık

- **Küçük tablo** (en çok 256 kayıt). Her kayıt adres, kaynak, son başarı, son başarısızlık ve bozuk işaretini tutar.
- **Bozuk işaret** yalnızca §3.2'deki kanıtlı ihlallerde konur ve 24 sa sürer. Taşıma hataları (EOF, zaman aşımı, meşgul) yalnızca yedek eşi sırayla değiştirir, işaret koymaz.
- **Kalıcılık: `network/` paketinin sahibi olduğu ayrı bir LevelDB dizini** (teknik öneri).
  - Tablo LevelDB batch'iyle atomik yazılır.
  - Zincirin şema silinmesinden etkilenmez, kopyalardan dışlanabilir, `core/`'a API gerektirmez.
  - Bedeli: ikinci bir veritabanı tanıtıcısı ve yol kuralı (`-db` ile birlikte).

### 4.4 Döngülerle bağlantı

- **Eş başına durum kendi sahibinde temizlenir.**
  - `presyncs`, `presyncBackoff` ve `lastMempoolSync` senkronizasyon döngüsünündür; tur başında güncel eş kümesine göre budanır.
  - Ertelemeler IP ya da iç anahtarda tutulur ve yedek eş değişince sıfırlanmaz.
  - Uç bekleme döngüleri eş başına durdurma kanalıyla durur.
  - Gönderici kuyrukları silinir, çalışanları boşaltılır.
- **Tetik tabanı eş başınadır.** Yapılandırılmış eşlerin tetiği bugünkü gibi kalır. Keşfedilmiş eşte ebeveyni bilinmeyen başlık tetik ya da öncelik vermez.
- **Uç beklemesi:** art arda N "meşgul" yanıtında yedek eşin bekleme yuvası başka eşe geçer.
- **`hasPeers`** (yayın bütçesi) etkin giden eşlerin tamamını sayar. Aksi hâlde yalnızca yedekle çalışan düğüm işlemleri sessizce atar.
- **Madencinin kabul denetimi** gövde kanıtıyla kalır.

### 4.5 İtme taşıması düzeltmesi (önkoşul; kapandı: feat/push-transport, `network/push.go`)

- **Alıcı:** okuma süresi her okumadan önce tazelenir ve üç sınırın en erkenidir:
  - şimdi + 10 sn (canlılık);
  - başlangıç + 10 sn + okunan bayt / 128 KiB/sn (asgari ortalama hız, ~1 Mbit/sn; ilk 10 sn eskisi gibi bedava);
  - başlangıç + 60 sn (tavan).

  Başlangıç çözümleme yuvası alındıktan sonradır. Hıza ya da tavana takılan bağlantı bozuk sayılmaz, eş geri çevrilmez. Çözümleme yuvası grup başına 1 kalır; yuvayı 10 sn'den uzun tutmak yuva başına ~1 Mbit/sn sürekli gönderim ister (quota.go belgesi).
- **Gönderen:**
  - Parça (64 KiB) başına 14 sn yazma süresi: alıcının en uzun yuva beklemesinden (2 + 10 sn) uzundur, çünkü alıcı yuva beklerken tampon dolar. Toplam en çok 75 sn.
  - Erken başarısız blok itmesi 2 sn sonra bir kez yeniden denenir. Erken: bağlantı kurulamadı ya da en çok 2 MiB yazılmışken koptu (ör. alıcının yuvası doluydu ve okumadan kapattı). İki uçtaki çekirdek tamponlarına sığan küçük itme gönderende hiç başarısız görünmez; o eşe blok uç beklemesi ya da senkronizasyonla gider. Koşullar: eşin son gönderimi başarılı, kuyrukta daha yeni blok yok (beklemeden önce ve sonra bakılır). Geç kopan itme denenmez.
  - **Kademeli itme bütün bloklar içindir**, yalnızca kendi bloğumuz için değil: düğüm aynı anda en çok 2 blok gövdesi iter.
    - Yuva bekleyen gönderim, tutanlar parça yazdıkça bekler.
    - 2 sn hiç parça yazamayan ya da dürüst alıcının isteyeceği hızın gerisinde kalan tutanın yuvasını bekleyenlerden yalnızca biri devralır. Böylece her takılan eş eşzamanlılığı en çok bir artırır.
    - Dolu blok için iki kopyada ~2 Mbit/sn yukarı bant alıcıların asgari hızını karşılar.
  - Senkronizasyonla ya da uç beklemesiyle bir eşten indirilen blok o eşe geri itilmez. Gelen bağlantıyla itilen blok kaynağına geri itilebilir (yankı): kaynak portu geçicidir, IP ile eşleştirmek aynı IP'deki (aynı makine, NAT) başka bir düğümü atlayabilirdi.
- **Yayılım ölçümü:** 60/30 sn ölçümü bu düzeltmeden sonra ve dolu blokla yapılır. Bu, "bugünkü taşımayla" kararına uyar, çünkü taşıma oturumsuz kalır.

### 4.6 Gömülü başlangıç listesi

- Ağ başına, ikiliye gömülü sabit liste. **Lansmanda DNS tohumu yoktur.**
- Mainnet listesi lansmanda doldurulur: en az üç bağımsız işletmecinin düğümleri, imzalı sürüm notunda da yayımlanır. Lansman kontrol listesine eklenir.
- DNS tohumu, bağımsız işletmeciler oluşunca ayrı bir kararla gelir.
- Bedel: liste bayatlar, güncellemesi yeni sürüm ister.

## 5. PR sırası

Her biri ayrı PR, test ve `guvenlik-denetimi` ile:

1. Bu not ve kararlar.
2. **İtme taşıması düzeltmesi** (§4.5). Keşiften bağımsızdır, bugünkü yapılandırılmış eşleri de düzeltir.
3. **Adres öğrenme:**
   - `getaddr`/`addr`, ağ kimliği, ağ başına port, protokol sürümü 3;
   - küçük tablo ve ayrı LevelDB;
   - gömülü liste mekanizması (mainnet boş);
   - `-discover` bayrağı.

   Yalnızca öğrenme: giden kullanım değişmez.
4. **Yedek kullanım:**
   - erişilemezlik tespiti ve histerezis;
   - yedek eş seçimi;
   - döngülerle bağlantı ve eş başına durum temizliği (§4.4);
   - bozuk işaret.

## 6. Kalanlar (yedek rolün bilinen sınırları ve sonraya kalanlar)

- **Ağ büyümesi operatörlere bağlıdır.** Yeni dinleyiciler kendiliğinden bulunmaz; biri onları `-peer` ile yapılandırmalıdır.
- **NAT arkasındaki madenci** itme için yapılandırılmış eşlerine, onlar düşünce yedeklere muhtaçtır.
- **Topoloji gömülü liste düğümlerinde yoğunlaşır.** Liste işletmecileri yarışlarda öne geçer (γ). Bunun ölçüsü yayılım ölçümünde ayrıca bakılmalıdır.
- **`getaddr` yanıtı yanıtlayanın yapılandırma ilişkilerini açığa çıkarır** (topoloji çıkarımı).
- **Birincil rol lansmandan sonra ayrı karar ister:**
  - kendini duyurma ve besleyici yoklaması;
  - Bitcoin benzeri `new`/`tried` tablosu (IP başına kayıt tavanı, "dolu konumdaki iyi kaydı koru", "çıkarmadan önce sına");
  - senkronizasyon döngüsünün eş başına işçilere bölünmesi;
  - çapalar;
  - eclipse sinyali olarak ebeveyni bilinmeyen, PoW'u geçen bloklar.

## 7. Kararlar (kullanıcı, 2026-11-14)

1. **Keşif lansmanda yedek roldedir.** `-peer` ve gömülü liste birincil kalır. Adres yalnızca bu güvenilen eşlerden `getaddr` ile öğrenilir. Keşfedilmiş eşler az sayıda ve sınırlı rolde kullanılır. Borsalar keşfi kapatabilir.
2. **Önce itme taşıması düzeltilir.** Keşif PR'ları ondan sonra gelir. Yayılım ölçümü düzeltmeden sonra ve dolu blokla yapılır.
3. **Başlangıç listesi gömülü sabit listedir.** Lansmanda DNS tohumu yoktur. Liste en az üç işletmecinin düğümlerinden oluşur ve imzalı sürüm notunda yayımlanır. DNS tohumu bağımsız işletmeciler oluşunca gelir.
4. **`-peer` verilmiş düğümde keşfedilmiş eşler yalnızca yedektir.** Yapılandırılmış eşlerin hepsine ulaşılamıyorken devreye girerler. Keşif bir bayrakla tamamen kapatılabilir.
