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
- "ASIC'e dirençli" ya da "Bitcoin ASIC'leri Q-BTC'yi kazamaz"

Doğru ifade: ML-DSA-65 imzaları, kabul görmüş güvenlik varsayımları altında, kuantum bilgisayarlı saldırganın imza sahteciliğine karşı tasarlanmıştır. Uygulama ve yan kanal riskleri bu kapsamın dışındadır. Zincirin yeniden yazılmasına karşı koruma yalnızca dürüst hashrate çoğunluğundan gelir. Kuantum hızlandırmasının (Grover) madenciliğe etkisi bu notta değerlendirilmemiştir.

## Donanım uyumluluğu

Başlık 96 bayttır ve nonce ilk 64 baytlık SHA-256 bloğundadır (core/header.go). Bu düzen, Bitcoin ASIC'lerinin ilk bloğun ara durumunu (midstate) bir kez hesaplayıp yalnızca ikinci bloktaki nonce'u döndürme varsayımını bilerek bozar. Ancak bu **bir donanım testi değil, tasarım gerekçesidir.** Hiçbir yazılım testi bir donanımın Q-BTC'yi kazamayacağını kanıtlamaz. Mevcut ASIC'lerin yazılım uyarlaması, FPGA'lar ve yeni yongalar mümkün kalır.

Kullanıcı kararı (2026-11-12): referans donanım, Q-BTC başlığında ölçülmüş açık kaynak bir GPU kazıcısıdır. Lansmandan önce bu kazıcı ve çok çekirdekli bir CPU kazıcısı yayımlanır. Başlatıcı genesis'i ve 1.–2. bloğu yalnızca bu GPU kazıcısıyla kazar; açıkladığı hashrate aynı kazıcıyla herkesçe yeniden üretilebilir. Zorluk tabanı hedef cinsinden min(başlangıç · 2^16, 0x1d00ffff ≈ 2^224) seçilir (MAINNET-ONCESI.md).

Sonuçları:
- Bu başlıkta aynı SHA-256 donanımı Bitcoin'deki hızının yaklaşık 2/3'ünü verir: nonce denemesi başına 3 SHA-256 sıkıştırması gerekir (başlık için 2, ikinci SHA-256 için 1), Bitcoin'de midstate ile 2. Bu yalnızca sıkıştırma sayısından kaba bir tahmindir, kesin değer ya da alt sınır değildir; rakamlar ölçülmedi.
- GPU bulut kiralaması ilk günden erişilebilirdir. Lansman hashrate'i küçük olduğundan erken dönemde %51 saldırısı ucuzdur.
- GPU ölçeğinde bir lansmanda taban başlangıçtan yalnızca ~2^4–2^6 kat kolay olabilir (ölçülmemiş 1–5 GH/s varsayımı; oran GPU sayısıyla büyür). Zincir takvimin 8–12 saat gerisine düşerse taban dolar ve sonrasında açık birikir. Bu oranı aşan kalıcı hashrate düşüşünde bloklar süresiz yavaş kalır.
- Güvenlik varsayımı yine en kötü durumdur: uyumlu SHA-256 hashrate'i (uyarlanmış ya da yeni donanım, kiralama pazarları) Q-BTC'ye yönelebilir.

## Selfish mining

**Mekanizma.** Saldırgan bulduğu blokları saklar ve özel bir dal üzerinde kazmaya devam eder. Dürüst ağ bir blok bulduğunda saldırgan saklı bloğunu yayar. Böylece dürüst madencilerin işinin bir kısmı yetim kalır. Saldırganın ana zincirdeki blok payı, hashrate payından büyük olabilir (Eyal ve Sirer).

**Eşik.** Eyal–Sirer modelinde strateji, saldırganın hashrate payı α şu eşiği aştığında kârlıdır:

    α > (1 − γ) / (3 − 2γ)

Burada γ, iki dal yarıştığında dürüst hashrate'in saldırganın dalında kazan kısmıdır.

- γ = 0 için eşik ≈ %33, γ = 0,5 için %25'tir.
- γ 1'e yaklaştıkça eşik **sıfıra iner**: ağa iyi bağlı bir saldırgan her büyüklükte kâr edebilir.
- Optimal stratejiler üzerine sonraki çalışmalar (Sapirshtein vd.) eşiğin bu basit modeldekinden düşük olduğunu gösterir; ağ gecikmesi hesaba katıldığında kârlılık eşiği tamamen ortadan kalkabilir. İletişimde kesin bir güvenli yüzde verilmemelidir.

**Q-BTC'de γ büyük olabilir.**
- Ağ küçüktür ve eş keşfi yoktur (yalnızca `-peer`). Birçok düğümün yapılandırılmış eşi olan bir havuz operatörü, yarışlarda dürüst düğümlerin çoğuna önce ulaşabilir.
- Korunan bağlantı yuvaları, ucu uzatan yeni bloğu ilk getiren 8 adres grubuna verilir (network/peers.go). Dürüst bloğu görür görmez saklı bloğunu yayan bir saldırgan bu yuvaları kazanabilir.
- 60 sn blok süresi ve büyük ML-DSA imzaları yayılım gecikmesinin payını artırır. Bu madde ölçülmemiştir.

**Eşitlik kuralı işe dayalıdır.**
- Q-BTC dalları blok sayısıyla değil, birikmiş işle karşılaştırır. Tam eşit işte ilk görülen kalır (core/chain.go).
- Hedef yalnızca ebeveynin zaman damgasına bağlı olduğundan tam eşitlik pratikte yalnızca kardeş bloklarda oluşur.
- İki ya da daha uzun eşit uzunluklu dalda hangisinin daha çok iş taşıdığını blokların zaman damgaları belirler. Saldırgan gizli bloklarının damgalarını MTP'ye doğru geriye atarak torunlarını zorlaştırabilir ve dalına birkaç yüzde fazla iş kazandırabilir. İleri, FTL'ye doğru atmak işi azaltır. Etki blok başına yaklaşık 2^(Δ/7200)'dür; Δ, damganın dürüst rakibine göre ne kadar geride olduğudur ve MTP ile sınırlıdır (60 sn'lik blokta birkaç dakika, ~%3–4). Etki birikmez, çünkü her bloğun hedefi yalnızca ebeveyninin damgasına bağlıdır. Kazanç bedava da değildir: daha zor blok orantılı olarak daha fazla hash ister. Kazanç yalnızca aynı blok sayısındaki işe dayalı eşitlik bozmada ortaya çıkar.
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
- Saldırı dalındaki ödüller coinbase olgunluğuna ve saldırı sonrası olası fiyat düşüşüne tabidir. Kendi donanımını kullanan saldırgan için maliyet, kaçırdığı dürüst kazım gelirinin fırsat maliyetidir.
- Hashrate kirası blok ödülüne yaklaşırsa "hashrate maliyeti − saldırı dalındaki ödüller" derinlikten bağımsız olarak sıfıra yaklaşır. Bu durumda onayı uzatmak saldırıyı pahalılaştırmaz ve güvenli bir bekleme eşiği hiç kurulamayabilir.

**`/confirmations` göstergelerini birlikte kullanın.** Şu alanlar birlikte değerlendirilmelidir:
- `confirmations`: blok sayısı.
- `work` / `work_in_start_blocks`: birikmiş iş.
- `elapsed_seconds`: medyan zamanla geçen süre. Ucun son 11 bloğunun çoğunu kazan biri bu değeri 300 sn'ye kadar şişirebilir; işlem bloğunun çevresindeki blokların çoğunu da kazandıysa onun medyan zamanını geride tutarak daha fazla.
- `attacker_success`: hashrate payı 0,10, 0,20, 0,30 ve 0,40 olan saldırganın `attacker_success_depth` blok geriden yetişme olasılığı (aşağıda). Blok ana zincirde değilse ya da alarm varsa verilmez.

**Yetişme olasılığını modelin sınırlarıyla okuyun.** `attacker_success`, Bitcoin makalesinin (Nakamoto 2008, §11) hesabıdır: payı q olan saldırgan işlem bloğunun üzerine z blok eklendiğinde gizli bir dalla başlar, ilerlemesi Poisson dağılır ve geride kaldığı her bloğu (q/p) olasılıkla kapatır. Makalenin tablosundan, P < %0,1 için gereken z:

| q | 0,10 | 0,15 | 0,20 | 0,25 | 0,30 | 0,35 | 0,40 | 0,45 |
|---|---|---|---|---|---|---|---|---|
| z | 5 | 8 | 11 | 15 | 24 | 41 | 89 | 340 |

- **z nasıl seçilir:** işlem bloğunun üzerindeki blok sayısı (`confirmations` − 1), iki sınırın küçüğüyle kısılır. Model dürüst blokların işlem bloğuyla aynı zorlukta ve beklenen sürede geldiğini varsayar; duraklamadan sonra taban zorluğunda gelen bir blok seli z'yi şişirmemelidir.
  - İş: üstteki işin işlem bloğunun kendi işi cinsinden blok sayısı. Duraklamadan önce kazılmış bir işlemin üzerindeki sel az iş taşır ve sayılmaz.
  - Süre: (`elapsed_seconds` − 300) / `TargetSpacing`. 300 sn, ucun medyan zamanının ileri itilebileceği paydır; işlem bloğunun çevresindeki blokların çoğunu kazan biri süreyi bundan fazla da şişirebilir.
  - Süre sınırı her derinlikte yaklaşık 5 blok düşürür: olağan işleyişte z ≈ `confirmations` − 6 (60 sn'de; 30 sn'de ≈ − 11). Tablodaki z'yi `attacker_success_depth` olarak okuyun, `confirmations` − 1 olarak değil: örneğin q = 0,10'da P < %0,1 için ~11 onay gerekir. İlk ~6 blokta derinlik 0, olasılık 1 görünür; hashrate işlemden sonra düştüyse derinlik blok sayısından küçük çıkar. Bu temkinlidir, hata değildir.
  - Duraklama sırasında sabit pay varsayımı da çöker: dürüst ağ dururken saldırgan gizli dalında tek başına kazar. Uzun bir duraklamadan sonra olasılık yerine iş ve süre eşiklerine bakın. Kullanılan z `attacker_success_depth`'tir; 10.000'den derin bloklar için 10.000'deki değer (bir üst sınır) verilir.
- **60 sn blok:** z blok burada Bitcoin'in 10 dakikalık bloklarına göre 10 kat daha kısa süre demektir. Olasılık blok sayısına bağlıdır, süreye değil; ama hashrate kiralayan saldırganın maliyeti süreyle büyür, bu yüzden aynı z daha ucuza denenir (yukarıdaki kârlılık hesabı).
- **Model iyimserdir:** sabit hashrate payı ve Poisson blok zamanları varsayar; selfish mining yoktur. Yetim bloklar dürüst hashrate'in bir kısmını boşa harcatır ve saldırganın payını fiilen büyütür (60 sn'de Bitcoin'den daha çok): yan blok oranı s ise saldırganın fiilî payı yaklaşık q / (q + p·(1 − s))'dir. q de bilinmez: tablodaki satırlar varsayımdır, ölçüm değildir.
- Olasılık kârlılık hesabının yerine geçmez; q ≥ 0,5'te 1'dir ve hiçbir derinlik yetmez.

**Alarmları doğru okuyun.** `deep_reorg_alarm`, `missing_data_alarm` ya da `below_min_chain_work` true ise ya da `behind_blocks` sıfırdan büyükse hiçbir onaya güvenmeyin. Ama **alarmın false olması güvenli olduğunuz anlamına gelmez**:
- Derin reorg alarmı ~2 saatlik blok sayısından (`TargetSpacing`'den: 60 sn'de 120, 30 sn'de 240 blok) derin reorg'da kalıcı olarak açılır. Eşik süreye değil blok sayısına bağlıdır: hashrate çökerse bu kadar blok çok daha uzun sürer, taban zorluğunda bir blok selinde dakikalar içinde dolar.
- Tipik bir çift harcama yalnızca borsanın onay derinliğini geri sarar, bu yüzden alarmı hiç tetiklemez.
- Eşiğin altındaki art arda reorg'lar sayılmaz.
- Alarm saldırıdan sonra çalar, saldırıyı önlemez.
- Alarm tek düğümün gözlemidir; reorg sırasında kapalı olan düğüm onu görmez.
- Alarmı yalnızca operatör, olayı inceledikten sonra kapatabilir (`-clear-reorg-alarm`).
- Eksik veri alarmı (`missing_data_alarm`), düğüm indeksindeki bir bloğun gövdesini, undo ya da indeks geri alma kaydını diskte bulamadığı ya da gövdeyi diskte bozuk bulduğu (başlığıyla tutmuyor) için daha çok işli zincire geçemediğinde kalıcı olarak açılır: düğüm hafif zincirde kalmış olabilir. Bugün bu yalnızca disk ya da bellek arızası, yazılım hatası ya da elle silinmiş veya değiştirilmiş veri demektir. Diski, belleği ve yedeği inceleyin (bozulma yazma anında bellekte olduysa yedek de bozuktur); kurtarma yedekten geri yükleme ya da veri dizinini silip yeniden eşitlemedir. Alarmı yalnızca operatör kapatabilir (`-clear-data-alarm`).
- Geride kalma göstergesi (`behind_blocks`), düğüm bildiği daha çok işli bir zincire geçemediğinde o zincirin kaç blokluk iş önde olduğunu verir: düğüm bayat zincirdedir, onayları o zincire göre sayar. Neden eksik ya da bozuk veri, disk hatası ya da geri giden saat olabilir; alarm vermeyen nedenleri de gösterir. Kalıcı değildir: düğüm yetişince ya da yeniden başlatılınca sıfırlanır (sorun sürüyorsa açılıştaki geçiş denemesinde yeniden dolar). Açık ~2 saatlik blok sayısına (derin reorg alarmıyla aynı ölçü) ulaşınca düğüm kazımı durdurur (`mining_paused`); o zamana kadar bayat zincire kazmaya devam eder. Eşik bilinçli olarak büyüktür: bütün düğümleri aynı blokta takan bir yazılım hatasında küçük bir eşik, o bloğun üstüne birkaç blok kazan herkese ağı durdurma imkânı verirdi. Hashrate çoğunluğunu tutan takılı bir madenci hiç durmaz; kendi dalı en çok işli zincir olur. Bütün düğümleri takan bir hatada, hashrate çoğunluğunu birkaç saat tutan biri geçilemeyen dalı eşik kadar öne geçirip bırakırsa bütün dürüst madenciler durur ve kazım düzeltme sürümüne kadar kendiliğinden sürmez; operatör `-mine-while-behind` ile duraklamayı kapatıp kazmaya devam edebilir (`mining_pause_disabled` bunu gösterir). Gösterge tek düğümün gözlemidir ve yalnızca düğümün gördüğü zincirleri kapsar.

**Ağı dışarıdan izleyin.** Düğümün `/mininginfo` ucu tek düğümün gözlediği hashrate tahminini ve yan blok oranını (`stale_rate`) verir; ama bu bir düğümün görüşüdür, izleme birden çok bağımsız düğüm ve dış araçlarla yapılmalıdır. Şunlar çekimleri durdurma sinyalidir:
- blok aralığının ya da zorluğun ani değişmesi,
- birden çok düğümde farklı uç görülmesi,
- reorg sıklığının artması.

**Coinbase olgunluğuna dikkat edin.** Olgunluk ~4 saatlik blok sayısıdır (`TargetSpacing`'den: 60 sn'de 240, 30 sn'de 480 blok). Olgunluk derinliğinden derin bir reorg, alacaklandırılmış ertelenmiş madenci ödemelerini de geri alır.

**Merkle kanıtıyla (SPV) doğrularken kökü kendiniz doğrulayın.** `GET /merkleproof?id=` bir işlemin bloğun Merkle köküne dahil olduğunu gösteren kanıtı verir (Bitcoin makalesi §8). Bloğun tamamını indirmeden ödemeyi doğrulamak için:
- Kökü yanıttaki `merkle_root`'tan değil, kendinizin doğruladığı bir başlıktan alın: başlıkları (`/headers`) birden çok bağımsız düğümden çekin, iş kanıtını ve `prev_hash` bağını denetleyin, her başlığın `bits`'inin zorluk kuralına (ASERT, yalnızca ebeveyne bağlı) uyduğunu doğrulayın ve en az bir toplam iş (tam düğümdeki `MinChainWork` gibi) isteyin, en çok işi taşıyan zinciri seçin. Yalnızca özet ≤ hedef denetimi yetmez: sizi kuşatan bir düğüm kendi seçtiği kolay `bits`'li başlıklar verebilir. Kanıtı sunan düğümün kökü kanıtı anlamsız kılar.
- Kimliği kendiniz hesaplayın; düğümün bildirdiği `txid`'ye ve JSON işleme güvenmeyin. `/merkleproof` yanıtı işlemin kanonik gövdesini (`body`, hex, ~153 bayt) ve tanık özetini (`witness_hash` = SHA256(field(PublicKey) ‖ field(Signature)), field(x) = 4 baytlık büyük-endian uzunluk ‖ x) verir; kimlik = SHA256(field("QBTC-TXID-v3") ‖ SHA256(gövde) ‖ tanık özeti) (`core.TxIDFromParts`). Kanıtı bu kimlikle doğrulayın, alıcı ve tutarı gövdeden okuyun (`core.DecodeTxBody`: katı çözücü, yeniden kodlama girdiyle aynı olmalı; düzen `core/transaction.go`). Gövde imzayı içermez: imzayı denetlemek isterseniz tam işlemi `/transaction`'dan alıp kimliğini `IDBytes` ile hesaplayın.
- Biçim: yaprak = SHA256(0x00 ‖ txid), düğüm = SHA256(0x01 ‖ sol ‖ sağ); n > 1 yaprakta sol alt ağaç n'den küçük en büyük ikinin kuvveti kadar yapraktır; `proof` kardeşleri yapraktan köke sıralar, her kardeşin yönü `index` ve `tx_count`'tan çıkar (`core.VerifyMerkleProof`). Son eleman kopyalanmaz (CVE-2012-2459 yok).
- Kanıt yalnızca dahil olmayı kanıtlar: `index` ve `tx_count` başlıkla bağlanmaz, düğümün iddiasıdır (farklı bir sıra ve sayı aynı köke çıkabilir). İşlemin bloktaki sırasına dayanan bir karar vermeyin.
- Hafif istemci işlemin geçerliliğini denetlemez, ana zincirin geçerli olduğuna güvenir: hashrate'in çoğunu ele geçiren biri geçersiz işlem içeren bir zinciri de kabul ettirebilir. Bu yüzden büyük tutarlarda tam düğüm çalıştırın.
- Onay derinliğini de kendiniz hesaplayın: kanıttaki `block_hash`'in sizin doğruladığınız en çok işli başlık zincirinde olduğunu ve üzerindeki iş/blok sayısını kendi başlıklarınızdan bulun. Yanıttaki `confirmations`, `height` ve `block_hash` düğümün iddiasıdır; sizi kuşatan (eclipse) bir düğüm kendi özel dalındaki işlemi derin gösterebilir. Onay eşikleri yukarıdaki gibi kurulur.
- Bir işlemin zincirde olmadığı kanıtlanamaz: 404 yalnızca bu düğümün görüşüdür.

## Kaynaklar

- I. Eyal, E. G. Sirer, "Majority is not Enough: Bitcoin Mining is Vulnerable", Financial Cryptography 2014.
- A. Sapirshtein, Y. Sompolinsky, A. Zohar, "Optimal Selfish Mining Strategies in Bitcoin" (arXiv:1507.06183).
- C. Grunspan, R. Pérez-Marco, "On profitability of selfish mining", 2018.
