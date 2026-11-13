package network

import (
	"encoding/hex"
	"errors"
	"sync"

	"quantum-bitcoin/core"
)

// Ret önbelleği (düğüm politikası, uzlaşma kuralı değil): eşlerden gelen (yayın ve havuz
// eşitlemesi) işlemlerden, aynı kimlikle bir daha geçerli olamayacak biçimde reddedilenler
// hatırlanır; havuz eşitlemesi onları yeniden istemez, yayında Mempool.Add'e (ve imza
// doğrulamasına) gitmez. İşlem kimliği gövdenin bütün alanlarını ve imzayı kapsadığından
// (core.Transaction.IDBytes) aynı kimlik bayt bayt aynı işlemdir: bir saldırgan dürüst işlemin
// bir alanını bozup önce gönderirse bozuk kopya kendi kimliğiyle önbelleğe girer, dürüst işlem
// etkilenmez. Yalnızca havuz içeriğinden bağımsız retler önbelleğe alınır:
//   - rejectInvalid: durumdan bağımsız geçersiz (invalidTx: imza, gönderen, biçim); kalıcı.
//     Yayında yeniden gelirse gönderen grup eskisi gibi cezalanır.
//   - rejectPolicy: ücret havuz alt sınırının altında (sabit politika); kalıcı.
//   - rejectTip: uç ilerledikçe geçerli olamayacak retler: süresine çok az kalmış ya da nonce'u
//     zaten onaylanmış işlem. Retin dayandığı uç kaydedilir; o uç ana zincirde kaldıkça (her
//     yeni uç onun torunu) ret geçerlidir, reorg o ucu düşürürse kayıt yok sayılır.
// Nonce boşluğu, dolu havuz, yer değiştirme, gönderen sınırı, yetersiz bakiye (gelen ödeme ya da
// olgunlaşan coinbase ile değişir), fazla ileri son kullanma ve iç hatalar önbelleğe alınmaz.
// Önbellek bellektedir, sınırlıdır (en eski kayıt düşer) ve yeniden başlatmada kaybolur. Yerel
// cüzdanın POST /tx yolu önbelleği kullanmaz (kesin hata alır).

type rejectClass uint8

const (
	rejectNone rejectClass = iota
	rejectInvalid
	rejectPolicy
	rejectTip
)

// rejectCacheMax önbellekteki en fazla kimliktir (~2 havuz dolusu).
const rejectCacheMax = 2 * core.MempoolMaxTxs

// classifyReject Mempool.Add hatasının önbellek sınıfıdır. rejectTip için çağıran ayrıca
// koşulları (nonce gerçekten onaylı, uç değişmedi) denetler.
func classifyReject(err error) rejectClass {
	switch {
	case err == nil:
		return rejectNone
	case invalidTx(err):
		return rejectInvalid
	case errors.Is(err, core.ErrFeeTooLow):
		return rejectPolicy
	case errors.Is(err, core.ErrExpiryTooSoon), errors.Is(err, core.ErrBadNonce):
		return rejectTip
	default:
		return rejectNone
	}
}

type rejectEntry struct {
	class rejectClass
	tip   string // rejectTip için retin dayandığı uç
}

// rejectCache kimliğe göre ret kayıtlarının sınırlı (FIFO) önbelleğidir; kendi kilidi vardır ve
// kilit altında zincir ya da havuz çağrısı yapılmaz.
type rejectCache struct {
	mu   sync.Mutex
	m    map[[32]byte]rejectEntry
	ring [][32]byte
	next int
	max  int
}

func newRejectCache(max int) *rejectCache {
	return &rejectCache{m: map[[32]byte]rejectEntry{}, max: max}
}

func (c *rejectCache) add(id [32]byte, e rejectEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[id]; ok {
		c.m[id] = e
		return
	}
	if len(c.ring) < c.max {
		c.ring = append(c.ring, id)
	} else {
		delete(c.m, c.ring[c.next])
		c.ring[c.next] = id
		c.next = (c.next + 1) % c.max
	}
	c.m[id] = e
}

func (c *rejectCache) get(id [32]byte) (rejectEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	return e, ok
}

func (c *rejectCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

// check id'nin geçerli ret sınıfını döndürür; rejectTip kaydı, dayandığı uç artık ana zincirde
// değilse yok sayılır. onMain kilit dışında çağrılır.
func (c *rejectCache) check(id [32]byte, onMain func(string) bool) rejectClass {
	e, ok := c.get(id)
	if !ok {
		return rejectNone
	}
	if e.class == rejectTip && !onMain(e.tip) {
		return rejectNone
	}
	return e.class
}

// onMainFn bloğun ana zincirde olup olmadığını söyleyen, çağrı başına sonuçları hatırlayan bir
// işlevdir (havuz eşitlemesinde binlerce kimlik aynı birkaç ucu paylaşır).
func (ps *P2PServer) onMainFn() func(string) bool {
	memo := map[string]bool{}
	return func(hash string) bool {
		if v, ok := memo[hash]; ok {
			return v
		}
		n, _ := ps.Blockchain.Confirmations(hash)
		memo[hash] = n > 0
		return n > 0
	}
}

// addPeerTx eşten gelen işlemi havuza ekler ve önbelleğe alınabilir retleri kaydeder.
func (ps *P2PServer) addPeerTx(tx core.Transaction) error {
	before := ps.Blockchain.TipHash()
	_, err := ps.Mempool.Add(tx)
	cls := classifyReject(err)
	if cls == rejectNone {
		return err
	}
	e := rejectEntry{class: cls}
	if cls == rejectTip {
		if errors.Is(err, core.ErrBadNonce) {
			// ErrBadNonce yalnızca nonce onaylıysa uçla tekdüzedir (Add'in başka yolları da bu
			// hatayı verebilir).
			n, nerr := ps.Blockchain.GetNonce(tx.Sender)
			if nerr != nil || tx.Nonce > n {
				return err
			}
		}
		// Uç Add sırasında değiştiyse ret hangi uca dayandığı bilinmez; zincir seçimi yalnızca
		// daha çok işe gittiğinden uç bırakılan bir özete dönemez: iki okuma eşitse Add o ucu gördü.
		if ps.Blockchain.TipHash() != before {
			return err
		}
		e.tip = before
	}
	ps.rejects.add(tx.IDBytes(), e)
	return err
}

// filterRejected önbellekte geçerli retti olan kimlikleri çıkarır (kimlikler kanonik hex).
func (ps *P2PServer) filterRejected(ids []string) []string {
	onMain := ps.onMainFn()
	out := ids[:0:0]
	for _, s := range ids {
		var id [32]byte
		if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
			copy(id[:], b)
			if ps.rejects.check(id, onMain) != rejectNone {
				continue
			}
		}
		out = append(out, s)
	}
	return out
}
