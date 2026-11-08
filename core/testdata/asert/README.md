# aserti3-2d test vektörleri

Kaynak: Bitcoin Cash Node QA varlıkları, `test_vectors/aserti3-2d` (run01–run11; run12 büyüklüğü nedeniyle alınmadı).
İndirildiği yer: https://download.bitcoincashnode.org/misc/data/asert/test_vectors/ (2026-11-08).
Belirtim: https://upgradespecs.bitcoincashnode.org/2020-11-15-asert

Vektörler BCH parametreleriyle üretilmiştir: blok süresi 600 sn, yarı ömür 172800 sn, en büyük hedef `0x1d00ffff`.
`core/asert_test.go` bunları `asertTarget` ile, Q-BTC parametrelerinden bağımsız olarak sınar.
Her satır: yineleme, değerlendirilen (ebeveyn) bloğun yüksekliği ve zamanı, ardından o bloğun çocuğunun hedefi.
