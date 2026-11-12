package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/syndtr/goleveldb/leveldb/util"

	"quantum-bitcoin/crypto"
)

// Adres geçmişi indeksi (şema 10, düğüm özelliği, uzlaşma kuralı değil): yalnızca ana zincir için
// atx_<adres>_<16 hex yükseklik>_<8 hex blok içi sıra> → işlemin özeti. Her transfer iki kayıt
// üretir (gönderende "out", alıcıda "in"; kendine transfer uzlaşma gereği yoktur), coinbase
// yalnızca alıcıda "in" üretir; sıfır değerli coinbase (yalnızca genesis, premine yok) indekslenmez.
// Kayıtlar işlem indeksiyle aynı yerde ve aynı batch'te yazılır/silinir (blockTxPuts,
// deleteBlockTxs). Adresler uzlaşma gereği sabit uzunluklu küçük harf hex olduğundan bir adresin
// aralığı başka bir adresinkiyle çakışmaz; "atx_" hex olmayan 't' ve '_' içerdiğinden blok gövdesi
// anahtarlarıyla (çıplak 64 karakter hex özet) da çakışmaz.

const prefixAddrTx = "atx_"

// Adres geçmişi sayfa sınırları.
const (
	AddrHistoryDefaultLimit = 50
	AddrHistoryMaxLimit     = 200
)

// ErrBadCursor geçersiz sayfa imleci.
var ErrBadCursor = errors.New("geçersiz imleç")

// addrTxVal diskteki kayıttır; sayfa blok gövdesi okumadan sunulur.
type addrTxVal struct {
	ID           string `json:"id"`
	Block        string `json:"b"`
	Dir          string `json:"d"` // "in" | "out"
	Amount       int64  `json:"a"`
	Fee          int64  `json:"f,omitempty"` // yalnızca "out"
	Counterparty string `json:"c"`
	Coinbase     bool   `json:"cb,omitempty"`
}

// AddrTx adres geçmişindeki tek bir kayıttır.
type AddrTx struct {
	TxID          string
	BlockHash     string
	Height        int64
	Index         int
	Confirmations int64
	Direction     string // "in" | "out"
	Amount        int64
	Fee           int64 // "out" için ödenen ücret, "in" için 0
	Counterparty  string
	Coinbase      bool
}

// AddrHistory bir adres geçmişi sayfasıdır (yeniden eskiye).
type AddrHistory struct {
	TipHeight int64
	TipHash   string
	Txs       []AddrTx
	Next      string // sonraki sayfanın imleci; boşsa sayfa sonuncudur
}

func addrTxPrefix(addr string) string { return prefixAddrTx + addr + "_" }

func addrTxKey(addr string, height int64, pos int) []byte {
	return []byte(fmt.Sprintf("%s%016x_%08x", addrTxPrefix(addr), height, pos))
}

// FormatAddrCursor (yükseklik, sıra) imlecini "yükseklik:sıra" olarak yazar.
func FormatAddrCursor(height int64, pos int) string {
	return strconv.FormatInt(height, 10) + ":" + strconv.Itoa(pos)
}

// ParseAddrCursor "yükseklik:sıra" imlecini çözer; ikisi de negatif olmayan ondalık tamsayı ve
// sıra en çok MaxTxPerBlock olmalıdır (coinbase 0. sırada, MaxTxPerBlock onu saymaz).
func ParseAddrCursor(s string) (int64, int, error) {
	hs, ps, ok := strings.Cut(s, ":")
	if !ok || !isDecimal(hs) || !isDecimal(ps) || len(hs) > 18 || len(ps) > 9 {
		return 0, 0, ErrBadCursor
	}
	h, err1 := strconv.ParseInt(hs, 10, 64)
	p, err2 := strconv.Atoi(ps)
	if err1 != nil || err2 != nil || p > MaxTxPerBlock {
		return 0, 0, ErrBadCursor
	}
	return h, p, nil
}

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// blockAddrPuts b'nin işlemleri için adres geçmişi kayıtlarını döndürür. Adresler uzlaşmada
// denetlenir; yine de kanonik olmayan adres anahtar aralıklarını bozmasın diye reddedilir.
func blockAddrPuts(b *Block) ([]txIndexPut, error) {
	var out []txIndexPut
	put := func(addr string, pos int, v addrTxVal) error {
		if !crypto.IsValidAddress(addr) {
			return fmt.Errorf("%w: %s bloğunda kanonik olmayan adres", ErrCorruptState, b.Hash)
		}
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out = append(out, txIndexPut{key: addrTxKey(addr, b.Index, pos), value: data})
		return nil
	}
	for i := range b.Transactions {
		tx := &b.Transactions[i]
		id := tx.ID()
		if tx.IsCoinbase {
			if tx.Amount == 0 {
				continue
			}
			if err := put(tx.Receiver, i, addrTxVal{ID: id, Block: b.Hash, Dir: "in", Amount: tx.Amount,
				Counterparty: tx.Sender, Coinbase: true}); err != nil {
				return nil, err
			}
			continue
		}
		if err := put(tx.Sender, i, addrTxVal{ID: id, Block: b.Hash, Dir: "out", Amount: tx.Amount,
			Fee: tx.Fee, Counterparty: tx.Receiver}); err != nil {
			return nil, err
		}
		if err := put(tx.Receiver, i, addrTxVal{ID: id, Block: b.Hash, Dir: "in", Amount: tx.Amount,
			Counterparty: tx.Sender}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// deleteBlockAddrs b'nin adres geçmişi kayıtlarını siler (olmayan anahtarı silmek zararsızdır).
func deleteBlockAddrs(batch interface{ Delete([]byte) }, b *Block) {
	for i := range b.Transactions {
		tx := &b.Transactions[i]
		if !tx.IsCoinbase {
			batch.Delete(addrTxKey(tx.Sender, b.Index, i))
		}
		batch.Delete(addrTxKey(tx.Receiver, b.Index, i))
	}
}

// AddressHistory address'in ana zincirdeki işlemlerini yeniden eskiye, en çok limit kayıt
// döndürür. before boş değilse yalnızca o imleçten (hariç) eskiler gelir. Sayfa tek anlık
// görüntüdür; sayfalar arasında reorg olursa imlecin altındaki kayıtlar değişebilir (TipHash ile
// denetlenir). Tutarsız kayıt ErrCorruptState döner. Havuzdaki işlemler görünmez.
func (bc *Blockchain) AddressHistory(address, before string, limit int) (AddrHistory, error) {
	if !crypto.IsValidAddress(address) {
		return AddrHistory{}, fmt.Errorf("%w: adres", ErrBadCursor)
	}
	if limit < 1 || limit > AddrHistoryMaxLimit {
		return AddrHistory{}, fmt.Errorf("%w: limit %d", ErrBadCursor, limit)
	}
	pfx := addrTxPrefix(address)
	rng := util.BytesPrefix([]byte(pfx))
	if before != "" {
		h, p, err := ParseAddrCursor(before)
		if err != nil {
			return AddrHistory{}, err
		}
		rng.Limit = addrTxKey(address, h, p)
	}

	bc.mu.RLock()
	defer bc.mu.RUnlock()
	out := AddrHistory{TipHeight: bc.tip.Height, TipHash: bc.tip.Hash}
	it := bc.db.NewIterator(rng, nil)
	defer it.Release()
	for ok := it.Last(); ok; ok = it.Prev() {
		key := string(it.Key())
		if len(key) != len(pfx)+16+1+8 || key[len(pfx)+16] != '_' {
			return AddrHistory{}, fmt.Errorf("%w: adres geçmişi anahtarı %q", ErrCorruptState, key)
		}
		h, err1 := strconv.ParseUint(key[len(pfx):len(pfx)+16], 16, 63)
		p, err2 := strconv.ParseUint(key[len(pfx)+17:], 16, 31)
		if err1 != nil || err2 != nil {
			return AddrHistory{}, fmt.Errorf("%w: adres geçmişi anahtarı %q", ErrCorruptState, key)
		}
		height, pos := int64(h), int(p)
		if len(out.Txs) == limit {
			out.Next = FormatAddrCursor(out.Txs[limit-1].Height, out.Txs[limit-1].Index)
			break
		}
		var v addrTxVal
		if err := json.Unmarshal(it.Value(), &v); err != nil || !IsHashHex(v.ID) || !IsHashHex(v.Block) ||
			v.Amount <= 0 || v.Fee < 0 || (v.Dir != "in" && v.Dir != "out") ||
			(v.Dir == "in" && v.Fee != 0) || (v.Coinbase && v.Dir != "in") || pos > MaxTxPerBlock {
			return AddrHistory{}, fmt.Errorf("%w: adres geçmişi kaydı %q", ErrCorruptState, key)
		}
		if height > bc.tip.Height || bc.main[height] != v.Block {
			return AddrHistory{}, fmt.Errorf("%w: adres geçmişi kaydı %q ana zincirde olmayan %s bloğunu gösteriyor",
				ErrCorruptState, key, v.Block)
		}
		out.Txs = append(out.Txs, AddrTx{TxID: v.ID, BlockHash: v.Block, Height: height, Index: pos,
			Confirmations: bc.tip.Height - height + 1, Direction: v.Dir, Amount: v.Amount, Fee: v.Fee,
			Counterparty: v.Counterparty, Coinbase: v.Coinbase})
	}
	if err := it.Error(); err != nil {
		return AddrHistory{}, fmt.Errorf("adres geçmişi okunamadı: %w", err)
	}
	return out, nil
}
