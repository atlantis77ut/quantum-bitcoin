package network

import (
	"errors"
	"sync"
	"time"

	"quantum-bitcoin/core"
)

// Yetim işlem tamponu (düğüm politikası, uzlaşma kuralı değil): her mesaj ayrı bir bağlantıyla
// gelip ayrı goroutine'de işlendiğinden aynı gönderenin ardışık grupları burada yer
// değiştirebilir (ya da öncül başka bir eşten geç gelir); havuz, öncülü olmayan işlemi
// ErrNonceGap ile geri çevirir. Böyle işlemler bir süre (orphanTTL) tamponda tutulur ve öncülleri
// havuza girince (aynı handleTxs çağrısında zincirleme, ya da öncül bloktan, POST /tx'ten veya
// havuz eşitlemesinden geldiyse süpürmede) yeniden denenir ve yayılır; böylece boşluk sonraki
// atlamaya taşınmaz.
//
// Add boşluğu imzadan önce bulduğundan tampona girecek işlem önce Mempool.CheckOrphan'dan geçer:
// ucuz süzgeçler (boşluk gönderen sınırının içinde, son kullanma ufku, uç+1'de bakiye tutar +
// ücreti karşılıyor, kimlik havuzda ya da tamponda değil, gönderenin yuvaları dolu değil) ve en
// son ML-DSA imzası. Böylece sahte imzalı kopyalar dürüst göndericinin yuvalarını dolduramaz ve
// fonsuz anahtarlar imza doğrulaması harcatamaz. İmzası ya da biçimi geçersiz işlem gönderen grubu
// eskisi gibi cezalandırır; diğer süzgeç retleri cezasızdır. Tampon bellektedir, sınırlıdır
// (toplamda orphanMax, gönderen başına orphanPerSender; dolunca en eski düşer) ve yeniden
// başlatmada kaybolur: düşen yetimler havuz eşitlemesine kalır.

const (
	orphanMax       = 500
	orphanPerSender = core.MempoolMaxPerSender
	orphanTTL       = 2 * time.Minute
	orphanSweep     = 10 * time.Second
)

type orphanEntry struct {
	tx    core.Transaction
	added time.Time
	seq   uint64 // order'daki canlı kaydı tanır (çıkarılıp yeniden eklenen kimlik)
}

type orphanRef struct {
	id  [32]byte
	seq uint64
}

// orphanPool kimliğe göre yetim işlemlerin sınırlı tamponudur; kendi kilidi vardır ve kilit
// altında zincir ya da havuz çağrısı yapılmaz.
type orphanPool struct {
	mu       sync.Mutex
	m        map[[32]byte]*orphanEntry
	bySender map[string]map[[32]byte]struct{}
	order    []orphanRef // ekleme sırası; silinmiş kayıtlar tembelce atlanır
	seq      uint64
}

func newOrphanPool() *orphanPool {
	return &orphanPool{m: map[[32]byte]*orphanEntry{}, bySender: map[string]map[[32]byte]struct{}{}}
}

func (o *orphanPool) len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.m)
}

// has id tamponda mı (CheckOrphan'dan önce ucuz süzgeç).
func (o *orphanPool) has(id [32]byte) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.m[id]
	return ok
}

// senderFull gönderenin yuvaları dolu mu (CheckOrphan'dan önce ucuz süzgeç).
func (o *orphanPool) senderFull(sender string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.bySender[sender]) >= orphanPerSender
}

// add işlemi tampona alır; zaten varsa ya da gönderenin yuvaları doluysa false. Tampon doluysa
// en eski kayıt düşer.
func (o *orphanPool) add(tx core.Transaction, now time.Time) bool {
	id := tx.IDBytes()
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.m[id]; ok || len(o.bySender[tx.Sender]) >= orphanPerSender {
		return false
	}
	for len(o.m) >= orphanMax {
		if o.liveLocked(o.order[0]) {
			o.removeLocked(o.order[0].id)
		}
		o.order = o.order[1:]
	}
	o.seq++
	o.m[id] = &orphanEntry{tx: tx, added: now, seq: o.seq}
	s := o.bySender[tx.Sender]
	if s == nil {
		s = map[[32]byte]struct{}{}
		o.bySender[tx.Sender] = s
	}
	s[id] = struct{}{}
	o.order = append(o.order, orphanRef{id: id, seq: o.seq})
	o.compactLocked()
	return true
}

func (o *orphanPool) removeLocked(id [32]byte) {
	e, ok := o.m[id]
	if !ok {
		return
	}
	delete(o.m, id)
	if s := o.bySender[e.tx.Sender]; s != nil {
		delete(s, id)
		if len(s) == 0 {
			delete(o.bySender, e.tx.Sender)
		}
	}
}

// liveLocked r'nin kimliğin şu anki kaydı olup olmadığını söyler.
func (o *orphanPool) liveLocked(r orphanRef) bool {
	e, ok := o.m[r.id]
	return ok && e.seq == r.seq
}

// compactLocked sıradan silinmiş kayıtları atar (sıra en çok iki katına kadar şişer).
func (o *orphanPool) compactLocked() {
	for len(o.order) > 0 && !o.liveLocked(o.order[0]) {
		o.order = o.order[1:]
	}
	if len(o.order) <= 2*len(o.m)+orphanPerSender {
		return
	}
	out := make([]orphanRef, 0, len(o.m))
	for _, r := range o.order {
		if o.liveLocked(r) {
			out = append(out, r)
		}
	}
	o.order = out
}

// take gönderenin nonce'u verilen değerden küçük ya da eşit kayıtlarını tampondan çıkarır ve
// nonce'u tam eşit olanları döndürür (küçükler artık yetim olamaz: yuvaları dolu).
func (o *orphanPool) take(sender string, nonce uint64) []core.Transaction {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []core.Transaction
	for id := range o.bySender[sender] {
		e := o.m[id]
		if e.tx.Nonce > nonce {
			continue
		}
		if e.tx.Nonce == nonce {
			out = append(out, e.tx)
		}
		o.removeLocked(id)
	}
	o.compactLocked()
	return out
}

// expire orphanTTL'den eski kayıtları düşürür ve kalan göndericileri döndürür.
func (o *orphanPool) expire(now time.Time) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	for id, e := range o.m {
		if now.Sub(e.added) >= orphanTTL {
			o.removeLocked(id)
		}
	}
	o.compactLocked()
	out := make([]string, 0, len(o.bySender))
	for s := range o.bySender {
		out = append(out, s)
	}
	return out
}

// bufferOrphan ErrNonceGap ile geri çevrilen eş işlemini süzgeçlerden geçirip tampona alır. İmzası
// ya da biçimi geçersizse false döner (çağıran grubu cezalandırır); öncüller bu arada geldiyse
// işlemi yeniden dener.
func (ps *P2PServer) bufferOrphan(tx core.Transaction, now time.Time) bool {
	id := tx.IDBytes()
	if ps.orphans.has(id) || ps.orphans.senderFull(tx.Sender) {
		return true
	}
	err := ps.Mempool.CheckOrphan(tx)
	switch {
	case err == nil:
		ps.orphans.add(tx, now)
	case errors.Is(err, core.ErrNotOrphan):
		// İmza henüz doğrulanmadı: Add doğrular, geçersizse grup cezalanır.
		err = ps.addPeerTx(tx)
		if err == nil {
			ps.RelayTx(tx)
			ps.admitOrphans(tx.Sender, tx.Nonce+1)
		}
		return !invalidTx(err)
	case invalidTx(err):
		ps.rejects.add(id, rejectEntry{class: rejectInvalid})
		return false
	default:
		return true
	}
	// Tampona girerken öncül başka bir goroutine'de kabul edilmiş olabilir (o goroutine'in
	// zincirlemesi bu kaydı görmeden bitmiş olabilir).
	if p, err := ps.Mempool.Policy(tx.Sender); err == nil {
		ps.admitOrphans(tx.Sender, p.NextNonce)
	}
	return true
}

// admitOrphans gönderenin nonce'lu yetimlerini sırayla havuza almayı dener ve kabul edilenleri
// yayar (yetim imzası Add'de yeniden doğrulanır).
func (ps *P2PServer) admitOrphans(sender string, nonce uint64) {
	for {
		accepted := false
		for _, tx := range ps.orphans.take(sender, nonce) {
			if !accepted && ps.addPeerTx(tx) == nil {
				ps.RelayTx(tx)
				accepted = true
			}
		}
		if !accepted {
			return
		}
		nonce++
	}
}

// sweepOrphans süresi dolan yetimleri düşürür ve öncülü başka yoldan (blok, POST /tx, havuz
// eşitlemesi) gelmiş olanları kabul etmeyi dener.
func (ps *P2PServer) sweepOrphans(now time.Time) {
	senders := ps.orphans.expire(now)
	if ps.Blockchain.BelowMinChainWork() {
		return
	}
	for _, s := range senders {
		if p, err := ps.Mempool.Policy(s); err == nil {
			ps.admitOrphans(s, p.NextNonce)
		}
	}
}

func (ps *P2PServer) orphanLoop(stop <-chan struct{}) {
	t := time.NewTicker(orphanSweep)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			ps.sweepOrphans(time.Now())
		}
	}
}
