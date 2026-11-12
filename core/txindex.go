package core

import (
	"encoding/json"
	"errors"
	"fmt"

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

// blockTxPuts b'nin işlemleri için indeks kayıtlarını döndürür.
func blockTxPuts(b *Block) ([]txIndexPut, error) {
	out := make([]txIndexPut, 0, len(b.Transactions))
	for i := range b.Transactions {
		data, err := json.Marshal(txLoc{Block: b.Hash, Pos: i})
		if err != nil {
			return nil, err
		}
		out = append(out, txIndexPut{key: txKey(b.Transactions[i].ID()), value: data})
	}
	return out, nil
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

// deleteBlockTxs b'nin işlemlerini indeksten siler.
func deleteBlockTxs(batch *leveldb.Batch, b *Block) {
	for i := range b.Transactions {
		batch.Delete(txKey(b.Transactions[i].ID()))
	}
}

// TxByID ana zincirdeki işlemi bloğuyla, blok içi sırasıyla ve aynı anlık görüntüdeki onay
// sayısıyla (uç = 1) döndürür. id kanonik 64 küçük harf hex olmalıdır. Ana zincirde değilse
// ErrUnknownTx; indeks kaydı tutarsızsa ErrCorruptState döner. Blok önbellekten olabilir
// (salt okunur).
func (bc *Blockchain) TxByID(id string) (*Block, int, int64, error) {
	if !IsHashHex(id) {
		return nil, 0, 0, fmt.Errorf("%w: geçersiz kimlik", ErrUnknownTx)
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	data, err := bc.db.Get(txKey(id), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, 0, 0, ErrUnknownTx
	}
	if err != nil {
		return nil, 0, 0, fmt.Errorf("işlem indeksi okunamadı: %w", err)
	}
	var loc txLoc
	if err := json.Unmarshal(data, &loc); err != nil || !IsHashHex(loc.Block) || loc.Pos < 0 {
		return nil, 0, 0, fmt.Errorf("%w: işlem indeksi kaydı %s", ErrCorruptState, id)
	}
	e, ok := bc.index[loc.Block]
	if !ok || !bc.isMain(e) {
		return nil, 0, 0, fmt.Errorf("%w: işlem %s ana zincirde olmayan %s bloğunu gösteriyor", ErrCorruptState, id, loc.Block)
	}
	b, err := bc.cachedBlockLocked(e.Hash)
	if err != nil {
		return nil, 0, 0, err
	}
	if loc.Pos >= len(b.Transactions) || b.Transactions[loc.Pos].ID() != id {
		return nil, 0, 0, fmt.Errorf("%w: işlem %s, %s bloğunun %d. sırasında değil", ErrCorruptState, id, loc.Block, loc.Pos)
	}
	return b, loc.Pos, bc.tip.Height - e.Height + 1, nil
}
