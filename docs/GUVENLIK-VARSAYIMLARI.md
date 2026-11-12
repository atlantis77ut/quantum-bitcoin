# Güvenlik varsayımları: selfish mining ve %51 saldırısı

Bu not borsalar, entegrasyonlar ve kamuya yapılan iletişim içindir. Q-BTC'nin iş ispatı (PoW) güvenliği hakkında söylenebilecekleri ve söylenmemesi gerekenleri sabitler. Ayrıntılı uzlaşma kararları: [MAINNET-ONCESI.md](MAINNET-ONCESI.md).

**Sorumluluk reddi.** Bu not bir güvence değildir; proje ekibinin bilinen riskler hakkındaki değerlendirmesidir. Öneriler, her entegrasyonun kendi risk değerlendirmesinin yerini tutmaz. Pazarlama iletişiminde (ör. AB'de MiCA kapsamında) kullanılmadan önce hukuki görüş alınmalıdır.

## Kısa özet (iletişimde kullanılacak metin)

> Q-BTC'nin iş ispatı SHA256d karma fonksiyonunu kullanır; başlık düzeni Bitcoin'inkinden farklıdır. Güvenlik değerlendirmemiz en kötü durumu varsayar: SHA256d madencilik donanımı ve kiralanabilir hashrate Q-BTC'ye uyarlanabilir ve ağın toplam hashrate'ini kolayca aşabilir. Hiçbir zorluk ayarlama algoritması (DAA) buna karşı koruma sağlamaz. Ağın çoğunluğunu kısa süreliğine kontrol eden biri zinciri yeniden yazabilir ve çift harcama yapabilir. Q-BTC'de uzlaşma kuralı olarak bir yeniden düzenleme (reorg) derinliği sınırı ya da kontrol noktası yoktur. Onay sayısı tek başına kesinlik vermez.
>
> Q-BTC zorluğu her blokta ayarlar (ASERT, yarı ömür 2 saat). Bu, hashrate dalgalanmalarına hızlı uyum sağlar. Ancak "selfish mining" (bulunan blokları saklayıp stratejik yayma) stratejisinin zorluk ayarlamasını beklerken katlandığı kayıp dönemini de kısaltır.

İletişimde **söylenmemesi** gerekenler:
- "Bitcoin ile aynı iş ispatı" ya da "Bitcoin'in güvenliğini paylaşır"
- "51% saldırısına dayanıklı" ya da "ASERT/DAA saldırıları önler"
- "N onaydan sonra işlem geri alınamaz"
- "Derin reorg alarmı yoksa işlemler güvendedir"
- "Kuantuma dayanıklı imzalar zinciri güvenli kılar"

Doğru ifade: ML-DSA-65 imzaları, kabul görmüş güvenlik varsayımları altında, kuantum bilgisayarlı saldırganın imza sahteciliğine karşı tasarlanmıştır. Uygulama ve yan kanal riskleri bu kapsamın dışındadır. Zincirin yeniden yazılmasına karşı koruma yalnızca dürüst hashrate çoğunluğundan gelir. Kuantum hızlandırmasının (Grover) madenciliğe etkisi bu notta değerlendirilmemiştir.

## Donanım uyumluluğu

Başlık 96 bayttır ve nonce ilk 64 baytlık SHA-256 bloğundadır (core/header.go). Bu düzen, Bitcoin ASIC'lerinin ilk bloğun ara durumunu (midstate) bir kez hesaplayıp yalnızca ikinci bloktaki nonce'u döndürme varsayımını bilerek bozar. Ancak bu **bir donanım testi değil, tasarım gerekçesidir.**

Güvenlik varsayımı bu yüzden en kötü durumdur: SHA256d hashrate'i (mevcut donanımın yazılım uyarlamasıyla, yeni yongalarla ya da kiralama pazarlarıyla) Q-BTC'ye yönelebilir. Zorluk tabanı seçimi (MAINNET-ONCESI.md, "tek ucuz ASIC ~10 TH/s") da aynı uyumlu-donanım varsayımına dayanır. İki varsayım lansmandan önce birlikte yeniden değerlendirilmelidir.

## Selfish mining

**Mekanizma.** Saldırgan bulduğu blokları saklar ve özel bir dal üzerinde kazmaya devam eder. Dürüst ağ bir blok bulduğunda saldırgan saklı bloğunu yayar. Böylece dürüst madencilerin işinin bir kısmı yetim kalır. Saldırganın ana zincirdeki blok payı, hashrate payından büyük olabilir (Eyal ve Sirer).

**Eşik.** Eyal–Sirer modelinde strateji, saldırganın hashrate payı α şu eşiği aştığında kârlıdır:

    α > (1 − γ) / (3 − 2γ)

Burada γ, iki dal yarıştığında dürüst hashrate'in saldırganın dalında kazan kısmıdır.

- γ = 0 için eşik ≈ %33, γ = 0,5 için %25'tir.
- γ 1'e yaklaştıkça eşik **sıfıra iner**: ağa iyi bağlı bir saldırgan her büyüklükte kâr edebilir.
- Optimal stratejiler üzerine sonraki çalışmalar (Sapirshtein vd.) eşiğin bu basit modeldekinden düşük olduğunu, ağ gecikmesi hesaba katıldığında daha da düştüğünü gösterir. İletişimde kesin bir güvenli yüzde verilmemelidir.

**Q-BTC'de γ büyük olabilir.**
- Ağ küçüktür ve eş keşfi yoktur (yalnızca `-peer`). Birçok düğümün yapılandırılmış eşi olan bir havuz operatörü, yarışlarda dürüst düğümlerin çoğuna önce ulaşabilir.
- Korunan bağlantı yuvaları, ucu uzatan yeni bloğu ilk getiren 8 adres grubuna verilir (network/peers.go). Dürüst bloğu görür görmez saklı bloğunu yayan bir saldırgan bu yuvaları kazanabilir.
- 60 sn blok süresi ve büyük ML-DSA imzaları yayılım gecikmesinin payını artırır. Bu madde ölçülmemiştir.

**Eşitlik kuralı işe dayalıdır.**
- Q-BTC dalları blok sayısıyla değil, birikmiş işle karşılaştırır. Tam eşit işte ilk görülen kalır (core/chain.go).
- Hedef yalnızca ebeveynin zaman damgasına bağlı olduğundan tam eşitlik pratikte yalnızca kardeş bloklarda oluşur.
- İki ya da daha uzun eşit uzunluklu dalda hangisinin daha çok iş taşıdığını blokların zaman damgaları belirler. Saldırgan kendi gizli bloklarının damgalarını (MTP ile FTL arasında) seçerek dalına birkaç yüzde fazla iş kazandırabilir. Etki blok başına yaklaşık 2^(Δ/7200)'dür.
- Bu, ısrarcı (stubborn) türü stratejilere yarar. Nicel etkisi ölçülmemiştir.

**Hızlı DAA'nın etkisi.**
- Selfish mining yetim bloklar yüzünden ağın blok üretim hızını düşürür. Zorluk uyum sağlayana kadar saldırganın birim zamandaki geliri, dürüst kazsaydı alacağından azdır. Strateji ancak zorluk düştükten sonra kâra geçer (Grunspan ve Pérez-Marco).
- Bitcoin'de bu kayıp dönemi 2016 blokluk ayarlama aralığına (~2 hafta) bağlıdır. Q-BTC'de ASERT saatler içinde uyum sağlar, dolayısıyla kayıp dönemi çok daha kısadır.
- Başa baş süresi saldırganın payına bağlıdır: eşiğin belirgin üstündeki bir saldırgan için kısadır, eşiğe yakın bir saldırgan için uzundur. Q-BTC için nicel hesap yapılmamıştır.

**Zaman damgası manipülasyonunun sınırı.** ASERT mutlak bir formüldür (çapa 1. blok). Damga manipülasyonu birikmez (timewarp yok). Tek bir bloğun damgasını FTL (300 sn) kadar ileri atmanın zorluğa etkisi en çok ~%2,9'dur.

**Yapılmayanlar.** Selfish mining'e özel bir uzlaşma önlemi yoktur (ör. eşitlikte rastgele dal seçimi). Böyle bir önlem uzlaşma kuralı değişikliğidir ve ayrı karar gerektirir.

## %51 saldırısı

- Zincir seçimi saf en çok birikmiş iştir; uzlaşmada reorg derinliği sınırı ya da kontrol noktası yoktur. Ağın dürüst hashrate'inden fazlasını yeterince uzun çalıştıran biri, istediği derinlikte geçmişi yeniden yazabilir.
- **Hiçbir DAA %51'e karşı koruma vermez.** ASERT yalnızca hedef blok süresini korur. Saldırganın özel dalı da kendi zorluğunu kendi hızına göre ayarlar, en çok iş kuralı da iki dalı doğru karşılaştırır.
- Saldırının maliyeti, ağın dürüst hashrate'ini aşmanın bedelidir. Saldırganın kendi dalında kazandığı blok ödülleri bu bedeli düşürür.
- Gizli dalda kazılan hashrate, dal yayımlanana kadar ağda görünmez.
- Zorluk tabanı (`PowLimitBits`) ve en az zincir işi (`MinChainWork`) %51 saldırısına karşı savunma değildir. İkisi de spam'e ve yeni düğümlerin sahte zincirle kandırılmasına karşı düğüm korumalarıdır.

## Entegrasyonlar için öneriler

**Onay eşiğini kârlılık hesabıyla kurun.** Saldırganın kârı şöyle hesaplanır:

    kâr = tüm kurbanlardaki toplam çift harcama + saldırı dalındaki ödüller − hashrate maliyeti

- Aynı reorg ile birden çok borsada aynı anda çift harcama yapılabilir. Bu yüzden eşik, tek bir borsadaki yatırıma göre değil, ağdaki olası toplam açığa göre düşünülmelidir.
- Bekleme süresi ve iş, bu kârın negatif kalacağı kadar olmalıdır. Kârın negatif kalmadığı tutarlarda hiçbir onay eşiği yeterli değildir.

**`/confirmations` göstergelerini birlikte kullanın.** Şu alanlar birlikte değerlendirilmelidir:
- `confirmations`: blok sayısı.
- `work` / `work_in_start_blocks`: birikmiş iş.
- `elapsed_seconds`: medyan zamanla geçen süre. Son 11 bloğun çoğunu kazan biri bu değeri en çok 300 sn şişirebilir.

**Alarmları doğru okuyun.** `deep_reorg_alarm` ya da `below_min_chain_work` true ise hiçbir onaya güvenmeyin. Ama **alarmın false olması güvenli olduğunuz anlamına gelmez**:
- Derin reorg alarmı ~2 saatlik blok sayısından (`TargetSpacing`'den: 60 sn'de 120, 30 sn'de 240 blok) derin reorg'da kalıcı olarak açılır. Eşik süreye değil blok sayısına bağlıdır: hashrate çökerse bu kadar blok çok daha uzun sürer, taban zorluğunda bir blok selinde dakikalar içinde dolar.
- Tipik bir çift harcama yalnızca borsanın onay derinliğini geri sarar, bu yüzden alarmı hiç tetiklemez.
- Eşiğin altındaki art arda reorg'lar sayılmaz.
- Alarm saldırıdan sonra çalar, saldırıyı önlemez.
- Alarm tek düğümün gözlemidir; reorg sırasında kapalı olan düğüm onu görmez.
- Alarmı yalnızca operatör, olayı inceledikten sonra kapatabilir (`-clear-reorg-alarm`).

**Ağı dışarıdan izleyin.** Düğüm API'sinde şu an hashrate ya da yetim/yan blok oranı uç noktası yoktur; izleme birden çok bağımsız düğüm ve dış araçlarla yapılmalıdır. Şunlar çekimleri durdurma sinyalidir:
- blok aralığının ya da zorluğun ani değişmesi,
- birden çok düğümde farklı uç görülmesi,
- reorg sıklığının artması.

**Coinbase olgunluğuna dikkat edin.** Olgunluk 240 bloktur (~4 saat); daha derin bir reorg ertelenmiş madenci ödemelerini de geri alır.

## Kaynaklar

- I. Eyal, E. G. Sirer, "Majority is not Enough: Bitcoin Mining is Vulnerable", Financial Cryptography 2014.
- A. Sapirshtein, Y. Sompolinsky, A. Zohar, "Optimal Selfish Mining Strategies in Bitcoin" (arXiv:1507.06183).
- C. Grunspan, R. Pérez-Marco, "On profitability of selfish mining", 2018.
