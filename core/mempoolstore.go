package core

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Havuzun kalıcı kaydı (düğüm politikası, uzlaşma durumu değil). Kayıt zincirin LevelDB'sinde,
// tek bir atomik batch ile tutulur: bir meta anahtarı ve Select sırasıyla işlem başına bir
// anahtar (her göndericinin nonce'ları bitişik kalır). Yüklemede her işlem kabul yolundan
// (Mempool.add) geçer: biçim, ücret, ufuk, nonce zinciri, bakiye, sınırlar ve imza yeniden
// denetlenir; yalnızca kabul marjı (MempoolExpiryMargin) atlanır. Şema sürümü değişmez: eski
// sürümler bu anahtarları okumaz; "mempool_" önekiyle çakışan başka önek ya da tarama yoktur.

const (
	keyMempoolMeta         = "mempool_meta"
	prefixMempoolTx        = "mempool_tx_" // mempool_tx_<8 hex sıra> → işlem JSON'u
	mempoolSnapshotVersion = 1
)

type mempoolMeta struct {
	Version int    `json:"version"`
	Genesis string `json:"genesis"`
	Tip     string `json:"tip"` // kaydedildiği andaki uç (bilgi içindir)
	Count   int    `json:"count"`
}

func mempoolTxKey(i int) []byte { return []byte(fmt.Sprintf("%s%08x", prefixMempoolTx, i)) }

// Save havuz son kayıttan beri değiştiyse onu atomik olarak diske yazar; eski kaydın bütün
// işlem anahtarları aynı batch'te silinir. Değişiklik yoksa yazmaz ve false döner. Havuz ve
// zincir kilitleri anlık görüntü alınırken tutulur, JSON kodlaması ve yazma
// kilitsizdir (kapanmış veritabanına yazma hata döner).
func (m *Mempool) Save() (bool, error) {
	m.saveMu.Lock() // kilit sırası: saveMu → Mempool.mu → Blockchain.mu
	defer m.saveMu.Unlock()
	bc := m.bc
	m.mu.Lock()
	bc.mu.RLock()
	err := m.syncLocked()
	gen := m.gen
	unchanged := gen == m.savedGen
	var ps []*poolTx
	var tip string
	if err == nil && !unchanged {
		ps, tip = m.orderedLocked(m.maxTxs), m.tip
	}
	bc.mu.RUnlock()
	m.mu.Unlock()
	if err != nil {
		return false, err
	}
	if unchanged {
		return false, nil
	}

	vals := make([][]byte, len(ps))
	for i, p := range ps {
		data, err := json.Marshal(&p.tx)
		if err != nil {
			return false, fmt.Errorf("havuz işlemi kodlanamadı: %w", err)
		}
		vals[i] = data
	}
	meta, err := json.Marshal(mempoolMeta{Version: mempoolSnapshotVersion, Genesis: bc.params.GenesisHash,
		Tip: tip, Count: len(ps)})
	if err != nil {
		return false, err
	}

	if err := m.writeSnapshot(vals, meta); err != nil {
		return false, err
	}
	m.mu.Lock() // zincir kilidi bırakıldı: kilit sırası Mempool.mu → Blockchain.mu korunur
	if gen > m.savedGen {
		m.savedGen = gen
	}
	m.mu.Unlock()
	return true, nil
}

// writeSnapshot eski kaydı silip yenisini tek batch'te yazar. Batch sırayla uygulanır: önce
// eski işlem anahtarları silinir, sonra yeni kayıt yazılır (aynı anahtara sonraki Put geçerlidir).
// Hiçbir kilit tutulmaz: büyük ve Sync'li yazma blok bağlamayı ve okuyucuları bekletmesin.
// Havuz anahtarlarını yalnızca saveMu altındaki Save yazar. Close'dan sonra ErrClosed döner, ama
// Close ile eşzamanlı çağrılmamalıdır (main kaydediciyi Close'dan önce durdurur).
func (m *Mempool) writeSnapshot(vals [][]byte, meta []byte) error {
	bc := m.bc
	batch := new(leveldb.Batch)
	it := bc.db.NewIterator(util.BytesPrefix([]byte(prefixMempoolTx)), nil)
	for it.Next() {
		batch.Delete(append([]byte(nil), it.Key()...))
	}
	it.Release()
	if err := it.Error(); err != nil {
		return fmt.Errorf("eski havuz kaydı okunamadı: %w", err)
	}
	for i, v := range vals {
		batch.Put(mempoolTxKey(i), v)
	}
	batch.Put([]byte(keyMempoolMeta), meta)
	if err := bc.db.Write(batch, &opt.WriteOptions{Sync: true}); err != nil {
		return fmt.Errorf("havuz kaydedilemedi: %w", err)
	}
	return nil
}

// Load kalıcı kaydı havuza geri yükler; havuz ağdan ya da API'den işlem almadan önce, bir kez
// çağrılmalıdır. Kayıt yoksa bir şey yapmaz. Sürüm ya da genesis tutmazsa kayıt bütünüyle
// atlanır; çözülemeyen ya da kabul yolundan geçmeyen (ör. kapalıyken onaylanmış, süresi dolmuş)
// işlemler tek tek atlanır. En çok havuz sınırı kadar kayıt okunur. Atlanan varsa bir sonraki
// Save eski kaydı yeniden yazar.
func (m *Mempool) Load() (restored, skipped int, err error) {
	bc := m.bc
	bc.mu.RLock()
	metaRaw, err := bc.db.Get([]byte(keyMempoolMeta), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		bc.mu.RUnlock()
		return 0, 0, nil
	}
	if err != nil {
		bc.mu.RUnlock()
		return 0, 0, fmt.Errorf("havuz kaydı okunamadı: %w", err)
	}
	var vals [][]byte
	it := bc.db.NewIterator(util.BytesPrefix([]byte(prefixMempoolTx)), nil)
	for it.Next() {
		if len(vals) >= m.maxTxs {
			skipped++
			continue
		}
		vals = append(vals, append([]byte(nil), it.Value()...))
	}
	it.Release()
	iterErr := it.Error()
	bc.mu.RUnlock()
	if iterErr != nil {
		return 0, 0, fmt.Errorf("havuz kaydı okunamadı: %w", iterErr)
	}

	defer func() {
		if skipped > 0 {
			m.mu.Lock()
			m.gen++ // eski kayıt bir sonraki Save'de yeniden yazılsın
			m.mu.Unlock()
		}
	}()
	var meta mempoolMeta
	if json.Unmarshal(metaRaw, &meta) != nil || meta.Version != mempoolSnapshotVersion ||
		meta.Genesis != bc.params.GenesisHash {
		bc.log.Warn("havuz kaydı tanınmadı; atlanıyor", "surum", meta.Version, "genesis", meta.Genesis)
		return 0, skipped + len(vals), nil
	}
	for _, v := range vals {
		var tx Transaction
		if err := json.Unmarshal(v, &tx); err != nil {
			skipped++
			continue
		}
		if _, err := m.add(tx, true); err != nil {
			skipped++
			continue
		}
		restored++
	}
	return restored, skipped, nil
}
