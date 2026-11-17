package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
)

// İşlem indeksi (şema 9): yalnızca ana zincirdeki işlemler için tx_<64 hex kimlik> → blok özeti
// ve blok içi sıra. Ana zinciri değiştiren tek yerler writeGenesis ve activateLocked'dır;
// indeks ikisinde de yükseklik tablosu ve uçla aynı atomik batch'te güncellenir: geri sarılan
// blokların anahtarları önce silinir, bağlanan blokların anahtarları sonra yazılır (aynı işlem
// iki dalda da varsa — ör. aynı madencinin aynı yükseklikteki coinbase'i — batch sırası
// gereği yeni dal kazanır). Ana zincirde iki farklı işlemin kimliği çakışamaz: coinbase'in
// nonce'u uzlaşma gereği yüksekliktir, diğer işlemler gönderen başına artan nonce taşır.

const prefixTx = "tx_"

// ErrUnknownTx işlem ana zincirde değil.
var ErrUnknownTx = errors.New("işlem ana zincirde değil")

type txLoc struct {
	Block string `json:"b"`
	Pos   int    `json:"p"`
}

func txKey(id string) []byte { return []byte(prefixTx + id) }

// txIndexPut indekse yazılacak tek bir anahtar/değerdir. Reorg sırasında bağlanan blokların
// gövdeleri yerine yalnızca bunlar biriktirilir (derin reorg'da bellek gövde boyutuyla büyümesin).
type txIndexPut struct {
	key, value []byte
}

// blockTxPuts b'nin işlemleri için işlem indeksi ve adres geçmişi kayıtlarını döndürür.
func blockTxPuts(b *Block) ([]txIndexPut, error) {
	out := make([]txIndexPut, 0, len(b.Transactions))
	for i := range b.Transactions {
		data, err := json.Marshal(txLoc{Block: b.Hash, Pos: i})
		if err != nil {
			return nil, err
		}
		out = append(out, txIndexPut{key: txKey(b.Transactions[i].ID()), value: data})
	}
	addrs, err := blockAddrPuts(b) // adres geçmişi (addrindex.go)
	if err != nil {
		return nil, err
	}
	return append(out, addrs...), nil
}

// putBlockTxs b'nin işlemlerini indekse ekler.
func putBlockTxs(batch *leveldb.Batch, b *Block) error {
	puts, err := blockTxPuts(b)
	if err != nil {
		return err
	}
	for _, p := range puts {
		batch.Put(p.key, p.value)
	}
	return nil
}

// İndeks geri alma kaydı (şema 13, budama hazırlığı): iundo_<özet> → bloğun bağlanırken yazdığı
// işlem indeksi ve adres geçmişi anahtarları (JSON dizi). Durum undo kaydıyla (undo_) aynı batch'te
// yazılır ve silinir; geri sarmada anahtarlar gövde okunmadan bu kayıttan silinir, böylece gövdesi
// budanmış blok da geri sarılabilir. Yalnızca yazılan anahtarlar kaydedilir (sıfır değerli
// coinbase'in hiç yazılmayan adres anahtarı yoktur; o yalnızca hiç geri sarılmayan genesis'tedir,
// writeGenesis kayıt yazmaz). Dolu blokta (250 işlem: bir tx_ ve iki atx_ anahtarı) ~56 KB,
// gövdenin (~1,8 MB) ~%3'üdür ve budamadan sonra da diskte kalır.
const prefixIndexUndo = "iundo_"

// indexUndoData bağlanan bloğun indeks anahtarlarının kaydıdır.
func indexUndoData(puts []txIndexPut) ([]byte, error) {
	keys := make([]string, len(puts))
	for i, p := range puts {
		keys[i] = string(p.key)
	}
	return json.Marshal(keys)
}

// loadIndexUndo bloğun indeks geri alma kaydını okur. Kayıt yoksa eksik veri hatasıdır
// (dataalarm.go); işlem indeksi ya da adres geçmişi dışında bir anahtar içeren kayıt bozuktur
// (bakiye gibi uzlaşma durumunu silemez).
func (bc *Blockchain) loadIndexUndo(hash string) ([]string, error) {
	data, err := bc.db.Get([]byte(prefixIndexUndo+hash), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, &missingDataError{hash: hash, kind: dataIndexUndo, cause: ErrCorruptState}
	}
	if err != nil {
		return nil, localIOErr("indeks geri alma kaydı okunamadı ("+hash+")", err)
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, fmt.Errorf("%w: indeks geri alma kaydı (%s): %v", ErrCorruptState, hash, err)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, prefixTx) && !strings.HasPrefix(k, prefixAddrTx) {
			return nil, fmt.Errorf("%w: indeks geri alma kaydında yabancı anahtar (%s)", ErrCorruptState, hash)
		}
	}
	return keys, nil
}

// TxByID ana zincirdeki işlemi bloğuyla, blok içi sırasıyla ve aynı anlık görüntüdeki onay
// sayısıyla (uç = 1) döndürür. id kanonik 64 küçük harf hex olmalıdır. Ana zincirde değilse
// ErrUnknownTx; indeks kaydı tutarsızsa ErrCorruptState döner. Blok önbellekten olabilir
// (salt okunur).
func (bc *Blockchain) TxByID(id string) (*Block, int, int64, error) {
	return bc.Reads(nil).TxByID(id)
}

// TxByID Blockchain.TxByID gibidir; blok gövdesi kaçırmada kapıdan geçer.
func (r BlockReads) TxByID(id string) (*Block, int, int64, error) {
	bc := r.bc
	if !IsHashHex(id) {
		return nil, 0, 0, fmt.Errorf("%w: geçersiz kimlik", ErrUnknownTx)
	}
	var loc txLoc
	b, conf, err := r.mainBlock(func() (*indexEntry, error) {
		data, err := bc.db.Get(txKey(id), nil)
		if errors.Is(err, leveldb.ErrNotFound) {
			return nil, ErrUnknownTx
		}
		if err != nil {
			return nil, fmt.Errorf("işlem indeksi okunamadı: %w", err)
		}
		if err := json.Unmarshal(data, &loc); err != nil || !IsHashHex(loc.Block) || loc.Pos < 0 {
			return nil, fmt.Errorf("%w: işlem indeksi kaydı %s", ErrCorruptState, id)
		}
		e, ok := bc.index[loc.Block]
		if !ok || !bc.isMain(e) {
			return nil, fmt.Errorf("%w: işlem %s ana zincirde olmayan %s bloğunu gösteriyor", ErrCorruptState, id, loc.Block)
		}
		return e, nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	// Konum denetimi kilit dışındadır: loc kilit altındaki anlık görüntüden, gövde değişmez.
	if loc.Pos >= len(b.Transactions) || b.Transactions[loc.Pos].ID() != id {
		return nil, 0, 0, fmt.Errorf("%w: işlem %s, %s bloğunun %d. sırasında değil", ErrCorruptState, id, loc.Block, loc.Pos)
	}
	return b, loc.Pos, conf, nil
}
