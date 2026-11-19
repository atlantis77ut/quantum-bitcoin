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

Madde 6(a)'nın "kazıcılar yazılır" kısmı yani kapanmıştır. Aşağıdaki eksikler kapanmadan lansman yapılamaz.

## 2. Eksikler

### 2.1 Kod ve araç (PR sırası §5'te)

1. **Düğüm, bağlandığı liboqs'un doğru ML-DSA-65'i uyguladığını denetlemiyor.** liboqs sürümü yalnızca CI'da sabit (0.15.0). Depoda sabit bir bilinen-yanıt vektörü yok; testler yalnızca imzala-doğrula gidiş dönüşünü sınar. 0.12.0'dan önceki liboqs ML-DSA'nın taslak sürümünü uygular. Eski bir kurulumla derleyen katılımcı ilk 240 blokta uyumlu görünür (bu bloklarda imzalı işlem yoktur: coinbase olgunluğu 240), ilk harcamayı içeren blokta zincirden kopar. Lansman günü denetimlerinin hiçbiri bunu görmez. **Kapandı (feat/mldsa-kat):** düğüm her kipten önce `crypto.SelfTest` ile sabit bir vektörü denetler ve geçemezse çalışmaz. **Düzeltme:** bu maddedeki "eski bir kurulumla derleyen katılımcı" senaryosu depodaki liboqs-go sürümüyle oluşmaz (0.12.0'dan eski liboqs ile derleme başarısız olur; kaynaktan çıkarım, denenmedi); denetimin kapattığı gerçek durum ML-DSA-65'i etkin olmayan ya da farklı davranan bir liboqs'tur. Ayrıntı ve kalanlar: [MAINNET-ONCESI.md](MAINNET-ONCESI.md) madde 6.
2. **Genesis'i kazıyan araç yok.** Tek iz `grindGenesis` test yardımcısıdır (tek çekirdek, 50 milyon nonce sınırı). Mainnet başlangıç hedefinde genesis yalnızca GPU kazıcısıyla ve dakikalar içinde bulunabilir.
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
- **Gizli kazım penceresi yayın anı − t_b ile sınırlıdır**, yayın anı − T_g ile değil: başlatıcı T_g'yi ileri bir ana koyup o ana kadar gizlice kazabilir. Dışarıdan doğrulanabilen ölçü yayın anı − t_b'dir; lansman kaydına yazılır.
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

### 4.1 Genesis kazım aracı (`-mine-genesis`)

Girdi: Bitcoin blok başlıkları (80 bayt, hex) ve ölçüm raporu dosyası.

Davranış:
- **Bitcoin başlıklarını kendisi doğrular.** Özetleri hesaplar; her başlığın kendi hedefini karşıladığını ve bu hedefin gerçek Bitcoin zorluğu ölçeğinde olduğunu denetler. Zorluk tabanı gömülüdür ve geniş bir payla seçilir (ör. prova günündeki zorluğun 1/16'sı): Bitcoin zorluğu iki haftada bir ayarlanır, aşağı yönlü tek bir ayarlama lansman günü gerçek başlıkları reddettirmemelidir. Araç iki başlık alır: mesaja giren (bir onay almış) blok ve onun üstündeki blok; üstteki başlığın mesajdaki bloğa bağlandığını da denetler.
- **Mesaj:** `Q-BTC mainnet genesis | BTC <özet>`. Blok yüksekliği mesaja girmez: başlıkta yükseklik yoktur, elle girilen ve doğrulanamayan bir sayı genesis'e kalıcı bir yazım hatası olarak girebilir. Yükseklik yalnızca lansman kaydında, bilgi olarak durur.
- **T_g kuralı:** T_g yerel saattir ve t_b'den küçük olamaz. Yerel saat verilen Bitcoin başlıklarının zamanlarıyla tutarsızsa araç durur (sınırlar araç PR'ında belirlenir; amaç saat dilimi ve donanım saati kaynaklı kaymaları yakalamaktır). Arama sürerken T_g her 10 saniyede bir tazelenir; başlık değiştiği için arama yeni başlıkla sürer, beklenen süre değişmez. Bulunan nonce yalnızca kazıldığı başlıkla birlikte yazılır ve yazılan dosya yeniden yüklenip CPU'da doğrulanır.
- **Yalnızca GPU.** Mainnet'te OpenCL aygıtının türü GPU olmalıdır (ölçümdeki `benchDevice` kuralı).
- **Ölçüm raporuyla öz-tutarlılık.** Araç şunları denetler, tutmuyorsa durur: `MainnetParams`'taki başlangıç ve taban, rapordaki ortanca hashrate'ten `core.LaunchTargets` ile hesaplanan değerlerdir (taban kuralı: min(başlangıç · 2^16, 0x1d00ffff)); raporun commit'i temizdir; raporun çekirdek özeti çalışan ikiliyle, aygıt modeli açılan aygıtla aynıdır. Bu denetim başlatıcıyı kendi hatasına karşı korur; kamuya "aynı donanım kanıtı" diye sunulmaz. Kamusal kanıt, başkalarının aynı modelde ölçümü yeniden üretmesidir.
- **Ön-uçuş kipi:** aynı denetimleri genesis kazmadan çalıştırır; lansmandan saatler önce koşulur.
- **Çıktı:** lansman dosyası (§4.2) ve lansman kaydı (denenen nonce sayısı, süre, T_g, t_b, aygıt, commit, rapor özeti).
- **Reddettikleri:** mainnet genesis'i zaten tanımlıysa çalışmaz.

### 4.2 Veri dosyaları: lansman commit'i yalnızca veri olsun

**Kapandı (feat/mainnet-launch-file; `core/launch.go`).** Lansman commit'i Go kaynağına dokunmaz, iki küçük veri dosyasını değiştirir:

- **`core/mainnet.json`** (ikiliye `go:embed` ile gömülür): ağ adı, genesis'in zaman damgası, mesajı, başlangıç hedefi, taban, nonce'u, özeti ve iki Bitcoin başlığı. Dosya bağımsız betiğin girdi biçimindedir (§4.3); betik onu doğrudan okur.
  - Lansmandan önce dosya boş bir nesnedir (`{}`) ve `MainnetParams` genesis'siz kalır.
  - Doluysa eksiksiz ve kanonik olmalıdır: çözülen içerik yeniden kodlandığında dosyayla bayt bayt aynı çıkmalıdır. Bilinmeyen ya da eksik alan, yinelenen anahtar, kesirli sayı ve artık veri böylece reddedilir.
  - Dolu dosya lansman şartlarını paket başlatılırken denetler; tutmuyorsa ikili hiçbir ağda başlamaz (panik). Şartlar: ağ adı `mainnet`; hedefler seçilmiştir (`mainnetTargetsChosen`, yer tutucu değildir); dosyadaki hedefler `MainnetParams.Bits` ve `MainnetParams.PowLimitBits`; taban kuralı; iki Bitcoin başlığının iş ispatı, zorluk ölçeği ve bağı; mesaj biçimi; T_g ≥ t_b; alanlardan kurulan genesis'in özeti dosyadaki özettir ve hedefi karşılar.
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
- Kazım aracının sahte arka uçla uçtan uca testi araç PR'ındadır (§4.1).

Korunanlar ve lansman sonrası:
- Testnet ve regtest vektörleri bugünkü gibi elle yazılmış Go sabitleri olarak kalır. Üreteç betiğiyle veri dosyasını birlikte yeniden üreten bir PR onları sessizce değiştiremez.
- Lansmandan sonraki ilk PR mainnet özetini aynı listeye elle yazılmış bir sabit olarak ekler ve iki veri dosyasının değişmesini reddeden bir CI denetimi koyar.
- Lansman commit'i `guvenlik-denetimi`'nden geçecek zamanı bulamaz. Bu istisnanın yerine önceden denetlenmiş mekanik bir koruma konur (commit yalnızca bu iki dosyaya dokunur ve şemaya uyar). İstisnanın onayı çalıştırma kılavuzu PR'ında sorulur.

Seçilmeyen seçenek: genesis'i çalışma anında bir dosyadan ya da bayraktan okumak. Katılımcıları yeniden derlemekten kurtarırdı, ama düğüme kalıcı bir "bana verilen genesis'e inan" yolu ekler ve sahte ağlara yönlendirmeyi kolaylaştırır.

### 4.3 Bağımsız vektör betiği

**Kapandı (feat/genesis-vector).** `scripts/genesis_vektor.py`, yalnızca Python standart kitaplığıyla (3.8 ya da sonrası) ve Go kodunu çağırmadan:
- Girdiden (ağ adı, mesaj, zaman damgası, başlangıç hedefi, zorluk tabanı, nonce) alıcı adresini, coinbase kimliğini, Merkle kökünü ve blok özetini hesaplar. İki hedefin kodlamasının kanonik olduğunu, başlangıç hedefinin tabandan ve tabanın 2^252 − 1'den kolay olmadığını (`Params.Validate` ile aynı şartlar) ve özetin hedefi karşıladığını denetler. Taban başlığa girmez; vektör onu da sabitlesin diye girdidedir.
- Ağın imza alanını ve sabit baytlı bir bilinen-yanıt işleminin kimliği ile o alanda imzalanan mesajının özetini de hesaplar. Böylece vektör, mainnet imzalarının bağlanacağı alanı da sabitler.
- Mainnet için iki Bitcoin başlığını kendisi doğrular: her biri kendi iş ispatını taşımalı, hedefleri gerçek Bitcoin zorluğu ölçeğinde olmalı, ikincisi ilkine bağlanmalıdır. Mesaj tam olarak `Q-BTC mainnet genesis | BTC <ilk başlığın özeti>` olmalı, T_g ≥ t_b tutmalı, taban tam olarak min(başlangıç hedefi · 2^16, 0x1d00ffff) olmalıdır (madde 6a'daki taban kuralı).

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
- Betikteki Bitcoin zorluk tabanı (`BITCOIN_MAX_BITS`) 976600. bloğun (2026-11-19) zorluğunun 1/16'sıdır ve tam provada yenilenir. Go tarafındaki karşılığı `core/launch.go`'daki `launchBitcoinMaxBits`'tir; bugün yalnızca lansman dosyası yükleyicisi kullanır, kazım aracının (§4.1) yazdığı dosya da aynı yükleyiciden geçecektir. İki sabit provada birlikte yenilenir ve denenir.
- T_g − t_b için üst sınır yoktur: gerçek ama bayat bir Bitcoin bloğu betikten geçer. Betik farkı yazdırır; sınır, yerel saat ile başlık zamanlarının tutarlılığı olarak kazım aracı PR'ında belirlenir (§4.1) ve betiğe de eklenir.
- Betik başlıkların en çok iş taşıyan Bitcoin zincirinde olduğunu bilemez. Özet herkesçe gezgin sitelerinden denetlenir; bir onay şartı bayat blok riskini azaltır, kaldırmaz.
- Betik başlangıç hedefinin ölçülen hashrate'e karşılık geldiğini bilemez (girdide hashrate yoktur). Bugünkü yer tutucu değerlerle (başlangıç = taban = 0x1d00ffff) kazılmış bir genesis taban kuralına uyar ve betikten geçer. Go tarafında onu `mainnetTargetsChosen` işareti durdurur (§4.2): hedefler seçilip işaret true yapılmadan dolu lansman dosyası reddedilir. Seçilen hedeflerin ölçülen hashrate'e karşılık geldiğini denetleyecek olan ise kazım aracının ölçüm raporuyla öz-tutarlılık denetimidir (§4.1).
- Regtest ve testnette taban için başlangıç hedefiyle sıralama dışında kural yoktur; değerini ağın parametresine Go testi bağlar.

### 4.4 Prova

- **CI düzeyinde:** araç mantığı kolay hedef ve sahte arka uçla Go testinde, vektör betiği CI adımında çalışır.
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
4. Genesis kazım aracı, ön-uçuş kipi ve lansman kaydı.
5. Lansman betiği ve çalıştırma kılavuzu; prova talimatı; lansman commit'i istisnası.
6. README ve LICENSE (lisans kararından sonra).

Depo dışındaki işler (§2.2) bu sıradan bağımsızdır; GPU ölçümü 4. PR'dan önce yapılırsa araç gerçek donanımda da denenir.

## 6. Karar soruları

Yanıtlar §7'dedir. İlk taslağın önerilerinden ikisi `devils-advocate` sonrasında değişti: yayın sırası (B'den A'ya) ve Bitcoin bloğu (uç bloktan bir onaylı bloğa).

1. **Yayın sırası** (§3.3): A (genesis doğrulanır doğrulanmaz) mı, B (önce 1.–2. blok) mi? Öneri: A.
2. **Bitcoin bloğu:** bir onay almış blok mu (öneri), o andaki uç blok mu? Uç blok yayın anı − t_b'yi ~10 dk kısaltır, ama bayat kalırsa genesis'te kalıcı bir leke olur; rampa yüzünden o 10 dakikanın değeri ihmal edilebilir.
3. **"Birkaç dakika" eşiği ve iptal kuralı:** yayın anı − T_g için sayısal eşik (öneri: 10 dk; aşılırsa deneme atılır, yeni Bitcoin bloğuyla baştan başlanır, atılan denemeler kayda yazılır).
4. **Lisans:** MIT, Apache-2.0 ya da GPL-3.0? Öneri: MIT (Bitcoin Core ile aynı; bağımlılıklar liboqs, liboqs-go, goleveldb izin verici lisanslıdır; kesin uyum denetimi lisans PR'ında yapılır). Apache-2.0'ın farkı açık patent lisansıdır; kurumsal entegratörler bunu sorabilir. Bu hukuki bir seçimdir, karar başlatıcınındır.

Soru sorulmadan uygulanacak tasarım seçimleri (itiraz gelirse PR'da değişir): lansman commit'inin veri dosyası olması (§4.2), mesajda yükseklik olmaması, T_g'nin tazelenmesi, ölçüm raporuyla öz-tutarlılık denetimi.

## 7. Kararlar (kullanıcı, 2026-11-18)

1. **Yayın sırası A.** Genesis ikinci makinede doğrulanır doğrulanmaz yayımlanır; 1. ve 2. blok yayından sonra herkese açık kazılır. Dondurulmuş "başlatıcı genesis'i ve 1.–2. bloğu yalnızca bu GPU kazıcısıyla ve aynı donanımla kazar" cümlesi şöyle okunur: başlatıcı genesis'i yalnızca açıklanan GPU kazıcısıyla kazar ve yayından sonra da (1.–2. blok dahil) yalnızca onunla kazar; bu blokları başlatıcının kazanacağı garanti edilmez.
2. **Genesis mesajına bir onay almış Bitcoin bloğunun özeti girer.** Araç iki başlık alır ve ikisinin de iş ispatını doğrular.
3. **Eşik: yayın anı − T_g ≤ 10 dakika.** Aşılırsa ya da bir denetim düşerse deneme atılır, yeni bir Bitcoin bloğuyla baştan başlanır; atılan her deneme lansman kaydına yazılır.
4. **Lisans MIT.**

Açık kalan: lansman commit'inin `guvenlik-denetimi` istisnası ve yerine konacak mekanik koruma (§4.2), çalıştırma kılavuzu PR'ında sorulur.
