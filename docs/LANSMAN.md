# Lansman hazırlığı: tasarım notu (madde 6)

**Durum:** kararlar verildi (kullanıcı, 2026-11-18, `devils-advocate` sonrası; bkz. §7), uygulama PR'ları bekliyor (§5). Bu not hiçbir uzlaşma kuralını değiştirmez. Konusu [MAINNET-ONCESI.md](MAINNET-ONCESI.md) madde 6'daki lansman kontrol listesinin bugünkü durumu, eksikleri ve lansman günü adımlarının "yayın anı − T_g ≤ birkaç dakika" şartına nasıl sığdırılacağıdır.

Terimler:
- **T_g:** genesis bloğunun zaman damgası.
- **t_b:** genesis mesajında özeti geçen Bitcoin bloğunun zaman damgası.
- **Yayın anı:** lansman commit'inin ve imzalı etiketin herkese açık depoya itildiği an. Bu andan önce başlatıcı dışında kimse mainnet düğümü çalıştıramaz.
- **Başlatıcı:** genesis'i üreten ve lansmanı yapan kişi.

## 1. Bugün depoda olanlar

| Parça | Yer | Durum |
|---|---|---|
| GPU kazıcısı (OpenCL 1.2) | `gpu/`, `-tags opencl -mine-gpu` | Hazır. Bulduğu her nonce CPU'da yeniden doğrulanır. CI'da yalnızca pocl ile (CPU üzerinde) doğruluğu sınanır. Çekirdek depodaki üreteçle (`gpu/gen/genkernel.py`) üretilir. |
| Çok çekirdekli CPU kazıcısı | `core/minerworkers.go`, `-mine-threads` | Hazır. |
| Hashrate ölçümü | `-bench-hashrate`, `scripts/gpu-olcum.sh`, [GPU-OLCUM.md](GPU-OLCUM.md) | Hazır. Rapor, başlangıç ve taban önerisini (`core.LaunchTargets`) içerir. Gerçek GPU'da hiç çalıştırılmadı. |
| Mainnet parametreleri | `core/params.go` `MainnetParams`; `core/mainnet.json`, `core/launch.go` | Genesis gömülü lansman dosyasından gelir; dosya bugün boş, başlangıç hedefi ve taban yer tutucu. Düğüm genesis'siz mainnet'i reddeder (`TestNetworkWithoutGenesisRefused`, `TestMainnetLaunchFile`). |
| Genesis vektörleri | `core/params_test.go` `genesisVectors`; `core/testdata/<ağ>_vector.json` | Testnet ve regtest için elle yazılmış sabitler ve bağımsız betiğin (§4.3) ürettiği dosyalar. |
| Genesis kazım aracı ve ön-uçuşu | `core/launchmine.go`, `genesismine.go`; `-mine-genesis`, `-launch-preflight` | Hazır (§4.1). Bugün her iki kip de durur: mainnet hedefleri seçilmedi. Gerçek GPU'da ve gerçek hedefle hiç çalıştırılmadı. |

Madde 6(a)'nın "kazıcılar yazılır" kısmı yani kapanmıştır. Aşağıdaki eksikler kapanmadan lansman yapılamaz.

## 2. Eksikler

### 2.1 Kod ve araç (PR sırası §5'te)

1. **Düğüm, bağlandığı liboqs'un doğru ML-DSA-65'i uyguladığını denetlemiyor.** liboqs sürümü yalnızca CI'da sabit (0.15.0). Depoda sabit bir bilinen-yanıt vektörü yok; testler yalnızca imzala-doğrula gidiş dönüşünü sınar. 0.12.0'dan önceki liboqs ML-DSA'nın taslak sürümünü uygular. Eski bir kurulumla derleyen katılımcı ilk 240 blokta uyumlu görünür (bu bloklarda imzalı işlem yoktur: coinbase olgunluğu 240), ilk harcamayı içeren blokta zincirden kopar. Lansman günü denetimlerinin hiçbiri bunu görmez. **Kapandı (feat/mldsa-kat):** düğüm her kipten önce `crypto.SelfTest` ile sabit bir vektörü denetler ve geçemezse çalışmaz. **Düzeltme:** bu maddedeki "eski bir kurulumla derleyen katılımcı" senaryosu depodaki liboqs-go sürümüyle oluşmaz (0.12.0'dan eski liboqs ile derleme başarısız olur; kaynaktan çıkarım, denenmedi); denetimin kapattığı gerçek durum ML-DSA-65'i etkin olmayan ya da farklı davranan bir liboqs'tur. Ayrıntı ve kalanlar: [MAINNET-ONCESI.md](MAINNET-ONCESI.md) madde 6.
2. **Genesis'i kazıyan araç yok.** Tek iz `grindGenesis` test yardımcısıdır (tek çekirdek, 50 milyon nonce sınırı). Mainnet başlangıç hedefinde genesis yalnızca GPU kazıcısıyla ve dakikalar içinde bulunabilir. **Kapandı (feat/genesis-mine-tool):** `-mine-genesis` ve `-launch-preflight` (§4.1).
3. **Bağımsız vektörü üreten betik depoda yok.** Lansmanda mainnet vektörünü dakikalar içinde üretecek, önceden sınanmış bir betik gerekir. **Kapandı (feat/genesis-vector):** `scripts/genesis_vektor.py` (§4.3).
4. **Lansman commit'i elle kaynak düzenlemesi ister.** Bugünkü hâliyle `params.go`, `params_test.go` (yeni vektör ve mainnet'i reddetme testi) ve `main.go` yardım metni elle değiştirilmelidir. Süre baskısı altında derleme ya da test hatası çıkarmaya açıktır. **Kapandı (feat/mainnet-launch-file):** lansman commit'i yalnızca iki veri dosyasıdır (§4.2).
5. **6(b)'deki "T_g ≥ t_b" şartını sabitleyen test yok.** **Kapandı (feat/mainnet-launch-file):** şartı lansman dosyası yükleyicisi ve bağımsız betik denetler; ikisinin testleri de bozulmuş hâlini reddeder (§4.2, §4.3).
6. **Lansman adımlarını ve provayı anlatan çalıştırma kılavuzu yok.**
7. **README ve LICENSE yok.** Lisansı olmayan kamuya açık depo hukuken açık kaynak sayılmaz. Bu yüzden 6(a)'daki "açık kaynak kazıcı yayımlanır" şartı lisans seçilmeden karşılanmış olmaz.

### 2.2 Depo dışında, başlatıcıya bağlı

1. **Gerçek GPU ölçümü**, lansmanda kullanılacak donanımda ([GPU-OLCUM.md](GPU-OLCUM.md); Linux ve üretici sürücüsü ister, ~15 dk). Üç şeyi belirler: açıklanacak hashrate, çekirdek A/B kararı (oran 1'in altındaysa döngülü çekirdeğe dönülür) ve başlangıç ile taban hedefi. Ölçümden sonra lansmana kadar sürücü güncellenmez.
2. **Sürüm imza anahtarı.** 6(c) tohum listesinin "imzalı sürüm notunda" yayımlanmasını ister; aynı anahtar "hangi genesis gerçek" sorusunun da güven köküdür. Açık anahtar lansmandan önce duyurulur. Anahtar, internete bağlı lansman makinesinin diskinde değil, dokunma onaylı bir donanım belirtecinde durmalıdır.
3. **İkinci bir makine** (ör. küçük bir sunucu): kendi saat eşitlemesi ve kendi liboqs kurulumuyla, yayından önceki doğrulama için (§3.2).
4. **Sabit adresli, dışarıdan ulaşılabilir başlatıcı düğümü ve en az üç bağımsız tohum eş işletmecisi** (6c; `network/addr.go` `seedPeers`). İşletmeciler etiketi görür görmez derleyip başlatan hazır bir betikle bekler.
5. **Çok makineli yayılım ölçümü** (açık kalan; 60 sn kararı benzetimli ölçümle kesinleşti, [YAYILIM-OLCUM.md](YAYILIM-OLCUM.md)).
6. **Lansman tarihi ve duyuru kanalı.**

## 3. Lansman günü

### 3.1 Neyin neyi sınırladığı

- **ASERT çapası 1. bloktur** (`core/asert.go`). T_g ile kazımın başladığı an arasındaki boşluk bu yüzden hedefi doğrudan kolaylaştırmaz. Ancak 1. blok T_g+1'e damgalanabilir; o zaman boşluk 3. bloktan itibaren 2^(boşluk / 2 sa) kat kolaylaşma ve yaklaşık boşluk/60 hızlı blok verir. Dondurulmuş şart (yayın anı − T_g ≤ birkaç dakika) bunu sınırlar.
- **Gizli kazım penceresi yayın anı − t_b ile sınırlıdır**, yayın anı − T_g ile değil: başlatıcı T_g'yi ileri bir ana koyup o ana kadar gizlice kazabilir. Dışarıdan doğrulanabilen ölçü yayın anı − t_b'dir; lansman kaydına yazılır. t_b madenci beyanıdır ve gerçek zamandan sapabilir (bugün ~±1–2 dk, kuralca iki saate kadar ileri): pencerenin gerçek başlangıcı bloğun ağda ilk görüldüğü andır ve kayda o da yazılır (§4.1 "Sınırların gerekçesi", §5 madde 5).
- **Rampa bu pencerenin değerini ihmal edilebilir kılar.** 1. bloğun ödülü 0,00025 QBTC, 2. bloğunki 0,0005 QBTC'dir. İlk 20 bloğun toplamı yaklaşık 0,05 QBTC, ilk 120 bloğun (~2 saat) toplamı yaklaşık 1,8 QBTC'dir. Bu hesap ödül içindir; pencerede gizli ek donanımla ileri damgalı blok biriktirmek, erken dönemdeki genel %51 riskinin ([GUVENLIK-VARSAYIMLARI.md](GUVENLIK-VARSAYIMLARI.md)) bir türevidir ve onu büyütmez.

### 3.2 İlke: geri alınamayan her nicelik için dış referans

Yayımlanmış bir genesis geri alınamaz; yeniden lansman "adil lansman" iddiasıyla bağdaşmaz. Başlatıcının kendi makinesindeki denetimler başlatıcıyı yalnızca kendisiyle karşılaştırır. Bu yüzden aşağıdaki her nicelik için başlatıcının makinesi dışında bir referans gerekir:

| Geri alınamayan nicelik | Bozulursa | Dış referans |
|---|---|---|
| T_g (ve ona bağlı ilk blok damgaları) | Saat ilerideyse sonraki bloklar FTL'e takılır ve ağ o süre boyunca ölü kalır; gerideyse yayın anı − T_g şişer. Örnek: Windows ile çift önyüklemeli makinede eşitlenmemiş Linux saati 3 saat ileri gösterebilir. | Ön-uçuşta saat eşitlemesi denetimi (sapma < 1 sn); yerel saatin, verilen Bitcoin başlıklarının zamanlarıyla tutarlılığı; ikinci makinenin kendi saati. |
| İtilen commit | Eksik dosya, yanlış dal, yarım yayın (commit gitti, etiket gitmedi). | Yerel imzalı etiketin temiz klonu ikinci makinede derlenip doğrulanır; dal ve etiket tek adımda (`git push --atomic`) itilir. |
| İmza anahtarı | Ele geçirilirse sahte tohum listesi ve sahte genesis imzalanır. | Donanım belirteci; prova ayrı, atılacak bir anahtarla yapılır. |
| Katılımcıların liboqs'u | §2.1 madde 1. | Düğüm açılışta ML-DSA-65 bilinen-yanıt vektörünü doğrular; başarısızsa başlamaz. Sürüm notunda liboqs sürümü yazılır. |
| Bitcoin referansı | Blok bayat kalırsa gezgin sitelerinde görünmez; "özet uydurma" kuşkusu kalıcı olur. | Bir onay almış blok; başlığın iş ispatı araçta ve testte doğrulanır (§4.1). |

### 3.3 Yayın sırası: iki seçenek

| | A: genesis doğrulanır doğrulanmaz yayımla | B: önce 1.–2. bloğu kaz, sonra yayımla |
|---|---|---|
| Yayın anı − T_g | ~2–3 dk | ortalama ~4–5 dk; %1 olasılıkla 9 dk'dan uzun |
| 1.–2. bloğu kim kazar | Yayından sonra herkes yarışır. Başlatıcı açıklanan GPU ile kazar; hazırlıklı büyük bir madenci onları kapabilir. | Başlatıcı, açıklanan GPU ile. |
| 1.–2. bloğun dışarıdan doğrulanabilirliği | Damgaları yayından sonradır, herkesçe sınırlanır. | Yayından öncedir; yalnızca başlatıcının beyanıdır. |
| Dondurulmuş metinle ("başlatıcı genesis'i ve 1.–2. bloğu yalnızca bu GPU kazıcısıyla … kazar") uyum | "Başlatıcı bu blokları kazarsa yalnızca açıklanan GPU ile kazar" diye okunur; kazanı garanti etmez. | Tam. |
| Yayından önce gerçek genesis'le uçtan uca deneme | Yok. Aynı kod yolu tam provada, aynı commit'te, başka bir genesis'le denenmiştir (§4.4). | Var, ama ancak ikinci düğüm ayrı makinede ve etiketin temiz klonundan çalışırsa anlamlıdır; aynı makinedeki ikinci düğüm saat, liboqs ve çalışma ağacı hatalarını göremez. |
| Ek gereksinim | Yok. | Kazımı 2. blokta kesin durduran bir bayrak (yoksa commit ve itme sürerken %60–80 olasılıkla 3. blok da kazılır); 1.–2. bloğun özet ve damgaları imzalı kayda girer. |
| Yayından sonraki ilk görünüm | Herkes genesis'ten başlar. | Başlatıcının düğümüne ulaşılamazsa erken katılımcılar kendi 1. bloklarını kazar; başlatıcının zinciri sonradan onları geri alır. |
| Başlatıcının yayından önce kazdığı ödül | 0 | 0,00075 QBTC |

İlk taslak B'yi öneriyordu ("önce çalıştığını gör, sonra yayımla"). `devils-advocate` şunu gösterdi: B'nin sağladığı güvencenin büyük kısmı ikinci makinede doğrulama ve tam provadan gelir ve bunlar A'da da yapılabilir; B'ye özgü kalan bedeller ise doğrulanamayan blok damgaları, durdurma bayrağı ve ulaşılabilirlik riskidir. Karar A'dır (§7 madde 1).

### 3.4 Adımlar (A seçeneğiyle)

Lansmandan günler önce:
1. Lansman donanımında GPU ölçümü yapılır, rapor yayımlanır.
2. Başlangıç ve taban hedefi rapordan hesaplanıp olağan bir PR ile `MainnetParams`'a yazılır; aynı PR `core/params.go`'daki `mainnetTargetsChosen` işaretini true yapar. Bunlar genesis mesajına bağlı değildir; önceden açıklanmaları lansman günü girdisini azaltır. İşaret false iken dolu bir lansman dosyası reddedilir: bu PR unutulursa dolu dosyayla derlenen ikili başlamaz ve testler kırmızıya döner.
3. Tohum listesi, imza anahtarı, lisans ve README tamamlanır. Lansman eşiği ve iptal ölçütleri (§7 madde 3) duyurulur.
4. Bütün akış iki makinede, ayrı bir klonda en az bir kez prova edilir (§4.4).

Lansman günü:
1. **Ön-uçuş (saatler önce ve hemen önce):** saat eşitlemesi, liboqs bilinen-yanıt denetimi, ölçüm raporuyla tutarlılık, kısa bir hashrate ölçümü (kayıtlı ortancanın ±%10'u içinde), ikinci makinenin hazır olduğu.
2. Başlatıcı Bitcoin başlıklarını (80 bayt, hex) verir. Araç başlıkları ve saati doğrular (§4.1), genesis'i GPU ile kazar, lansman dosyalarını yazar.
3. Bağımsız betik vektörü üretir ve özeti aracınkiyle karşılaştırır; ilgili testler çalışır.
4. Yerel commit ve imzalı etiket yapılır. Etiket ikinci makineye aktarılır; orada temiz klondan derlenir, testler ve bağımsız betik yeniden çalışır, T_g ikinci makinenin saatiyle karşılaştırılır.
5. Dal ve etiket tek adımda itilir (yayın anı). Başlatıcının düğümü ve tohumlar başlar; başlatıcı açıklanan GPU ile kazar.
6. Lansman kaydı (genesis özeti, commit, t_b, T_g, yayın anı, ölçüm raporunun özeti, atılan denemeler) imzalanıp duyurulur.

Eşik (yayın anı − T_g ≤ 10 dk) aşılırsa ya da bir denetim düşerse hiçbir şey yayımlanmaz; yeni bir Bitcoin bloğuyla baştan başlanır. Atılan her deneme (Bitcoin bloğu ve neden) lansman kaydına yazılır ki sonradan "genesis seçildi" kuşkusu doğmasın.

Yayın anının kanıtı: commit tarihi beyandır. Dakika ölçeği için birden çok bağımsız tanık (depo aynaları, arşiv anlık görüntüleri, duyuru damgaları) kullanılır; OpenTimestamps gibi bir hizmet yalnızca saatler ölçeğinde kaba bir üst sınır verir.

## 4. Araç tasarımı

### 4.1 Genesis kazım aracı (`-mine-genesis`) ve ön-uçuşu (`-launch-preflight`)

**Kapandı (feat/genesis-mine-tool; `core/launchmine.go`, `genesismine.go`).** Araç yeni bir uzlaşma kuralı getirmez: yazdığı dosya, her düğümün açılışta uyguladığı yükleyiciden (§4.2) geçmek zorundadır ve araç bunu kendisi de denetler. Araç yalnızca mainnet içindir; kuralları gevşeten bir bayrağı yoktur.

Kullanım (depo kökünden, `-tags opencl` ile derlenmiş ikiliyle):

```bash
./qbtc -network mainnet -launch-preflight -launch-block <başlık> -launch-confirmation <başlık> -launch-report qbtc-olcum-<zaman>.json
./qbtc -network mainnet -mine-genesis -launch-block <başlık> -launch-confirmation <başlık> -launch-report qbtc-olcum-<zaman>.json
```

- `-launch-block`: genesis mesajına girecek, bir onay almış Bitcoin bloğunun 80 baytlık başlığı (160 küçük harf hex).
- `-launch-confirmation`: o bloğu onaylayan bloğun başlığı.
- `-launch-report`: yayımlanan birleşik ölçüm raporu (`scripts/gpu-olcum.sh` çıktısı, [GPU-OLCUM.md](GPU-OLCUM.md)).
- `-launch-out`: lansman dosyası (varsayılan `core/mainnet.json`). `-launch-record`: lansman kaydı (varsayılan `qbtc-genesis-<UTC zaman>.json`).
- `-gpu-device`: aygıt. Tur boyu rapordan alınır; `-gpu-batch` verilirse rapordakiyle aynı olmalıdır.
- `-network mainnet` açıkça verilmelidir: varsayılan ağ testnet'tir.

**Denetimler.** Aşağıdaki sırayla çalışır (`TestRunLaunchCheckOrder` sırayı sabitler). Biri düşerse araç durur, genesis aranmaz ve geride hiçbir dosya kalmaz. Çıkış kodu 2'dir; yalnızca aygıt açılamazsa 1'dir.

1. **Ağ:** `-network mainnet` verilmiş olmalıdır.
2. **Lansman girdileri** (`core.NewMainnetLaunchPlan`):
   - Mainnet genesis'i zaten tanımlıysa araç çalışmaz.
   - Başlangıç hedefi ve taban seçilmiş olmalıdır (`mainnetTargetsChosen`) ve taban kuralını tutmalıdır. Bugün araç burada durur.
   - Araç Bitcoin başlıklarını kendisi doğrular; kod, lansman dosyası yükleyicisinin kodudur. Her başlık kendi iş ispatını taşır, hedefi gerçek Bitcoin zorluğu ölçeğindedir (`launchBitcoinMaxBits`, §4.3) ve ikincisi ilkine bağlanır.
   - Mesaj `Q-BTC mainnet genesis | BTC <ilk başlığın özeti>` olur. Blok yüksekliği mesaja girmez: başlıkta yükseklik yoktur, elle girilen ve doğrulanamayan bir sayı genesis'e kalıcı bir yazım hatası olarak girebilir.
3. **Yerel saat.** Denetim başta ve her T_g tazelemesinde yapılır. İki ayrı grup vardır:
   - T_g'nin veri kuralları (T_g yerel saat olduğu için saate uygulanır; yükleyicinin de şartlarıdır):
     - T_g ≥ t_b (madde 6b).
     - T_g − t_b ≤ 3600 sn. Bu yeni bir lansman şartıdır ve kullanıcı kararı bekler (aşağıda "Sınırların gerekçesi", §7).
   - Aracın kendi saat denetimleri (yalnızca araçta; onaylayan bloğun zamanına, t_c'ye göre):
     - t_c yerel saatten en çok 300 sn (FTL) ileride olabilir: saat geride ya da başlığın damgası ileride.
     - t_c yerel saatten en çok 1800 sn geride olabilir: başlıklar eski, saat ileride ya da Bitcoin'de uzun süredir blok yok.
4. **İkili** temiz bir commit'ten derlenmiş olmalıdır (git deposunda `go build`): kayda giren commit çalışan kodu anlatmalıdır. İzlenmeyen dosya da ağacı kirli gösterir; ikili (`qbtc`), ölçüm raporları ve aracın çıktıları bu yüzden `.gitignore`'dadır.
5. **Ölçüm raporuyla öz-tutarlılık:**
   - Rapor birleşik rapordur, gerçek bir GPU ölçümüdür (GPU olmayan aygıta izin verilmemiştir) ve temiz bir commit'ten alınmıştır.
   - Raporun çekirdek özeti çalışan ikilinin çekirdeğidir.
   - `MainnetParams`'taki başlangıç hedefi ve taban, rapordaki ortanca hashrate'ten `core.LaunchTargets` ile hesaplanan değerlerdir; raporun kendi önerisi de aynıdır.
   - Rapordaki tur boyu, ortanca hashrate'te en çok 1 sn sürmelidir: T_g, saat ve durdurma tur aralarında denetlenir ve uzun bir turda bulunan nonce'un başlığı bayat sayılır.
   - Bu denetim başlatıcıyı kendi hatasına karşı korur; kamuya "aynı donanım kanıtı" diye sunulmaz (rapor da ikili de başlatıcının elindedir). Kamusal kanıt, başkalarının aynı modelde ölçümü yeniden üretmesidir.
6. **Tur boyu:** genesis rapordaki tur boyuyla kazılır; `-gpu-batch` verilmişse rapordakiyle aynı olmalıdır.
7. **Çıktılar arama başlamadan önce ayrılır:**
   - `-launch-out` boş lansman dosyası (`{}`) olmalıdır.
   - Kayıt dosyası ve lansman dosyasının geçici kopyası (`<launch-out>.yeni`) yalnızca yoksa yaratılır ve açık tutulur. Kayıt zaten varsa, dizinler yazılamıyorsa ya da yarıda kesilmiş bir çalıştırmadan geçici dosya kalmışsa araç genesis aramadan durur.
   - Aynı lansman dosyasına yazacak ikinci bir çalıştırma da burada durur.
   - Araç sonraki bir denetimde ya da aramada durursa ayırdıklarını siler.
8. **Aygıt: yalnızca GPU.** OpenCL aygıtının türü GPU olmalıdır; modeli ve sürücü sürümü rapordaki aygıtınkiyle aynı olmalıdır (ölçümden sonra lansmana kadar sürücü güncellenmez, §2.2). Aygıt numarası farklı olabilir.

**Arama** (`LaunchPlan.Mine`):
- T_g ilk başlıkta ve sonra her 10 saniyede bir yerel saatten okunur. Başlık değiştiği için arama yeni başlıkla, nonce 0'dan sürer; denemeler bağımsız olduğundan beklenen süre değişmez.
- Nonce bulunduğunda başlık 20 sn'den eskiyse deneme biter (uykuya geçen makine, takılan sürücü, ileri atlayan saat). Böylece bulunan genesis'in T_g'si bulunduğu andan en çok 20 sn geridedir ve "yayın anı − T_g" ne aramanın ne de tek bir turun uzunluğuyla şişer.
- Saat geri giderse deneme biter: saat eşitlemesi kararsızdır ve eski damgalı başlık ileride kalabilir.
- Bulunan nonce yalnızca kazıldığı başlıkla birlikte kullanılır ve CPU'da yeniden özetlenir. Olağan kazımdan farklı olarak hatalı arka uçta CPU'ya dönülmez: genesis yalnızca açıklanan GPU kazıcısıyla kazılır.
- Kurulan lansman dosyası yükleyiciden (`applyLaunch`, gerçek kurallar) geçirilir.

**Çıktı** (kazım kipi):
1. Lansman kaydı ayrılmış dosyaya yazılır, diske indirilir ve geri okunur (ayrılan boş dosya arama sırasında silinmişse kayıt yazılmış sayılmaz). Alanları: genesis özeti, T_g, nonce, hedefler, mesaj, iki Bitcoin bloğunun özeti ve zamanı (t_b), T_g − t_b, aramanın başlangıcı ve süresi, denenen nonce ve başlık sayısı, aygıt, tur boyu, çekirdek özeti, commit, Go ve liboqs sürümü, ölçüm raporunun ve lansman dosyasının SHA-256'sı. Kayıt makine adı, kullanıcı adı ya da yol içermez; kamuya açıklanabilir.
2. Lansman dosyasının içeriği geçici kopyaya yazılır, diske indirilir, diskten geri okunur ve yükleyiciyle yeniden doğrulanır.
3. Lansman dosyası hâlâ boşsa geçici kopya onun yerine taşınır.

Lansman dosyası böylece ya boş kalır ya da doğrulanmış içeriği taşır: yarım ya da doğrulanmamış içerik onun adına hiç yazılmaz, dolu bir lansman dosyasının üzerine yazılmaz. Kayıt önce yazılır: kaydı yazılamayan bir genesis'in lansman dosyası da yazılmaz.

Araç bulduğu her genesis için ayrı bir kayıt bırakır. Atılan denemenin (§3.4) kaydı silinmez; imzalanıp duyurulan lansman kaydına eklenir (lansman betiği, §5 madde 5). Araç bir nonce bulduktan sonra durursa bu da atılmış bir denemedir:
- Başlık bayatsa ya da saat geri gittiyse nonce kullanılmaz; araç nonce'u ve özeti günlüğe yazar (çıkış kodu 2, kayıt yok).
- Kayıt ya da lansman dosyası yazılamaz ya da doğrulanamazsa çıkış kodu 1'dir; kayıt yazıldıysa saklanır, lansman dosyası boş kalır.

**Ön-uçuş kipi** aynı denetimleri genesis kazmadan çalıştırır ve geride dosya bırakmaz; çıktıların yaratılabildiğini görmek için kaydı ve geçici kopyayı yaratıp siler. Ek olarak açılan aygıtta kısa bir hashrate ölçümü yapar (3 sn ısınma atılır, 15 sn ölçüm): sonuç rapordaki ortancanın ±%10'u içinde olmalıdır. Lansmandan saatler önce ve hemen önce, o anın Bitcoin başlıklarıyla koşulur; böylece gömülü Bitcoin zorluk tabanının güncel başlıkları kabul ettiği de görülür.

**Sınırların gerekçesi.** `devils-advocate` (2026-11-20) bu sınırları sınadı; aşağıdaki ayrım onun sonucudur.

- **T_g − t_b ≤ 3600 sn: bayat blok ön-taahhüdü (veri kuralı; yükleyici, betik ve araç). Kullanıcı kararı bekler (§7).**
  - Ne yapar: lansmandan önce koda yazılır ve bayat bir Bitcoin bloğuyla (ör. günler önceki bir blokla ve ileri bir T_g ile) kurulmuş lansman dosyasını her düğüm reddeder. Sınır olmasa böyle bir dosya geçerdi.
  - Ne yapmaz: gizli kazım penceresini kanıtlamaz. T_g ile t_b zaten dosyadadır; kural yalnızca büyük farkı yasaklar. t_b madenci beyanıdır: Bitcoin kuralları damgayı gerçek zamandan kabaca bir saat geriye ve iki saat ileriye kadar serbest bırakır (bugünkü sapma ~±1–2 dk). Gerçek pencere yayın anı ile bloğun ağda ilk görüldüğü an arasındaki süredir ve onu yalnızca tanıklar verir. Bu yüzden lansman kaydına bloğun iki bağımsız gezgindeki ilk görülme zamanı da yazılır (§5 madde 5).
  - Operasyonel değeri: ön-uçuş saatler önce, o anın başlıklarıyla koşulur. Lansman günü yanlışlıkla o eski başlıklar verilirse T_g ≥ t_b tutar; bu sınır durdurur.
  - Bedeli: bir onay almış bloğun yaşı ortalama ~20 dakikadır. Bir saati aşma olasılığı rastgele bir anda ~%2, yeni gelen uç bloğun ebeveyni alınırsa ~%0,3'tür; o durumda sıradaki Bitcoin bloğu beklenir.
  - Açık seçenekler: şartın yükleyicide de olması ya da yalnızca araç ve betikte kalması; değerin 1 ya da 2 saat olması. İki değer veri tarafında denktir (fazladan bir saatin rampa ödülü en çok ~1,4 QBTC'dir). Lansman günü riski yoktur: araç, yükleyici ve betik aynı karşılaştırmayı yapar; biri kayarsa sıkı olan taraf yayından önce durur.
- **Aracın saat denetimleri (yalnızca araç; veri kuralından bağımsız sabitler).** Onaylayan bloğun zamanına (t_c) göredir, çünkü araç en yeni iki başlıkla çalıştırılır ve uç bloğun yaşı küçüktür.
  - İleri giden saat: t_c yerel saatten 1800 sn'den fazla gerideyse araç durur. Saat dilimi kaynaklı en küçük kayma bir saattir ve her zaman yakalanır; yeni bir uç blokla (~2 dk yaşında) yarım saatin üstündeki her kayma yakalanır. Eski başlıklar da buraya takılır.
  - Geri kalan saat: t_c yerel saatten 300 sn'den fazla ilerideyse araç durur (yeni bir uç blokla ~7 dakikanın üstündeki her kayma), ya da T_g ≥ t_b kuralına takılır. Pay 300 sn'dir, çünkü madenci damgaları gerçek zamandan birkaç dakika sapabilir; düğümün kendi FTL'i ile aynı değerdir.
  - Bedeli: rastgele bir anda uç bloğun 30 dakikadan yaşlı olma olasılığı ~%5'tir; araç yeni bir Bitcoin bloğu gelince başlatılırsa bu olasılık ihmal edilebilir.
- **Yakalanmayanlar:** yarım saatin altındaki ileri kaymalar ve birkaç dakikanın altındaki geri kaymalar bu denetimlerden geçebilir. Onları saat eşitlemesi denetimi (lansman betiği) ve ikinci makinenin saati (§3.2) yakalar. Araç ağ zamanı sorgulamaz: düğüm yalnızca yerel saati kullanır.
- **10 sn tazeleme, 20 sn bayatlık sınırı:** GPU turu ~100 ms sürer. 10 sn hem "yayın anı − T_g ≤ 10 dk" eşiğine göre küçüktür hem de başlığı gereksiz sık değiştirmez; 20 sn, bir tazeleme aralığı ile en çok o kadar süren son turdur.

**Testler:**
- `core/launchmine_test.go` (gerçek kurallar, yalnızca tavan gevşetilmiş; sahte arka uç ve sahte saat):
  - `TestLaunchMineEasyTarget`: araç depodaki örnek lansman dosyasını (`core/testdata/launch/easy_mainnet.json`) bayt bayt üretir. O dosyanın vektörünü bağımsız betik üretmiştir; böylece araç, dosya, yükleyici ve betik uçtan uca bağlanır.
  - `TestLaunchMineRefreshesTimestamp`: T_g tazelenince arama nonce 0'dan başlar; bulunan genesis tazelenen başlığın ilk geçerli nonce'udur.
  - `TestLaunchClock`, `TestLaunchMineFailures`: saat sınırlarının iki yanı (veri kuralının kaçırdığı yarım saatlik ileri kaymayı aracın kendi sınırının yakaladığı dahil); geri giden saat; arama sırasında eskiyen onaylayan blok; son turda ileri atlayan saat; hata veren ve doğrulanamayan nonce döndüren arka uç; yükleyiciden geçmeyen dosyanın döndürülmemesi; durdurma.
  - `TestNewLaunchPlanRefused`, `TestMainnetLaunchPlan`, `TestLaunchPlanVerify`: reddedilen girdiler; gerçek mainnet'in bugünkü durumu; yazılan dosyanın yeniden doğrulanması.
- `core/launch_test.go`: T_g − t_b sınırının iki ucu kabul edilir, bir saniye fazlası reddedilir; onu reddeden yalnızca bu sınırdır. Sınırın değeri (3600) Go testinde ve betiğin sınamasında ayrı ayrı sabittir.
- `genesismine_test.go` (sahte plan ve sahte aygıt):
  - `TestRunLaunchCheckOrder`: denetimlerin sırası. Hepsi birden bozukken ilk düşen ağdır; her adım düzeltilince sıradaki düşer.
  - `TestRunLaunchMine`: arama sırasında çıktıların ayrılmış olduğu ve ikinci bir çalıştırmanın durduğu; kaydın lansman dosyasından önce yazıldığı; doğrulamanın diskteki geçici kopyayla ve lansman dosyası değişmeden önce yapıldığı; kaydın içeriği.
  - `TestRunLaunchRefused`, `TestRunLaunchVerifyFailure`: bir denetim düştüğünde ya da dosya doğrulanamadığında lansman dosyasının boş kaldığı ve geride geçici dosya kalmadığı.
  - Ölçüm raporu şartlarının her birinin bozulmuş hâli, aygıt ve sürücü eşleşmesi, ön-uçuş ölçümü, dosya ayırma ve yazma.
- `scripts/genesis_vektor_test.py`: betikteki aynı sınır.
- CI adımı: derlenmiş ikili iki kipte de bir lansman denetiminde durur (çıkış kodu 2) ve `core/mainnet.json`'a dokunmaz.

**Kalanlar:**
- Araç gerçek GPU'da ve gerçek hedefle hiç çalışmadı; ilk çalıştırma tam provadadır (§4.4). `genesismine.go`'daki akış CI'da yalnızca sahte plan ve sahte aygıtla sınanır.
- Bugün iki kip de "hedefler seçilmedi" diye durur. Hedef PR'ından (§3.4) sonra CI adımındaki örnek "blok bayat" diye durur; adım üç durumu da kabul eder.
- T_g − t_b ≤ 3600 sn şartının yükleyicide kalıp kalmayacağı ve değeri kullanıcı kararı bekler (§7).
- Saat eşitlemesi (NTP) denetimi, ikinci makinenin hazır olduğu, "yayın anı − T_g ≤ 10 dk" eşiği, mesajdaki bloğun ilk görülme zamanı, atılan denemelerin toplanması ve kaydın imzalanması araçta yoktur; lansman betiğinin işidir (§5 madde 5).
- Araç başlıkların en çok iş taşıyan Bitcoin zincirinde olduğunu bilemez. Başlıkların iki bağımsız kaynaktan alınması çalıştırma kılavuzuna yazılır.
- Ön-uçuş ölçümü kısadır (15 sn); ±%10 payı ısınmış bir aygıt içindir.
- Araç zorla sonlandırılırsa (SIGKILL, elektrik kesintisi) ayırdığı boş kayıt ve geçici kopya geride kalır. Sonraki çalıştırma bunu söyleyip durur; dosyalar elle silinir.
- Arama sırasında disk dolarsa ya da ayrılan dosyalar silinirse bulunan genesis yazılamaz; araç özeti, T_g'yi ve nonce'u günlüğe yazar ve lansman dosyası boş kalır. Ayrılan dosyaların yerine başkasının dosyası konmuşsa araç ona dokunmaz.
- Dizin girdilerinin diske indirilmesi denenir ama zorunlu tutulmaz (desteklemeyen dosya sistemleri vardır); başarısız olursa araç yalnızca uyarır.
- Diskten geri okuma denetimi (yazılan baytların aynen okunması) CI'da hata enjeksiyonuyla sınanmaz.

### 4.2 Veri dosyaları: lansman commit'i yalnızca veri olsun

**Kapandı (feat/mainnet-launch-file; `core/launch.go`).** Lansman commit'i Go kaynağına dokunmaz, iki küçük veri dosyasını değiştirir:

- **`core/mainnet.json`** (ikiliye `go:embed` ile gömülür): ağ adı, genesis'in zaman damgası, mesajı, başlangıç hedefi, taban, nonce'u, özeti ve iki Bitcoin başlığı. Dosya bağımsız betiğin girdi biçimindedir (§4.3); betik onu doğrudan okur.
  - Lansmandan önce dosya boş bir nesnedir (`{}`) ve `MainnetParams` genesis'siz kalır.
  - Doluysa eksiksiz ve kanonik olmalıdır: çözülen içerik yeniden kodlandığında dosyayla bayt bayt aynı çıkmalıdır. Bilinmeyen ya da eksik alan, yinelenen anahtar, kesirli sayı ve artık veri böylece reddedilir.
  - Dolu dosya lansman şartlarını paket başlatılırken denetler; tutmuyorsa ikili hiçbir ağda başlamaz (panik). Şartlar: ağ adı `mainnet`; hedefler seçilmiştir (`mainnetTargetsChosen`, yer tutucu değildir); dosyadaki hedefler `MainnetParams.Bits` ve `MainnetParams.PowLimitBits`; taban kuralı; iki Bitcoin başlığının iş ispatı, zorluk ölçeği ve bağı; mesaj biçimi; T_g ≥ t_b; T_g − t_b ≤ 3600 sn (bayat blok geçmez; gerekçe §4.1); alanlardan kurulan genesis'in özeti dosyadaki özettir ve hedefi karşılar.
  - Yükleyici saate bakmaz: paket başlatılırken çalışır ve her makinede aynı sonucu vermelidir. Gelecekteki bir T_g'yi `TestMainnetLaunchFile` ve betiğin `uret` komutu yakalar (ikisi de lansman commit'inin CI'ında çalışır).
  - Başlangıç hedefi ve taban lansman commit'iyle değil, günler önce olağan bir PR ile `MainnetParams`'a yazılır (§3.4); lansman commit'i yine yalnızca veridir.
- **`core/testdata/mainnet_vector.json`**: bağımsız betiğin `core/mainnet.json`'dan ürettiği mainnet vektörü.

Elle düzenlenen üç yer veriye bağlandı: `MainnetParams` genesis'i gömülü dosyadan alır; reddetme testi genesis'siz bir kopyayla çalışır (`TestNetworkWithoutGenesisRefused`); `-network` yardım metni mainnet'in durumunu izler. Varsayılan ağ lansmandan sonra da testnet kalır.

Testler:
- `TestMainnetLaunchFile`: gömülü dosya ile `MainnetParams` tutarlıdır. Boşken mainnet genesis'sizdir; doluyken şartlar gerçek kurallarla yeniden denetlenir. Taban kuralı genesis'ten bağımsız, her koşuda denetlenir: hedefler günler önce yazıldığında da tutmalıdır.
- `TestGenesisVectorFiles`: "boş mu dolu mu" kararı yalnızca yükleyiciye bırakılmaz. Genesis'i tanımlı her ağın vektör dosyası Go kodunun hesabıyla (mainnet'te Bitcoin alanları dahil) aynı olmalıdır; genesis'i tanımsız ağın vektör dosyası olamaz. Lansman commit'inde iki dosyadan biri gelip öbürü gelmezse ya da ikisi tutmazsa test kırmızıya döner.
- `TestLaunchEasyTarget`, `TestLaunchRejected`, `TestParseLaunchFile`, `TestParseBitcoinHeader`, `TestLaunchFloorRule`: dolu dal bugün gömülü dosyayla çalışmadığından aynı yol kolay hedefli bir örnekle uçtan uca sınanır (gerçek kurallar, yalnızca tavan gevşetilmiş ve hedefler seçilmiş sayılmış). Her şartın bozulmuş hâli reddedilir; her örnek bir şartı hedefler ve hata metninin o şartın denetiminden geldiği denetlenir (mesajı ya da zamanı değişen örnekler yeniden kazılır, özetleri tutar), yani bir şartın denetimi kaldırılırsa test kırmızıya döner.
- `core/testdata/launch` altındaki iki dosya Go yükleyicisi ile betiğin ortak sınama verisidir: lansman dosyasını iki taraf da baştan kurar, vektörünü betik üretmiştir, Go testi vektörü kendi hesabıyla karşılaştırır. Lansman günü akışı da budur. Örnek mainnet biçimlidir ama gerçek kurallarla geçersizdir (tabanı gerçek tavandan kolaydır): iki uygulama da onu gerçek kurallarla reddeder, bunu da testler gösterir.
- CI adımı: lansman dosyası doluysa vektör var olmalı ve betiğin o dosyadan ürettiğiyle aynı olmalıdır; boşsa mainnet vektörü olamaz.
- Kazım aracının sahte arka uçla uçtan uca testi `TestLaunchMineEasyTarget`'tir (§4.1): araç aynı örnek dosyayı bayt bayt üretir.

Korunanlar ve lansman sonrası:
- Testnet ve regtest vektörleri bugünkü gibi elle yazılmış Go sabitleri olarak kalır. Üreteç betiğiyle veri dosyasını birlikte yeniden üreten bir PR onları sessizce değiştiremez.
- Lansmandan sonraki ilk PR mainnet özetini aynı listeye elle yazılmış bir sabit olarak ekler ve iki veri dosyasının değişmesini reddeden bir CI denetimi koyar.
- Lansman commit'i `guvenlik-denetimi`'nden geçecek zamanı bulamaz. Bu istisnanın yerine önceden denetlenmiş mekanik bir koruma konur (commit yalnızca bu iki dosyaya dokunur ve şemaya uyar). İstisnanın onayı çalıştırma kılavuzu PR'ında sorulur.

Seçilmeyen seçenek: genesis'i çalışma anında bir dosyadan ya da bayraktan okumak. Katılımcıları yeniden derlemekten kurtarırdı, ama düğüme kalıcı bir "bana verilen genesis'e inan" yolu ekler ve sahte ağlara yönlendirmeyi kolaylaştırır.

### 4.3 Bağımsız vektör betiği

**Kapandı (feat/genesis-vector).** `scripts/genesis_vektor.py`, yalnızca Python standart kitaplığıyla (3.8 ya da sonrası) ve Go kodunu çağırmadan:
- Girdiden (ağ adı, mesaj, zaman damgası, başlangıç hedefi, zorluk tabanı, nonce) alıcı adresini, coinbase kimliğini, Merkle kökünü ve blok özetini hesaplar. İki hedefin kodlamasının kanonik olduğunu, başlangıç hedefinin tabandan ve tabanın 2^252 − 1'den kolay olmadığını (`Params.Validate` ile aynı şartlar) ve özetin hedefi karşıladığını denetler. Taban başlığa girmez; vektör onu da sabitlesin diye girdidedir.
- Ağın imza alanını ve sabit baytlı bir bilinen-yanıt işleminin kimliği ile o alanda imzalanan mesajının özetini de hesaplar. Böylece vektör, mainnet imzalarının bağlanacağı alanı da sabitler.
- Mainnet için iki Bitcoin başlığını kendisi doğrular: her biri kendi iş ispatını taşımalı, hedefleri gerçek Bitcoin zorluğu ölçeğinde olmalı, ikincisi ilkine bağlanmalıdır. Mesaj tam olarak `Q-BTC mainnet genesis | BTC <ilk başlığın özeti>` olmalı, T_g ≥ t_b ve T_g − t_b ≤ 3600 sn tutmalı (`MAX_GENESIS_LAG`; gerekçe §4.1), taban tam olarak min(başlangıç hedefi · 2^16, 0x1d00ffff) olmalıdır (madde 6a'daki taban kuralı).

Komutlar:
- `denetle` vektör dosyalarını baştan hesaplar. Dosya eksiksiz ve betiğin üreteceği baytlarla aynı olmalıdır. Saate bakmaz; her yerde aynı sonucu verir.
- `uret` girdi dosyasından vektörü üretir. Girdide bulunan türetilmiş alanları (kazım aracının yazdığı özet) hesaplananla karşılaştırır. T_g bu makinenin saatinden 300 sn'den (FTL) fazla ilerideyse durur. Var olan bir vektör dosyasının üzerine yazmaz; içerik aynıysa geçer.
- Lansman günü ikinci makinede `uret`, `-o` ile commit'teki vektör dosyası gösterilerek yeniden çalıştırılır. Geçerse commit'teki vektör girdiden üretilenle aynıdır ve T_g o makinenin saatinden en çok 300 sn ileridedir; betik farkı saniye olarak yazdırır (§3.2'deki "ikinci makinenin kendi saati"). Daha sıkı bir eşik lansman betiğinin işidir.

Depodaki bağ:
- Regtest ve testnet vektörleri `core/testdata/<ağ>_vector.json` dosyalarındadır. CI'da betik bunları baştan hesaplar; `TestGenesisVectorFiles` aynı dosyaları Go kodunun hesabıyla karşılaştırır. Kodlama iki uygulamadan yalnızca birinde değişirse biri kırmızıya döner; lansmana kadar betik Go koduyla birlikte güncel kalır.
- Betiğin kendi sınamaları (`scripts/genesis_vektor_test.py`, CI adımı): bozulmuş girdiler ve dosyalar, gerçek Bitcoin başlıklarıyla mainnet şartları, gerçek mainnet kurallarıyla hedef ve taban tabloları (taban, `LaunchTargets`'in üreteceği değerle karşılaştırılır), komut satırı.

İlk tasarımdan farklar: beklenen değerler betiğin içinde değil vektör dosyalarındadır (Go testi aynı dosyaları okur; Go kaynağını düzenli ifadelerle çözümlemek gerekmez); girdi dosyası ağ adını, başlangıç hedefini ve tabanı da taşır (§4.2).

Sınırlar:
- Mainnet şartları, genesis hedefi gevşetilmiş bir kural kümesiyle sınanır: gerçek hedefte nonce aramak sınamada yapılamaz ve depoya mainnet görünümlü geçerli bir genesis konmaz. Gerçek hedefle deneme tam provadadır (§4.4). Komut satırı kuralları gevşetemez.
- Betikteki Bitcoin zorluk tabanı (`BITCOIN_MAX_BITS`) 976600. bloğun (2026-11-19) zorluğunun 1/16'sıdır ve tam provada yenilenir. Go tarafındaki karşılığı `core/launch.go`'daki `launchBitcoinMaxBits`'tir; lansman dosyası yükleyicisi ve kazım aracı (§4.1) aynı sabiti kullanır. İki sabit provada birlikte yenilenir ve denenir; ön-uçuş kipi güncel başlıkların tabandan geçtiğini lansman günü de gösterir.
- T_g − t_b en çok 3600 sn'dir (`MAX_GENESIS_LAG`; Go tarafında `launchMaxGenesisLag`): bayat bir Bitcoin bloğu betikten ve yükleyiciden geçmez. Sınır kazım aracı PR'ında kondu ve kullanıcı kararı bekler (§7); araç onu yerel saate de uygular. Bu bir ön-taahhüttür, gizli kazım penceresinin kanıtı değildir (§4.1). Sınırın içindeki fark yine yazdırılır.
- Betik başlıkların en çok iş taşıyan Bitcoin zincirinde olduğunu bilemez. Özet herkesçe gezgin sitelerinden denetlenir; bir onay şartı bayat blok riskini azaltır, kaldırmaz.
- Betik başlangıç hedefinin ölçülen hashrate'e karşılık geldiğini bilemez (girdide hashrate yoktur). Bugünkü yer tutucu değerlerle (başlangıç = taban = 0x1d00ffff) kazılmış bir genesis taban kuralına uyar ve betikten geçer. Go tarafında onu `mainnetTargetsChosen` işareti durdurur (§4.2): hedefler seçilip işaret true yapılmadan dolu lansman dosyası reddedilir. Seçilen hedeflerin ölçülen hashrate'e karşılık geldiğini denetleyen ise kazım aracının ölçüm raporuyla öz-tutarlılık denetimidir (§4.1); bu denetim yalnızca aracı çalıştıran başlatıcıyı korur, veriden okunamaz.
- Regtest ve testnette taban için başlangıç hedefiyle sıralama dışında kural yoktur; değerini ağın parametresine Go testi bağlar.

### 4.4 Prova

- **CI düzeyinde:** araç mantığı kolay hedef ve sahte arka uçla Go testinde, vektör betiği CI adımında çalışır. Derlenmiş ikilinin bugün genesis kazmayı reddettiğini ayrı bir CI adımı gösterir (§4.1).
- **Tam prova:** lansman makinesinde ve ikinci makinede, gerçek başlangıç hedefiyle ve güncel Bitcoin başlıklarıyla bütün akış çalıştırılır; süreler ölçülür. Koşulları:
  - Deponun ayrı bir klonunda yapılır (çalışma ağacı kopyasında değil: etiketler ve nesneler ortak kalırdı).
  - Atılacak ayrı bir anahtarla ve farklı bir etiket adıyla imzalanır. Gerçek anahtarla imzalanmış geçerli ikinci bir "mainnet" etiketi hiçbir zaman üretilmez.
  - Veri dizini açıkça verilir ve provadan sonra silinir (kalan bir prova zinciri lansman günü düğümün açılmasını engeller).
  - İtme adımı özel bir deneme deposuna karşı denenir. Bugün `main` dalında koruma kuralı yoktur; sonradan eklenirse lansman itmesi yeniden denenmelidir.
  - Provadan önce betikteki ve araçtaki Bitcoin zorluk tabanı o günün zorluğuna göre yenilenir (§4.3); prova güncel başlıkların iki uygulamadan da geçtiğini gösterir.
- Ayrı bir "prova ağı" eklenmez.

## 5. PR sırası

Her biri ayrı PR, test ve `guvenlik-denetimi` ile:

1. **ML-DSA-65 bilinen-yanıt denetimi:** sabit vektör testte ve düğüm açılışında (kriptografi; lansman araçlarından bağımsız ve en acil olanı). **Kapandı (feat/mldsa-kat).**
2. Bağımsız vektör betiği ve CI adımı. (Uzlaşma kodu değişmez.) **Kapandı (feat/genesis-vector).**
3. Gömülü mainnet genesis dosyası ve lansman şartı testleri (`core/params.go`). **Kapandı (feat/mainnet-launch-file).**
4. Genesis kazım aracı, ön-uçuş kipi ve lansman kaydı. **Kapandı (feat/genesis-mine-tool).**
5. Lansman betiği ve çalıştırma kılavuzu; prova talimatı; lansman commit'i istisnası. Araçta olmayanlar buradadır: saat eşitlemesi denetimi, ikinci makine, "yayın anı − T_g ≤ 10 dk" eşiği, mesajdaki Bitcoin bloğunun iki bağımsız gezgindeki ilk görülme zamanının kayda yazılması (gizli kazım penceresinin gerçek ölçüsü budur; başlıktaki zaman madenci beyanıdır), atılan denemelerin toplanması ve kaydın imzalanması (§4.1 "Kalanlar").
6. README ve LICENSE (lisans kararından sonra).

Depo dışındaki işler (§2.2) bu sıradan bağımsızdır; GPU ölçümü 4. PR'dan önce yapılırsa araç gerçek donanımda da denenir.

## 6. Karar soruları

Yanıtlar §7'dedir. İlk taslağın önerilerinden ikisi `devils-advocate` sonrasında değişti: yayın sırası (B'den A'ya) ve Bitcoin bloğu (uç bloktan bir onaylı bloğa).

1. **Yayın sırası** (§3.3): A (genesis doğrulanır doğrulanmaz) mı, B (önce 1.–2. blok) mi? Öneri: A.
2. **Bitcoin bloğu:** bir onay almış blok mu (öneri), o andaki uç blok mu? Uç blok yayın anı − t_b'yi ~10 dk kısaltır, ama bayat kalırsa genesis'te kalıcı bir leke olur; rampa yüzünden o 10 dakikanın değeri ihmal edilebilir.
3. **"Birkaç dakika" eşiği ve iptal kuralı:** yayın anı − T_g için sayısal eşik (öneri: 10 dk; aşılırsa deneme atılır, yeni Bitcoin bloğuyla baştan başlanır, atılan denemeler kayda yazılır).
4. **Lisans:** MIT, Apache-2.0 ya da GPL-3.0? Öneri: MIT (Bitcoin Core ile aynı; bağımlılıklar liboqs, liboqs-go, goleveldb izin verici lisanslıdır; kesin uyum denetimi lisans PR'ında yapılır). Apache-2.0'ın farkı açık patent lisansıdır; kurumsal entegratörler bunu sorabilir. Bu hukuki bir seçimdir, karar başlatıcınındır.

Soru sorulmadan uygulanacak tasarım seçimleri (itiraz gelirse PR'da değişir): lansman commit'inin veri dosyası olması (§4.2), mesajda yükseklik olmaması, T_g'nin tazelenmesi, ölçüm raporuyla öz-tutarlılık denetimi. Araç PR'ında aynı yolla belirlenenler aracın kendi saat denetimleridir (§4.1). T_g − t_b ≤ 3600 sn şartı bu sınıfa girmez: madde 6(b)'yi daraltan bir lansman kararıdır ve kullanıcıya sorulur (§7).

## 7. Kararlar (kullanıcı, 2026-11-18)

1. **Yayın sırası A.** Genesis ikinci makinede doğrulanır doğrulanmaz yayımlanır; 1. ve 2. blok yayından sonra herkese açık kazılır. Dondurulmuş "başlatıcı genesis'i ve 1.–2. bloğu yalnızca bu GPU kazıcısıyla ve aynı donanımla kazar" cümlesi şöyle okunur: başlatıcı genesis'i yalnızca açıklanan GPU kazıcısıyla kazar ve yayından sonra da (1.–2. blok dahil) yalnızca onunla kazar; bu blokları başlatıcının kazanacağı garanti edilmez.
2. **Genesis mesajına bir onay almış Bitcoin bloğunun özeti girer.** Araç iki başlık alır ve ikisinin de iş ispatını doğrular.
3. **Eşik: yayın anı − T_g ≤ 10 dakika.** Aşılırsa ya da bir denetim düşerse deneme atılır, yeni bir Bitcoin bloğuyla baştan başlanır; atılan her deneme lansman kaydına yazılır.
4. **Lisans MIT.**

Açık kalanlar:
- Lansman commit'inin `guvenlik-denetimi` istisnası ve yerine konacak mekanik koruma (§4.2), çalıştırma kılavuzu PR'ında sorulur.
- **T_g − t_b ≤ 3600 sn şartı (karar bekliyor, 2026-11-20; `devils-advocate` sonrası, §4.1 "Sınırların gerekçesi").** Kazım aracı PR'ı şartı yükleyiciye, betiğe ve araca koydu. Sorulan: şart madde 6(b)'ye eklensin mi; yükleyicide de mi olsun, yalnızca araç ve betikte mi; değeri 1 saat mi, 2 saat mi? `devils-advocate` hükmü: teknik bir zararı ve lansman günü riski yoktur; değeri bir ön-taahhüt olmasıdır, gizli kazım penceresinin kanıtı olarak sunulmamalıdır.
