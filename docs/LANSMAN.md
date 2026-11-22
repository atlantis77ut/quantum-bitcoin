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
| Lansman betiği ve lansman commit'i koruması | `scripts/lansman.py`, `scripts/lansman_commit_denetle.py`; [LANSMAN-KILAVUZU.md](LANSMAN-KILAVUZU.md) | Hazır (§4.5). Gerçek GPU, belirteç ve ikinci makineyle hiç çalıştırılmadı; istisna onaylandı (kullanıcı kararı 2026-11-22, §7 madde 6). |

Madde 6(a)'nın "kazıcılar yazılır" kısmı yani kapanmıştır. Aşağıdaki eksikler kapanmadan lansman yapılamaz.

## 2. Eksikler

### 2.1 Kod ve araç (PR sırası §5'te)

1. **Düğüm, bağlandığı liboqs'un doğru ML-DSA-65'i uyguladığını denetlemiyor.** liboqs sürümü yalnızca CI'da sabit (0.15.0). Depoda sabit bir bilinen-yanıt vektörü yok; testler yalnızca imzala-doğrula gidiş dönüşünü sınar. 0.12.0'dan önceki liboqs ML-DSA'nın taslak sürümünü uygular. Eski bir kurulumla derleyen katılımcı ilk 240 blokta uyumlu görünür (bu bloklarda imzalı işlem yoktur: coinbase olgunluğu 240), ilk harcamayı içeren blokta zincirden kopar. Lansman günü denetimlerinin hiçbiri bunu görmez. **Kapandı (feat/mldsa-kat):** düğüm her kipten önce `crypto.SelfTest` ile sabit bir vektörü denetler ve geçemezse çalışmaz. **Düzeltme:** bu maddedeki "eski bir kurulumla derleyen katılımcı" senaryosu depodaki liboqs-go sürümüyle oluşmaz (0.12.0'dan eski liboqs ile derleme başarısız olur; kaynaktan çıkarım, denenmedi); denetimin kapattığı gerçek durum ML-DSA-65'i etkin olmayan ya da farklı davranan bir liboqs'tur. Ayrıntı ve kalanlar: [MAINNET-ONCESI.md](MAINNET-ONCESI.md) madde 6.
2. **Genesis'i kazıyan araç yok.** Tek iz `grindGenesis` test yardımcısıdır (tek çekirdek, 50 milyon nonce sınırı). Mainnet başlangıç hedefinde genesis yalnızca GPU kazıcısıyla ve dakikalar içinde bulunabilir. **Kapandı (feat/genesis-mine-tool):** `-mine-genesis` ve `-launch-preflight` (§4.1).
3. **Bağımsız vektörü üreten betik depoda yok.** Lansmanda mainnet vektörünü dakikalar içinde üretecek, önceden sınanmış bir betik gerekir. **Kapandı (feat/genesis-vector):** `scripts/genesis_vektor.py` (§4.3).
4. **Lansman commit'i elle kaynak düzenlemesi ister.** Bugünkü hâliyle `params.go`, `params_test.go` (yeni vektör ve mainnet'i reddetme testi) ve `main.go` yardım metni elle değiştirilmelidir. Süre baskısı altında derleme ya da test hatası çıkarmaya açıktır. **Kapandı (feat/mainnet-launch-file):** lansman commit'i yalnızca iki veri dosyasıdır (§4.2).
5. **6(b)'deki "T_g ≥ t_b" şartını sabitleyen test yok.** **Kapandı (feat/mainnet-launch-file):** şartı lansman dosyası yükleyicisi ve bağımsız betik denetler; ikisinin testleri de bozulmuş hâlini reddeder (§4.2, §4.3).
6. **Lansman adımlarını ve provayı anlatan çalıştırma kılavuzu yok.** **Kapandı (feat/launch-script):** [LANSMAN-KILAVUZU.md](LANSMAN-KILAVUZU.md) ve `scripts/lansman.py` (§4.5).
7. **README ve LICENSE yok.** Lisansı olmayan kamuya açık depo hukuken açık kaynak sayılmaz. Bu yüzden 6(a)'daki "açık kaynak kazıcı yayımlanır" şartı lisans seçilmeden karşılanmış olmaz. **Kapandı (feat/readme-license):** `LICENSE` (MIT, telif sahibi "The Q-BTC developers"; kullanıcı kararı 2026-11-22) ve `README.md` (durum, özellikler, derleme, çalıştırma, testler, depo düzeni, güvenlik, lisans). Bağımlılık lisansları GitHub'dan okundu: liboqs ve liboqs-go MIT (liboqs'un üçüncü taraf alt dizinleri kendi lisanslarıyla), goleveldb BSD-2-Clause, snappy BSD-3-Clause; hepsi MIT ile uyumludur. Kalanlar: bu hukuki bir görüş değildir, kesin uyum denetimi başlatıcınındır; depoda kaynak dosya başlıklarında telif satırı yoktur (kök `LICENSE` yeter); özel güvenlik bildirim kanalı (GitHub "Private vulnerability reporting" şu an kapalıdır, başlatıcının ayarıdır) lansmandan önce açılıp README'ye yazılmalıdır.

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
| İtilen commit | Eksik dosya, yanlış dal, yarım yayın (commit gitti, etiket gitmedi). | İmzasız aday ikinci makinede temiz klondan derlenir, bütün testler ve koruma çalışır; etiket ancak bundan sonra imzalanır; uzaktaki `main` tabanda olmalıdır; dal ve etiket tek adımda (`git push --atomic`) itilir ve sonuç uzak depodan okunur (§4.5). |
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
4. Yerel commit yapılır; imzasız ve etiketsizdir. Aday ikinci makineye aktarılır; orada temiz klondan derlenir, bütün testler ve koruma (bağımsız betik dahil) yeniden çalışır, Bitcoin bloğu iki kaynakta denetlenir, T_g ikinci makinenin saatiyle karşılaştırılır. Gerçek anahtarla etiket ancak bundan sonra imzalanır (§4.5).
5. Uzaktaki `main`'in taban commit'te olduğu denetlenir; dal ve etiket tek adımda, zorlamadan itilir (yayın anı). Başlatıcının düğümü ve tohumlar başlar; başlatıcı açıklanan GPU ile kazar.
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
     - T_g − t_b ≤ 3600 sn. Bu yeni bir lansman şartıdır (kullanıcı kararı, 2026-11-20; aşağıda "Sınırların gerekçesi", §7 madde 5).
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

- **T_g − t_b ≤ 3600 sn: bayat blok ön-taahhüdü (veri kuralı; yükleyici, betik ve araç). Kullanıcı kararı (2026-11-20, §7 madde 5).**
  - Ne yapar: lansmandan önce koda yazılır ve bayat bir Bitcoin bloğuyla (ör. günler önceki bir blokla ve ileri bir T_g ile) kurulmuş lansman dosyasını her düğüm reddeder. Sınır olmasa böyle bir dosya geçerdi.
  - Ne yapmaz: gizli kazım penceresini kanıtlamaz. T_g ile t_b zaten dosyadadır; kural yalnızca büyük farkı yasaklar. t_b madenci beyanıdır: Bitcoin kuralları damgayı gerçek zamandan kabaca bir saat geriye ve iki saat ileriye kadar serbest bırakır (bugünkü sapma ~±1–2 dk). Gerçek pencere yayın anı ile bloğun ağda ilk görüldüğü an arasındaki süredir ve onu yalnızca tanıklar verir. Bu yüzden lansman kaydına bloğun iki bağımsız gezgindeki ilk görülme zamanı da yazılır (§5 madde 5).
  - Operasyonel değeri: ön-uçuş saatler önce, o anın başlıklarıyla koşulur. Lansman günü yanlışlıkla o eski başlıklar verilirse T_g ≥ t_b tutar; bu sınır durdurur.
  - Bedeli: bir onay almış bloğun yaşı ortalama ~20 dakikadır. Bir saati aşma olasılığı rastgele bir anda ~%2, yeni gelen uç bloğun ebeveyni alınırsa ~%0,3'tür; o durumda sıradaki Bitcoin bloğu beklenir.
  - Seçilmeyen seçenekler: şartın yalnızca araç ve betikte kalması (öz-koruma kalırdı, Go ile Python arasında bir fark doğardı); değerin 2 saat olması (veri tarafında 1 saatle denktir: fazladan bir saatin rampa ödülü en çok ~1,4 QBTC'dir). Lansman günü riski yoktur: araç, yükleyici ve betik aynı karşılaştırmayı yapar; biri kayarsa sıkı olan taraf yayından önce durur.
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
- Saat eşitlemesi (NTP) denetimi, ikinci makinenin hazır olduğu, "yayın anı − T_g ≤ 10 dk" eşiği, mesajdaki bloğun ilk görülme zamanı, atılan denemelerin toplanması ve kaydın imzalanması araçta yoktur; lansman betiğinin işidir. **Kapandı (feat/launch-script, §4.5).**
- Araç başlıkların en çok iş taşıyan Bitcoin zincirinde olduğunu bilemez. Lansman betiği bunu iki bağımsız kaynaktan denetler (§4.5).
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
- Lansman commit'i `guvenlik-denetimi`'nden geçecek zamanı bulamaz. Bu istisnanın yerine önceden denetlenmiş mekanik bir koruma konur (commit yalnızca bu iki dosyaya dokunur ve şemaya uyar; §4.5). İstisna onaylandı (§7 madde 6).

Seçilmeyen seçenek: genesis'i çalışma anında bir dosyadan ya da bayraktan okumak. Katılımcıları yeniden derlemekten kurtarırdı, ama düğüme kalıcı bir "bana verilen genesis'e inan" yolu ekler ve sahte ağlara yönlendirmeyi kolaylaştırır.

### 4.3 Bağımsız vektör betiği

**Kapandı (feat/genesis-vector).** `scripts/genesis_vektor.py`, yalnızca Python standart kitaplığıyla (3.8 ya da sonrası) ve Go kodunu çağırmadan:
- Girdiden (ağ adı, mesaj, zaman damgası, başlangıç hedefi, zorluk tabanı, nonce) alıcı adresini, coinbase kimliğini, Merkle kökünü ve blok özetini hesaplar. İki hedefin kodlamasının kanonik olduğunu, başlangıç hedefinin tabandan ve tabanın 2^252 − 1'den kolay olmadığını (`Params.Validate` ile aynı şartlar) ve özetin hedefi karşıladığını denetler. Taban başlığa girmez; vektör onu da sabitlesin diye girdidedir.
- Ağın imza alanını ve sabit baytlı bir bilinen-yanıt işleminin kimliği ile o alanda imzalanan mesajının özetini de hesaplar. Böylece vektör, mainnet imzalarının bağlanacağı alanı da sabitler.
- Mainnet için iki Bitcoin başlığını kendisi doğrular: her biri kendi iş ispatını taşımalı, hedefleri gerçek Bitcoin zorluğu ölçeğinde olmalı, ikincisi ilkine bağlanmalıdır. Mesaj tam olarak `Q-BTC mainnet genesis | BTC <ilk başlığın özeti>` olmalı, T_g ≥ t_b ve T_g − t_b ≤ 3600 sn tutmalı (`MAX_GENESIS_LAG`; gerekçe §4.1), taban tam olarak min(başlangıç hedefi · 2^16, 0x1d00ffff) olmalıdır (madde 6a'daki taban kuralı).

Komutlar:
- `denetle` vektör dosyalarını baştan hesaplar. Dosya eksiksiz ve betiğin üreteceği baytlarla aynı olmalıdır. Saate bakmaz; her yerde aynı sonucu verir.
- `uret` girdi dosyasından vektörü üretir. Girdide bulunan türetilmiş alanları (kazım aracının yazdığı özet) hesaplananla karşılaştırır. T_g bu makinenin saatinden 300 sn'den (FTL) fazla ilerideyse durur. Var olan bir vektör dosyasının üzerine yazmaz; içerik aynıysa geçer.
- Lansman günü betiğin kodu iki makinede de lansman commit'i korumasının içinden çalışır (§4.5): koruma, commit'teki vektörün lansman dosyasından bayt bayt üretildiğini ve `denetle` ile aynı şartlardan geçtiğini denetler. İkinci makinenin saati için daha sıkı eşik (T_g en çok 5 sn ileride) lansman betiğindedir; `uret` lansman makinesinde vektörü üreten koddur.

Depodaki bağ:
- Regtest ve testnet vektörleri `core/testdata/<ağ>_vector.json` dosyalarındadır. CI'da betik bunları baştan hesaplar; `TestGenesisVectorFiles` aynı dosyaları Go kodunun hesabıyla karşılaştırır. Kodlama iki uygulamadan yalnızca birinde değişirse biri kırmızıya döner; lansmana kadar betik Go koduyla birlikte güncel kalır.
- Betiğin kendi sınamaları (`scripts/genesis_vektor_test.py`, CI adımı): bozulmuş girdiler ve dosyalar, gerçek Bitcoin başlıklarıyla mainnet şartları, gerçek mainnet kurallarıyla hedef ve taban tabloları (taban, `LaunchTargets`'in üreteceği değerle karşılaştırılır), komut satırı.

İlk tasarımdan farklar: beklenen değerler betiğin içinde değil vektör dosyalarındadır (Go testi aynı dosyaları okur; Go kaynağını düzenli ifadelerle çözümlemek gerekmez); girdi dosyası ağ adını, başlangıç hedefini ve tabanı da taşır (§4.2).

Sınırlar:
- Mainnet şartları, genesis hedefi gevşetilmiş bir kural kümesiyle sınanır: gerçek hedefte nonce aramak sınamada yapılamaz ve depoya mainnet görünümlü geçerli bir genesis konmaz. Gerçek hedefle deneme tam provadadır (§4.4). Komut satırı kuralları gevşetemez.
- Betikteki Bitcoin zorluk tabanı (`BITCOIN_MAX_BITS`) 976600. bloğun (2026-11-19) zorluğunun 1/16'sıdır ve tam provada yenilenir. Go tarafındaki karşılığı `core/launch.go`'daki `launchBitcoinMaxBits`'tir; lansman dosyası yükleyicisi ve kazım aracı (§4.1) aynı sabiti kullanır. İki sabit provada birlikte yenilenir ve denenir; ön-uçuş kipi güncel başlıkların tabandan geçtiğini lansman günü de gösterir.
- T_g − t_b en çok 3600 sn'dir (`MAX_GENESIS_LAG`; Go tarafında `launchMaxGenesisLag`): bayat bir Bitcoin bloğu betikten ve yükleyiciden geçmez. Sınır kazım aracı PR'ında kondu ve kullanıcı kararıyla kesinleşti (2026-11-20, §7 madde 5); araç onu yerel saate de uygular. Bu bir ön-taahhüttür, gizli kazım penceresinin kanıtı değildir (§4.1). Sınırın içindeki fark yine yazdırılır.
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
- Adım adım prova talimatı ve ölçülecek süreler (ikinci makinedeki tam test paketi dahil): [LANSMAN-KILAVUZU.md](LANSMAN-KILAVUZU.md) §5.
- Ayrı bir "prova ağı" eklenmez.

### 4.5 Lansman betiği ve lansman commit'i istisnası

**Uygulandı (feat/launch-script; `scripts/lansman.py`, `scripts/lansman_commit_denetle.py`; kılavuz [LANSMAN-KILAVUZU.md](LANSMAN-KILAVUZU.md)). İstisna ve "sonradan ENGELLEYİCİ çıkarsa" kuralı onaylandı (kullanıcı kararı 2026-11-22, §7 madde 6).** Betik ve koruma yalnızca Python standart kitaplığını ve git'i kullanır; uzlaşma kodu değişmez.

**İstisna.** Lansman commit'i yayın anı − T_g ≤ 10 dk eşiği yüzünden `guvenlik-denetimi`'nden ve PR'dan geçemez. `devils-advocate` (2026-11-20) üç seçeneği sınadı: (a) commit'i yine de denetimden geçirmek (eşiğe sığmaz), (b) istisnasız olağan PR (genesis'i günlerce açıkta bırakır, "adil lansman"la bağdaşmaz), (c) önceden denetlenmiş mekanik koruma, iki makine ve sonradan denetim. Seçilen (c)'dir, üç önkoşulla:
1. İkinci makine adayın temiz klonunda **bütün testleri** çalıştırır (mainnet'in "tanımsız"dan "tanımlı"ya döndüğü ağaçta ilk tam koşu budur); süresi provada ölçülür ve eşiğe sığmazsa ikinci makine hızlandırılır, test paketi kısaltılmaz.
2. **İmza sırası:** gerçek anahtarla imza ancak ikinci makine doğruladıktan sonra atılır. Aday imzasız ve etiketsiz aktarılır.
3. **Sonradan ENGELLEYİCİ çıkarsa** ne yapılacağı önceden yazılır (kılavuz §6; kullanıcı kararı 2026-11-22): genesis korunur, düzeltme sürümü çıkar; yalnızca genesis'in kendisi geçersizse yeniden lansman yapılır.

**Koruma (`lansman_commit_denetle.py`)** lansman makinesinde `kaz` ve `yayimla`'da, ikinci makinede `dogrula`'da çalışır; saate bakmaz, her makinede aynı sonucu verir:
- Depo sığ değildir; replace başvurusu ve grafts yoktur (geçmişi olduğundan farklı gösterebilecek durumlar).
- Commit'in tek ebeveyni taban commit'tir. Taban elle yazılan bir özet değildir: günler önce duyurulmuş, duyurulan anahtarla imzalı açıklamalı bir etiketin gösterdiği commit'tir (`check_base_tag`).
- Fark tam olarak iki girdidir (`git diff-tree --raw`, ham nesnelerle): `core/mainnet.json` değişmiştir, `core/testdata/mainnet_vector.json` eklenmiştir, ikisi de olağan dosyadır (kip 100644). Başka yol, kip değişikliği, sembolik bağ ve alt modül yoktur; tabandaki lansman dosyası boş nesnedir.
- Dosyalar ASCII'dir, CR içermez; lansman dosyası kanoniktir ve bağımsız betik gerçek mainnet kurallarıyla ondan commit'teki vektörü bayt bayt üretir.
- Commit mesajı tam olarak `launch: Q-BTC mainnet genesis <özet>` satırıdır.
- Etiket açıklamalıdır, bu commit'i gösterir, nesnedeki adı başvuru adıdır, aynı adda dal yoktur, mesajı commit mesajıdır ve imzalayan anahtarın parmak izi duyurulan parmak izidir (anahtarlıktaki başka bir anahtarın geçerli imzası yetmez).

**Betiğin akışı** (kılavuz §3): `on-ucus` → `basliklar` → `kaz` (araç, kayıt denetimi, vektör, lansman testleri, imzasız commit, koruma) → `yayimla` (koruma; aday ikinci makineye; ikinci makinede `dogrula`; iki saatin karşılaştırılması; eşik; uzaktaki `main` tabanda ve etiket yok; imza; imzanın doğrulanması; eşik; `git push --atomic`, zorlamasız; sonucun uzak depodan okunması) → `kayit`. Kaz ve yayın arasındaki bağlar:
- Aracın kaydındaki commit taban, ağ mainnet, rapor özeti verilen raporun SHA-256'sı; commit'teki lansman dosyasının SHA-256'sı kayıttakiyle aynıdır.
- İkinci makinenin doğruladığı commit, genesis ve T_g lansman makinesindekilerle aynıdır.

**Betiğin eşikleri** (sabit; komut satırından gevşetilemez; testte sayı olarak sabit):
- Yayın anı − T_g ≤ 600 sn (§7 madde 3); imza ve itme için 30 sn pay: imzadan önce ve sonra T_g'den bu yana en çok 570 sn geçmiş olmalıdır.
- Saat eşitlemesi: işletim sistemi eşitlendiğini söyler (`timedatectl`) ve ölçülen sapma (`chronyc tracking` ya da `timedatectl timesync-status`) 1 sn'nin altındadır.
- T_g ikinci makinenin saatinden en çok 5 sn ileride olabilir; iki makinenin T_g'ye uzaklığı en çok 10 sn farklı olabilir.
- Mesajdaki Bitcoin bloğu iki bağımsız Esplora kaynağında (`mempool.space`, `blockstream.info`) en çok iş taşıyan zincirdedir, onaylayan blok onun hemen üstündedir ve iki kaynak aynı başlığı ve yüksekliği verir. Araç ve yükleyici bunu bilemez: bayat (yetim) bir blok da geçerli iş ispatı taşır.

**Yarım kalan durumlar:** imzalanmış ama itilmemiş etiket yerelde silinir (Ctrl-C dahil; kesintiden kalmışsa sonraki çalıştırma, uzakta etiket olmadığını ve dalın tabanda olduğunu gördükten sonra siler). Durum itmeden hemen önce "itiliyor" olarak kaydedilir; bu durumdaki deneme `at` ile atılamaz ve yeniden çalıştırma yalnızca sonucu okur (yayımlanmış bir genesis'in yerel kaydı böylece kaybolmaz). Ağ üzerindeki ssh ve git çağrıları istem beklemez (`BatchMode=yes`). İtme komutu hata verip uzak depo güncellenmişse betik bunu uzak depodan görür ve yayımlanmış sayar (yayın anı kesin değil olarak kaydedilir); uzak depo okunamazsa durum "belirsiz" kalır ve `yayimla` yeniden çalıştırıldığında yalnızca sonucu okur (yeniden imza atmaz). Durum dosyası (`durum.json`) her adımda atomik yazılır ve deponun dışındadır.

**Lansman kaydı** (`kayit`): kılavuz §3.6'daki alanlar; mesajdaki bloğun iki ayrı kaynaktaki ilk görülme zamanı zorunludur ve t_b − 2 sa ile yayın anı arasında olmalıdır. Kayıt imzalı bir etiketin (`<etiket>-kayit`) mesajıdır.

**Testler** (CI adımı, yerelde de çalışır):
- `scripts/lansman_test.py`: gerçek git (çıplak "herkese açık" depo, iki makine klonu), geçici SSH imza anahtarı; go, qbtc, ssh, saat ve Bitcoin kaynakları sahte. Sahte ssh, ikinci makinedeki komutu aynı süreçte ikinci makinenin klonuyla çalıştırır. Uçtan uca lansman (ikinci makine doğrulamadan imza atılmadığı, imzanın itmeden önce olduğu, ikinci makinenin imza atmadığı ve itmediği); atılan denemelerin kayda girdiği; ikinci makinede kırmızı test; eşik ve uzaktaki dal denetimleri imzadan önce; başarısız itmede etiketin silinmesi; hata verip yine de inen itme; bayat Bitcoin bloğu; iki makinenin saati; taban etiketi; kirli ağaç; saat eşitlemesi çıktıları; komut satırı.
- `scripts/lansman_commit_denetle_test.py`: korumanın her şartının bozulmuş hâli; gerçek kurallarla örneğin reddedildiği; imzanın duyurulan anahtarla doğrulanması.

**Kalanlar:**
- Betik gerçek GPU, gerçek belirteç ve gerçek ikinci makineyle hiç çalışmadı; ilk çalıştırma tam provadadır (kılavuz §5).
- Saat eşitlemesi denetimi yalnızca Linux (systemd) içindir.
- Bitcoin kaynakları iki üçüncü taraftır; ikisi de ulaşılamazsa lansman bekler. Başlatıcının kendi Bitcoin düğümü üçüncü bir kaynak olarak eklenmedi.
- Yayın anının kanıtı yine tanıklardır (§3.4); betik yalnızca kendi saatini yazar.
- İlk görülme zamanları başlatıcının girdisidir; betik yalnızca aralığını denetler.
- Dal ve etiket koruması başlatıcının GitHub ayarıdır; betik yalnızca uzaktaki `main`'in tabanda olduğunu denetler.
- Durum dizini kilitlenmez: iki terminalde aynı anda çalıştırılan komutlar durum dosyasını birbirinin üstüne yazabilir. Kılavuz tek terminal ister; yayımlanmış bir aday her durumda uzak depodan tanınır (`yayimla` kaydeder, `at` reddeder).
- OpenPGP imza yolu yalnızca elle yazılmış `VALIDSIG` satırlarıyla sınanır; duyurulan anahtar OpenPGP ise gerçek gpg ile ilk deneme provadadır.
- GitHub bir eşzamanlılık grubunda bir koşan ve bir bekleyen koşu tutar: lansman koşusu başka bir `main` koşusunun arkasında beklerken gelen bir itme onu iptal eder. Lansmandan sonra ilk saatte `main`'e itilmez; iptal olursa koşu elle yeniden başlatılır.
- `guvenlik-denetimi` bulguları üzerine değişenler: başarıyla dönen ya da zaman aşımına uğrayan itme hiçbir zaman "hazir"e indirilmez (en çok "belirsiz"); durum dosyası "hazir"de kalmış ama aday uzakta yayımlanmışsa `yayimla` yayını kaydeder, `at` uzak depoya bakar ve reddeder; itmenin süresi sınırlıdır (60 sn, ssh canlılık denetimi, HTTPS düşük hız sınırı); itme ve okuma aynı itme adresine bakar (`remote get-url --push`); `GIT_SSH_COMMAND` ve `GIT_CONFIG_*` ortamdan temizlenir; aday `--no-follow-tags` ile aktarılır; atılan denemede araç kaydı varsa okunur; etiket başvurusu bir kez nesne kimliğine çözülür ve tür, içerik ve imza o kimlikle okunur; fark `--ignore-submodules=none` ile alınır, yinelenen yol reddedilir; CI'da `ssh-keygen` yoksa imza sınamaları atlanmaz, düşer; eşik sınaması artık lansman makinesinin kendi denetimini (imzadan önce ve sonra) sınar.

## 5. PR sırası

Her biri ayrı PR, test ve `guvenlik-denetimi` ile:

1. **ML-DSA-65 bilinen-yanıt denetimi:** sabit vektör testte ve düğüm açılışında (kriptografi; lansman araçlarından bağımsız ve en acil olanı). **Kapandı (feat/mldsa-kat).**
2. Bağımsız vektör betiği ve CI adımı. (Uzlaşma kodu değişmez.) **Kapandı (feat/genesis-vector).**
3. Gömülü mainnet genesis dosyası ve lansman şartı testleri (`core/params.go`). **Kapandı (feat/mainnet-launch-file).**
4. Genesis kazım aracı, ön-uçuş kipi ve lansman kaydı. **Kapandı (feat/genesis-mine-tool).**
5. Lansman betiği ve çalıştırma kılavuzu; prova talimatı; lansman commit'i istisnası. Araçta olmayanlar buradadır: saat eşitlemesi denetimi, ikinci makine, "yayın anı − T_g ≤ 10 dk" eşiği, mesajdaki Bitcoin bloğunun iki bağımsız gezgindeki ilk görülme zamanının kayda yazılması (gizli kazım penceresinin gerçek ölçüsü budur; başlıktaki zaman madenci beyanıdır), atılan denemelerin toplanması ve kaydın imzalanması (§4.1 "Kalanlar"). **Kapandı (feat/launch-script; §4.5); istisna onaylandı (§7 madde 6).**
6. README ve LICENSE (lisans kararından sonra). **Kapandı (feat/readme-license).**

Depo dışındaki işler (§2.2) bu sıradan bağımsızdır; GPU ölçümü 4. PR'dan önce yapılırsa araç gerçek donanımda da denenir.

## 6. Karar soruları

Yanıtlar §7'dedir. İlk taslağın önerilerinden ikisi `devils-advocate` sonrasında değişti: yayın sırası (B'den A'ya) ve Bitcoin bloğu (uç bloktan bir onaylı bloğa).

1. **Yayın sırası** (§3.3): A (genesis doğrulanır doğrulanmaz) mı, B (önce 1.–2. blok) mi? Öneri: A.
2. **Bitcoin bloğu:** bir onay almış blok mu (öneri), o andaki uç blok mu? Uç blok yayın anı − t_b'yi ~10 dk kısaltır, ama bayat kalırsa genesis'te kalıcı bir leke olur; rampa yüzünden o 10 dakikanın değeri ihmal edilebilir.
3. **"Birkaç dakika" eşiği ve iptal kuralı:** yayın anı − T_g için sayısal eşik (öneri: 10 dk; aşılırsa deneme atılır, yeni Bitcoin bloğuyla baştan başlanır, atılan denemeler kayda yazılır).
4. **Lisans:** MIT, Apache-2.0 ya da GPL-3.0? Öneri: MIT (Bitcoin Core ile aynı; bağımlılıklar liboqs, liboqs-go, goleveldb izin verici lisanslıdır; kesin uyum denetimi lisans PR'ında yapılır). Apache-2.0'ın farkı açık patent lisansıdır; kurumsal entegratörler bunu sorabilir. Bu hukuki bir seçimdir, karar başlatıcınındır.

Soru sorulmadan uygulanacak tasarım seçimleri (itiraz gelirse PR'da değişir): lansman commit'inin veri dosyası olması (§4.2), mesajda yükseklik olmaması, T_g'nin tazelenmesi, ölçüm raporuyla öz-tutarlılık denetimi. Araç PR'ında aynı yolla belirlenenler aracın kendi saat denetimleridir (§4.1). T_g − t_b ≤ 3600 sn şartı bu sınıfa girmez: madde 6(b)'yi daraltan bir lansman kararıdır; kullanıcıya soruldu (§7 madde 5).

## 7. Kararlar (kullanıcı, 2026-11-18; madde 5 2026-11-20, madde 6 2026-11-22)

1. **Yayın sırası A.** Genesis ikinci makinede doğrulanır doğrulanmaz yayımlanır; 1. ve 2. blok yayından sonra herkese açık kazılır. Dondurulmuş "başlatıcı genesis'i ve 1.–2. bloğu yalnızca bu GPU kazıcısıyla ve aynı donanımla kazar" cümlesi şöyle okunur: başlatıcı genesis'i yalnızca açıklanan GPU kazıcısıyla kazar ve yayından sonra da (1.–2. blok dahil) yalnızca onunla kazar; bu blokları başlatıcının kazanacağı garanti edilmez.
2. **Genesis mesajına bir onay almış Bitcoin bloğunun özeti girer.** Araç iki başlık alır ve ikisinin de iş ispatını doğrular.
3. **Eşik: yayın anı − T_g ≤ 10 dakika.** Aşılırsa ya da bir denetim düşerse deneme atılır, yeni bir Bitcoin bloğuyla baştan başlanır; atılan her deneme lansman kaydına yazılır.
4. **Lisans MIT.**
5. **T_g − t_b ≤ 3600 sn (2026-11-20, `devils-advocate` sonrası).** Genesis'in zaman damgası, mesajdaki Bitcoin bloğunun zamanından en çok bir saat sonra olabilir; şart lansman dosyası yükleyicisinde, bağımsız betikte ve kazım aracındadır. Bir ön-taahhüttür: bayat bir Bitcoin bloğuyla kurulmuş lansman dosyasını her düğüm reddeder. Gizli kazım penceresinin kanıtı olarak sunulmaz (§4.1 "Sınırların gerekçesi"). Seçilmeyenler: şartın yalnızca araç ve betikte kalması; 2 saat.

6. **Lansman commit'i istisnası ve sonradan denetim (kullanıcı kararı 2026-11-22, `devils-advocate` sonrası).** Lansman commit'i PR'dan ve `guvenlik-denetimi`'nden geçmez; yerine önceden denetlenmiş mekanik koruma (`lansman_commit_denetle.py`), ikinci makinede adayın temiz klonunda bütün testler ve yayından sonra CI ile `guvenlik-denetimi` konur (§4.5). Gerçek anahtarla imza ancak ikinci makine doğruladıktan sonra atılır. Sonradan CI ya da denetim ENGELLEYİCİ çıkarırsa genesis korunur ve düzeltme sürümü çıkar; yeniden lansman yalnızca genesis'in kendisi geçersizse (yükleyici, betik ya da Go testi onu reddediyorsa) yapılır (kılavuz §6). Seçilmeyenler: commit'i yine de denetimden geçirmek (eşiğe sığmaz); istisnasız olağan PR (genesis'i günlerce açıkta bırakır); her ENGELLEYİCİ bulguda yeniden lansman.
