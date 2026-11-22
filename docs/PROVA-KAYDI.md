# Lansman prova kaydı (şablon)

Bu dosya [LANSMAN-KILAVUZU.md](LANSMAN-KILAVUZU.md) §5'teki tam provanın kayıt şablonudur. Her prova için kopyalanıp doldurulur (ör. `docs/prova-kayitlari/2026-12-01.md`); şablonun kendisi boş kalır. Prova bittiğinde kayıt, lansman kararının dayanağıdır: ölçülen süreler §4'teki sınırlara sığmıyorsa lansman tarihi verilmez.

Doldurulan kayda gerçek anahtar parmak izi, makine adı, IP ya da kullanıcı adı yazılmaz; makineler "lansman makinesi" ve "ikinci makine" diye anılır.

## 1. Genel

| Alan | Değer |
|---|---|
| Prova tarihi (UTC) | |
| Prova numarası (ilk, ikinci, ...) | |
| Taban commit (prova öncesi) | |
| Prova taban etiketi / lansman etiketi | `prova-oncesi` / `v-prova` |
| Prova anahtarının parmak izi (atılacak anahtar) | |
| Deneme deposu (özel; adresi yazılmaz) | |
| `qbtc` commit'i ve Go sürümü | |
| liboqs sürümü (iki makinede de) | |
| GPU modeli ve sürücü sürümü | |
| Ölçüm raporu (SHA-256) | |

## 2. Ön koşullar (işaretle)

- [ ] İki makinede de deponun **ayrı bir klonu**; çalışma ağacı temiz; durum dizinleri depo dışında.
- [ ] Prova anahtarı belirteçte; `allowedSignersFile` yalnızca prova anahtarını içeriyor; gerçek anahtar makinede yok.
- [ ] `ssh` ile ikinci makineye parolasız erişim; ana makine anahtarları iki yönde `known_hosts`'ta.
- [ ] Saat eşitlemesi iki makinede de açık (`timedatectl show -p NTPSynchronized`), sapma < 1 sn.
- [ ] Betikteki ve araçtaki Bitcoin zorluk tabanı o günün zorluğuna göre yenilendi ([LANSMAN.md](LANSMAN.md) §4.3).
- [ ] Gerçek başlangıç hedefi ve tabanı (`MainnetParams`) kullanılıyor.
- [ ] Deneme deposunda gerçek depodakiyle aynı dal korumaları var.

## 3. Adımlar ve süreler

Her satır için başlangıç saati, süre (sn), çıkış durumu ve not yazılır. Süreler betiğin yazdırdığı değerlerdir; elle ölçülenler "elle" diye işaretlenir.

| # | Adım | Komut | Süre (sn) | Sonuç | Not |
|---|---|---|---|---|---|
| 1 | İkinci makinede hazırlık (`hazir`) | `lansman.py hazir` | | | |
| 2 | Ön-uçuş (`on-ucus`; ikinci makinede `hazir` dahil) | `lansman.py on-ucus` | | | |
| 3 | Başlıklar (`basliklar`) | `lansman.py basliklar` | | | |
| 4 | **Kazım** (aracın genesis'i bulma süresi) | `lansman.py kaz` | | | |
| 5 | `kaz`'ın toplamı (araç + vektör + lansman testleri + commit + koruma) | | | | |
| 6 | **İkinci makinede bütün testler** (`DOGRULANDI` satırındaki `sure`) | `lansman.py dogrula` | | | |
| 7 | `yayimla`'nın toplamı (aktarım + doğrulama + imza + itme) | `lansman.py yayimla` | | | |
| 8 | **T_g'den itmenin dönmesine kadar** (yayın anı − T_g) | | | | sınır 570 sn (600 sn eşik, 30 sn pay) |
| 9 | Kayıt (`kayit`) | `lansman.py kayit` | | | |

Ölçülecek diğer değerler:

| Değer | Sonuç | Sınır |
|---|---|---|
| Yayın anı − T_g (sn) | | ≤ 570 sn rahatça altında; ≤ 600 sn eşik |
| Yayın anı − t_b (sn) | | bilgi |
| Yayın anı − bloğun ilk görülme zamanı (sn) | | bilgi (gizli kazım penceresinin gerçek ölçüsü) |
| T_g − t_b (sn) | | ≤ 3600 sn |
| İki makinenin T_g'ye uzaklık farkı (sn) | | ≤ 10 sn |
| Saat sapması, iki makine (sn) | | < 1 sn |
| Ön-uçuş hashrate ölçümü / rapor ortancası | | ±%10 |
| Aracın tur boyu | | ≤ 1 sn |
| Denenen nonce sayısı ve başlık sayısı | | bilgi |

Toplam süre 570 sn'nin rahatça altında değilse (ör. ikinci makinedeki tam test paketi uzun sürüyorsa) ikinci makine hızlandırılır; eşik gevşetilmez, tam test paketi kısaltılmaz. Provada eşik zorlanıyorsa nedeni (hangi adım) yazılır.

## 4. Kasıtlı atma

Provada en az bir deneme kasıtlı olarak `at` ile atılır ve kayda girdiği görülür.

| Alan | Değer |
|---|---|
| Atılan denemenin numarası ve nedeni | |
| `at` sonrası ağaç tabana döndü mü (`git status`, `HEAD`) | |
| Atılan deneme `lansman-kaydi.json`'da `discarded_attempts` içinde mi | |
| Atılan denemenin genesis'i ve aracın kaydı kayıtta mı | |

## 5. Kesinti ve hata provası

İsteğe bağlı ama önerilir; her biri için sonuç yazılır.

- [ ] `yayimla` imza adımında Ctrl-C: yerel etiket silindi, hiçbir şey itilmedi.
- [ ] İtmeden hemen sonra süreç öldürüldü: yeniden çalıştırılan `yayimla` yalnızca sonucu okudu, yeniden doğrulama ve imza yapmadı; `at` reddetti.
- [ ] İkinci makine erişilemez: `yayimla` imza atmadan düştü.
- [ ] İkinci makinede bir test kasıtlı kırıldı: imza atılmadı.
- [ ] Bitcoin kaynaklarından biri kapatıldı (ör. `/etc/hosts`): `kaz` ve `dogrula` düştü.
- [ ] Uzak `main` taban commit'ten ilerletildi: `yayimla` itmeden düştü.

## 6. Kayıt ve temizlik

- [ ] İmzalı kayıt etiketi (`v-prova-kayit`) deneme deposunda ve prova anahtarıyla doğrulandı.
- [ ] Kayıttaki alanlar (genesis özeti, commit'ler, T_g, t_b, ilk görülme zamanları, yayın anı, ikinci makine sonucu, aracın kaydı, atılan denemeler) beklenenle karşılaştırıldı.
- [ ] Prova durum dizinleri, veri dizinleri (`qbtc_*_db`), klonlar ve deneme deposu silindi (kalan bir prova zinciri lansman günü düğümün açılmasını engeller).
- [ ] Prova anahtarı imha edildi; `allowedSignersFile` gerçek anahtarla yeniden kuruldu.
- [ ] Gerçek anahtarla imzalı geçerli ikinci bir "mainnet" etiketi **üretilmedi**.

## 7. Sonuç

| Alan | Değer |
|---|---|
| Bütün adımlar geçti mi | |
| En yavaş adım ve süresi | |
| Eşik payı (570 − ölçülen yayın anı − T_g, sn) | |
| Bulunan sorunlar (her biri için issue ya da PR) | |
| Lansmana engel var mı | |
| Provayı yapan | |
