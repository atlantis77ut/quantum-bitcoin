package network

import (
	"sync"

	"quantum-bitcoin/core"
)

// İşlem yayın taşması: yayın kuyruğu (txRelayQueueLen) dolunca yeni kabul edilen işlemlerin
// kimlikleri sınırlı bir FIFO'ya alınır ve yayın döngüsü bunları her turda (txRelayInterval),
// kuyruk boşken havuzdan okuyup yayar. Gönderim sırası korunur: taşma boş değilken gelen her
// işlem kuyrukta yer olsa bile taşmanın arkasına girer, döngü taşmayı ancak kuyruk ve bekleyen
// grup gönderildikten sonra boşaltır; böylece biz aynı gönderenin N+1 nonce'lu işlemini N'den
// önce göndermeyiz. Karşı düğüm her mesajı ayrı bağlantıda eşzamanlı işleyebildiğinden ardışık
// gruplar orada yer değiştirebilir (kuyruk yolunda da böyledir); nonce boşluğuyla düşen işlem
// havuz eşitlemesine kalır. Kuyruk grupları ve taşma boşaltması aynı gönderen kovasından harcar
// (relaybudget.go): karşı düğümün adres grubu başına hız sınırının altında kalınır. İşlemler
// gönderim anında havuzdan okunur (Mempool.Get): bu arada kazılan, değiştirilen, tahliye edilen
// ya da süresi dolan işlem atlanır; uca göre uzlaştırma yapılmadığı için eskimiş bir işlem
// gidebilir, karşı düğüm onu durum hatasıyla cezasız reddeder. Taşma
// dolarsa (core.MempoolMaxTxs) işlem atılır ve havuz eşitlemesine (en geç 10 dk) kalır; yeniden
// başlatmada taşma kaybolur.

// txOverflowMax taşmada tutulan en fazla işlem kimliğidir (havuzun alabileceği kadar).
const txOverflowMax = core.MempoolMaxTxs

// txOverflow yayın kuyruğuna sığmayan işlem kimliklerinin sıralı, tekrarsız ve sınırlı kuyruğudur.
type txOverflow struct {
	mu      sync.Mutex
	ids     []string
	set     map[string]struct{}
	dropped int64 // taşma doluyken atılan işlem sayısı
}

func newTxOverflow() *txOverflow { return &txOverflow{set: map[string]struct{}{}} }

// pushLocked id'yi sona ekler; zaten varsa true, taşma doluysa false döner.
func (o *txOverflow) pushLocked(id string) bool {
	if _, ok := o.set[id]; ok {
		return true
	}
	if len(o.ids) >= txOverflowMax {
		o.dropped++
		return false
	}
	o.ids = append(o.ids, id)
	o.set[id] = struct{}{}
	return true
}

// take baştan en çok n kimliği çıkarır.
func (o *txOverflow) take(n int) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if n > len(o.ids) {
		n = len(o.ids)
	}
	out := append([]string(nil), o.ids[:n]...)
	for _, id := range out {
		delete(o.set, id)
	}
	o.ids = o.ids[n:]
	if len(o.ids) == 0 {
		o.ids = nil // arka diziyi bırak
	}
	return out
}

func (o *txOverflow) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.ids)
}

// relayOverflow taşmadan en çok budget işlemi maxTxsPerMsg'lik gruplarla yayar ve yayılan
// işlem sayısını döndürür. Havuzda artık olmayan kimlikler atlanır (bütçeden düşer).
func (ps *P2PServer) relayOverflow(budget int, stop <-chan struct{}) int {
	sent := 0
	for budget > 0 {
		select {
		case <-stop:
			return sent
		default:
		}
		n := maxTxsPerMsg
		if n > budget {
			n = budget
		}
		ids := ps.txOverflow.take(n)
		if len(ids) == 0 {
			return sent
		}
		budget -= len(ids)
		if ps.Mempool == nil {
			continue
		}
		txs := ps.Mempool.Get(ids)
		if len(txs) == 0 {
			continue
		}
		ps.broadcastTxs(txs)
		sent += len(txs)
	}
	return sent
}
