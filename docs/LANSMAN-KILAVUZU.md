# Lansman çalıştırma kılavuzu

**Durum:** hazır (feat/launch-script); gerçek donanımla ilk çalıştırma tam provadadır. Lansman commit'i istisnası ve "sonradan ENGELLEYİCİ çıkarsa" kuralı onaylandı (kullanıcı kararı 2026-11-22; [LANSMAN.md](LANSMAN.md) §4.5, §7 madde 6). Bu kılavuz uzlaşma kuralı getirmez; tasarım ve gerekçeler [LANSMAN.md](LANSMAN.md)'dedir.

Bu kılavuz lansman günü ve provada hangi komutun hangi makinede, hangi sırayla çalıştırılacağını anlatır. Komutlar `scripts/lansman.py`'nin komutlarıdır; betik eşikleri kendisi denetler ve gevşetilmelerine izin vermez.

## 1. Roller ve makineler

- **Lansman makinesi:** ölçülmüş GPU'nun takılı olduğu Linux makine. Genesis'i kazar, etiketi imzalar ve iter. İmza anahtarı burada değil, takılı bir donanım belirtecindedir (dokunma onaylı).
- **İkinci makine:** başka bir ağda, kendi saat eşitlemesi ve kendi liboqs kurulumuyla bir Linux makine (ör. küçük bir sunucu). Adayı temiz bir klonda doğrular. İmza anahtarına erişimi yoktur, hiçbir şey itmez.
- **Herkese açık depo:** `origin` (GitHub). Lansman makinesinden ona yalnızca bir kez, dal ve etiket birlikte itilir.

İkinci makine lansman makinesinden ssh ile, parolasız (ssh-agent ya da belirteç) ulaşılabilir olmalıdır. Betik ağ üzerindeki ssh ve git çağrılarını istem beklemeden (`BatchMode=yes`, 15 sn bağlantı süresi) yapar: bir ana makine anahtarı ya da parola istemi pencereyi tüketmesin, adım hemen düşsün. İkinci makinenin ve `origin`'in ana makine anahtarları provada `known_hosts`'a kabul edilir. Adayın aktarıldığı yer ikinci makinedeki depodur (`ssh://<ikinci-ssh><ikinci-dizin>`), herkese açık depo değildir.

## 2. Günler önce

1. GPU ölçümü yapılır ve rapor yayımlanır ([GPU-OLCUM.md](GPU-OLCUM.md)).
2. Hedef PR'ı birleştirilir (`MainnetParams` başlangıç hedefi ve tabanı, `mainnetTargetsChosen = true`; [LANSMAN.md](LANSMAN.md) §3.4).
3. **Taban etiketi:** lansman öncesi son commit (`main`'in ucu), duyurulan anahtarla imzalı açıklamalı bir etikete bağlanır ve itilir:
   ```bash
   git tag -s -m "Q-BTC lansman oncesi taban" lansman-oncesi <commit>
   git push origin refs/tags/lansman-oncesi
   ```
   Etiketin adı, gösterdiği commit, anahtarın parmak izi ve lansman etiketinin adı (ör. `v-mainnet`) duyurulur. Bu andan lansmana kadar `main`'e hiçbir şey birleştirilmez: betik uzaktaki `main`'in tam olarak bu commit'te olduğunu denetler ve değilse durur.
4. İki makinede de depo bu etiketten klonlanır ve etikete geçilir (`git checkout --detach lansman-oncesi`). Çalışma ağacı temiz olmalıdır; durum dizini deponun dışında olmalıdır.
5. İki makinede git, imzayı doğrulayabilecek biçimde yapılandırılır: SSH anahtarı için `gpg.format ssh` ve `gpg.ssh.allowedSignersFile` (yalnızca duyurulan anahtar), OpenPGP için anahtarlıkta duyurulan anahtar. Lansman makinesinde ayrıca `user.signingkey` belirteçteki anahtardır.
6. Saat eşitlemesi kurulur (chrony ya da systemd-timesyncd). Windows ile çift önyüklemeli makinede donanım saati UTC'ye alınır.
7. Prova yapılır (§5). Prova süreleri kayda geçer.

Öneri (başlatıcının GitHub ayarı; betik değiştirmez): `main` için zorla itmeyi ve silmeyi yasaklayan dal koruması, `v-*` ve `lansman-*` etiketleri için etiket koruması. Koruma lansman itmesini engelleyebileceğinden ayarlandıktan sonra itme adımı özel bir deneme deposunda değil, aynı kurallarla yeniden prova edilir.

## 3. Lansman günü

Aşağıdaki örneklerde ortak seçenekler kısaltılmıştır:

```bash
ORTAK="--durum $HOME/qbtc-lansman --taban lansman-oncesi --etiket v-mainnet --anahtar SHA256:<parmak izi>"
IKINCI="--ikinci-ssh dogrulayici@ikinci.example --ikinci-dizin /srv/qbtc --ikinci-durum /srv/qbtc-lansman"
```

### 3.1 Saatler önce: ön-uçuş

İkinci makinede (bağımsız, isteğe bağlı; ön-uçuş zaten ssh ile çalıştırır):

```bash
python3 -I scripts/lansman.py hazir $ORTAK
```

Lansman makinesinde:

```bash
python3 -I scripts/lansman.py basliklar
python3 -I scripts/lansman.py on-ucus $ORTAK $IKINCI --blok <hex> --onay <hex> --rapor qbtc-olcum-<zaman>.json
```

`hazir` ve `on-ucus` şunları denetler: saat eşitlenmiş ve sapma 1 sn'nin altında; taban etiketi duyurulan anahtarla imzalı ve ağaç tam olarak onun commit'inde, temiz; derleme ve **bütün testler** (`go test ./...`) geçer. `on-ucus` ayrıca ikiliyi `-tags opencl` ile derler, başlıkların iki kaynakta en çok iş taşıyan zincirde olduğunu denetler, aracın ön-uçuşunu (`-launch-preflight`: saat, rapor, aygıt, kısa hashrate ölçümü) çalıştırır ve ikinci makinede `hazir`'ı çalıştırır. Ön-uçuş lansmandan hemen önce yinelenir.

### 3.2 Bitcoin başlıkları

```bash
python3 -I scripts/lansman.py basliklar
```

Betik uç bloğu ve ebeveynini iki bağımsız kaynaktan (`mempool.space` ve `blockstream.info` Esplora arayüzleri) alır, iki kaynağın aynı uç bloğu gösterdiğini, ebeveynin ikisinde de en çok iş taşıyan zincirde olduğunu ve uç bloğun onun üstünde durduğunu denetler, `--blok` ve `--onay` değerlerini yazdırır. Kaynaklar farklı uç gösterirse birkaç saniye sonra yinelenir. En iyisi yeni bir Bitcoin bloğu gelir gelmez başlamaktır: uç bloğun yaşı küçükse aracın saat denetimleri en sıkı hâlindedir ve T_g − t_b ≤ 3600 sn şartı rahat tutar.

Başlatıcı, mesaja girecek bloğun her iki gezginde ilk göründüğü anı not eder (gezginin "ilk görülme" ya da "alındı" zamanı; yoksa bloğun yayımlandığı duyurunun damgası). Bu iki zaman lansman kaydına girer (§3.6).

### 3.3 Kazım ve aday

```bash
python3 -I scripts/lansman.py kaz $ORTAK --blok <hex> --onay <hex> --rapor qbtc-olcum-<zaman>.json
```

Betik başlıkları yeniden iki kaynakta denetler, aracı (`-mine-genesis`) çalıştırır, aracın kaydını okur ve şunları ister: kayıttaki commit tabandır, ağ mainnet'tir, rapor özeti verilen raporun SHA-256'sıdır. Sonra vektörü bağımsız betiğin koduyla üretir, lansman testlerini (`go test -run 'Launch|Genesis|Mainnet|Network|Params' ./core/ .`) çalıştırır, yalnızca iki veri dosyasını sabit mesajla (`launch: Q-BTC mainnet genesis <özet>`) commit'ler ve korumayı (`lansman_commit_denetle.py`) çalıştırır; commit'teki lansman dosyasının SHA-256'sı aracın kaydındakiyle aynı olmalıdır. **Etiket atılmaz, hiçbir şey itilmez.**

### 3.4 Doğrulama ve yayın

```bash
python3 -I scripts/lansman.py yayimla $ORTAK $IKINCI
```

Sıra sabittir:

1. Koruma lansman makinesinde yeniden çalışır.
2. İmzasız aday ikinci makinedeki depoya `refs/lansman/aday` olarak aktarılır.
3. İkinci makinede `dogrula` çalışır: saat eşitlemesi; ikinci makinenin ağacı tabanda ve temiz; adayın temiz bir klonu; koruma; T_g ikinci makinenin saatinden en çok 5 sn ileride; başlıklar iki kaynakta en çok iş taşıyan zincirde; derleme; **bütün testler**. Sonuç tek satırdır (`DOGRULANDI commit=… genesis=… T_g=… saat_farki=… sure=…`).
4. Lansman makinesi sonucu okur; iki makinenin T_g'ye uzaklığı en çok 10 sn farklı olabilir.
5. Eşik denetimi: T_g'den bu yana en çok 570 sn geçmiş olmalıdır (600 sn eşik, 30 sn pay).
6. Uzaktaki `main` tam olarak taban commit'tedir ve uzakta lansman etiketi yoktur.
7. **Etiket ancak şimdi imzalanır** (belirteç dokunma onayı ister) ve duyurulan anahtarla imzalandığı doğrulanır; eşik yeniden denetlenir.
8. Dal ve etiket tek adımda, zorlamadan itilir (`git push --atomic origin <commit>:refs/heads/main refs/tags/v-mainnet:refs/tags/v-mainnet`).
9. Sonuç uzak depodan okunur: dal ve etiket bu commit'i göstermelidir. Yayın anı itme komutunun döndüğü andır.

Bir adım düşerse betik durur ve hiçbir şey yayımlanmamış olur; imzalanmış ama itilmemiş etiket yerelde silinir. İtme komutu hata verip uzak depo yine de güncellenmişse betik bunu uzak depodan görür ve yayımlanmış sayar (yayın anı kesin değildir, kayda öyle yazılır). Uzak depo okunamazsa durum "belirsiz" kalır: bağlantı gelince `yayimla` yeniden çalıştırılır ve yalnızca sonucu okur. Aynı durum dizinine aynı anda tek komut çalışır (işletim sistemi kilidi; ikinci komut hemen düşer). Durum itmeden hemen önce "itiliyor" olarak kaydedilir: süreç itme sırasında kesilirse (Ctrl-C, bağlantı, elektrik) yeniden çalıştırılan `yayimla` doğrulamayı ve imzayı yinelemez, yalnızca sonucu okur; bu durumdaki deneme `at` ile atılamaz.

### 3.5 Atma

Eşik aşılırsa, bir denetim düşerse ya da araç durursa deneme atılır ve yeni bir Bitcoin bloğuyla §3.2'den başlanır:

```bash
python3 -I scripts/lansman.py at $ORTAK --neden "<neden>"
```

`at` yalnızca iki veri dosyasına dokunan, tabanın hemen üstündeki yayımlanmamış commit'i ve yerel etiketi geri alır; başka bir değişiklik görürse durur. Atılan her deneme (Bitcoin bloğu, başlıklar, varsa genesis ve aracın kaydı, neden) durum dizininde kalır ve lansman kaydına girer. Ne zaman atılır:

- Eşik (yayın anı − T_g ≤ 600 sn) tutturulamayacaksa.
- Herhangi bir denetim düştüyse ve nedeni bu deneme içinde giderilemiyorsa (ör. ikinci makinede bir test kırmızı).
- Araç durduysa (bayat başlık, saat, çıktı hatası).

### 3.6 Lansman kaydı

Yayından sonra, başlatıcının düğümü ve tohumlar başladıktan sonra:

```bash
python3 -I scripts/lansman.py kayit $ORTAK --ilk-gorulme mempool.space=<unix> --ilk-gorulme blockstream.info=<unix>
```

Kayıt (`lansman-kaydi.json`) şunları içerir: genesis özeti, lansman ve taban commit'i, etiketler, anahtarın parmak izi, T_g, Bitcoin bloğunun özeti, yüksekliği ve zamanı (t_b), iki kaynaktaki ilk görülme zamanı, yayın anı ve kesin olup olmadığı, yayın anı − T_g, yayın anı − t_b, yayın anı − ilk görülme (gizli kazım penceresinin gerçek ölçüsü), ikinci makinenin sonucu ve süresi, aracın kaydı ve atılan bütün denemeler. Kayıt `v-mainnet-kayit` adlı imzalı etiketin mesajı olarak yazılır ve itilir; ayrıca duyurulur.

## 4. Anahtar

- Gerçek anahtar yalnızca lansman makinesinde, belirteçte durur; ikinci makinede bulunmaz.
- Gerçek anahtarla yalnızca iki şey imzalanır: taban etiketi (günler önce) ve lansman günü `yayimla`'nın 7. adımı ile `kayit`. İkinci makine doğrulamadan gerçek anahtarla hiçbir şey imzalanmaz.
- Prova ayrı, atılacak bir anahtarla ve farklı etiket adlarıyla yapılır; gerçek anahtarla imzalı geçerli ikinci bir "mainnet" etiketi hiçbir zaman üretilmez.

## 5. Prova

Tam prova lansmandan önce en az bir kez, iki makinede yapılır: Bulgular ve süreler [PROVA-KAYDI.md](PROVA-KAYDI.md) şablonuna yazılır (her prova için bir kopya):

1. Her iki makinede deponun **ayrı bir klonu** (çalışma ağacı kopyası değil) ve ayrı durum dizinleri.
2. Atılacak bir prova anahtarı; `allowedSignersFile` yalnızca prova anahtarını içerir. Etiket adları `prova-oncesi`, `v-prova`.
3. Herkese açık depo yerine özel bir deneme deposu (`--uzak` ile verilir). Dal korumaları gerçek depodakiyle aynı ayarlanır.
4. Gerçek başlangıç hedefi ve güncel Bitcoin başlıkları; betikteki ve araçtaki Bitcoin zorluk tabanı o günün zorluğuna göre yenilenmiştir ([LANSMAN.md](LANSMAN.md) §4.3).
5. Bütün akış: `on-ucus`, `basliklar`, `kaz`, `yayimla`, `kayit`; en az bir deneme kasıtlı olarak `at` ile atılır.
6. **Süreler ölçülür** ve kayda geçer: aracın genesis'i bulma süresi, `kaz`'ın toplamı, ikinci makinedeki tam test paketinin süresi (`dogrula` satırındaki `sure`), `yayimla`'nın toplamı. Toplam, T_g'den itmeye kadar 570 sn'nin rahatça altında kalmalıdır. Kalmıyorsa (ör. tam test paketi ikinci makinede çok uzun sürüyorsa) ikinci makine hızlandırılır; eşik gevşetilmez ve tam test paketi atlanmaz.
7. Provadan sonra prova durum dizinleri, veri dizinleri ve deneme deposu silinir (kalan bir prova zinciri lansman günü düğümün açılmasını engeller).

## 6. Sonradan yapılan denetimler

Lansman commit'i yayın anı − T_g eşiği yüzünden `guvenlik-denetimi`'nden ve PR'dan geçmez; yerine önceden denetlenmiş mekanik koruma ve iki makine vardır ([LANSMAN.md](LANSMAN.md) §4.5). Yayından sonra:

1. `main`'deki CI koşusu izlenir (main'deki koşular iptal edilmez).
2. Lansman commit'i `guvenlik-denetimi`'nden geçirilir.
3. Lansmandan sonraki ilk PR mainnet özetini elle yazılmış bir sabit olarak ekler ve iki veri dosyasının değişmesini reddeden bir CI denetimi koyar ([LANSMAN.md](LANSMAN.md) §4.2).

**Sonradan CI ya da denetim ENGELLEYİCİ bir bulgu çıkarırsa** (kullanıcı kararı 2026-11-22, [LANSMAN.md](LANSMAN.md) §7 madde 6): genesis korunur ve bir düzeltme sürümü yayımlanır; yeniden lansman yapılmaz. Gerekçe: yayımlanmış genesis geri alınamaz ve yeniden lansman "adil lansman" iddiasıyla bağdaşmaz; lansman commit'i yalnızca veridir ve koruma bu verinin bağımsız betikle, iki makinede ve bütün testlerle doğrulandığını göstermiştir, yani bulgu büyük olasılıkla genesis'te değil koddadır. Tek istisna genesis'in kendisinin geçersiz olduğunun (yükleyicinin, betiğin ya da Go testinin onu reddettiğinin) sonradan anlaşılmasıdır; o durumda zincir yürümez ve yeniden lansman kaçınılmazdır, bu da duyurulur.
