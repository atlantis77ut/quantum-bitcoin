package core

import (
	"fmt"
	"math/big"
)

// Başlık ön-senkronizasyonu (CLAUDE.md mainnet engeli). Bir eşin başlık zinciri ilk
// oturum penceresinde (HeaderSync, MaxSyncHeaders) ucun işini ve en az zincir işini
// geçmiyorsa, başlıklar saklanmadan sabit bellekte doğrulanır ve işleri toplanır:
// yalnızca son başlık, birikmiş iş, medyan zaman ve zorluk için son birkaç ata ile her
// `every` başlıkta bir kontrol noktası özeti tutulur. Toplam iş eşiği geçince (Done)
// başlıklar aynı eşten parça parça yeniden indirilir (NewProvenSync): her parça yalnızca
// ön-senkronizasyonun kontrol noktalarıyla eşleştiği yere kadar yetkilendirilir. Özet
// başlığa, başlık ebeveyn özetine bağlı olduğundan eşleşen bir kontrol noktası tüm öneki
// bağlar: eş ön-senkronizasyonda A zincirini gösterip yeniden indirmede B'yi veremez.
//
// Böylece: (1) düşük işli başlık spam'i belleği şişiremez ve diske hiçbir şey yazdıramaz;
// (2) ucun işini ancak MaxSyncHeaders'tan derinde geçen bir dal da benimsenebilir (en çok
// iş kuralı, reorg derinliği sınırı yok kararı). Uzlaşma kuralı değildir.

// Presync bir eşle yürütülen ön-senkronizasyonun sabit boyutlu durumudur.
type Presync struct {
	bc    *Blockchain
	every int64 // kontrol noktası aralığı (yeniden indirme penceresinden küçük olmalı)

	last       string   // son doğrulanan başlık
	height     int64    // son başlığın yüksekliği
	work       *big.Int // son başlığın birikmiş işi
	anchorTime int64    // son başlığın ASERT çapa zamanı

	window      map[string]ancestor // medyan zaman ve zorluk için son atalar
	windowOrder []string

	checkpoints map[int64]string // yükseklik → özet (son başlık dahil)
	count       int64            // doğrulanan yeni başlık sayısı

	done        bool
	finalHeight int64
	finalHash   string
	finalWork   *big.Int
}

// presyncWindow tutulan ata sayısıdır: medyan zaman için ebeveyn + MedianTimeSpan−1 ata.
const presyncWindow = MedianTimeSpan + 1

// NewPresync window boyutlu parçalarla yeniden indirilecek bir ön-senkronizasyon başlatır.
// Kontrol noktası aralığı window/4'tür: her parça en az bir kontrol noktası içerir.
func (bc *Blockchain) NewPresync(window int) *Presync {
	every := int64(window / 4)
	if every < 1 {
		every = 1
	}
	return &Presync{bc: bc, every: every, window: map[string]ancestor{}, checkpoints: map[int64]string{}}
}

// Done toplam iş eşiği geçtiyse true döner; yeniden indirme başlayabilir.
func (p *Presync) Done() bool { return p.done }

// Last son doğrulanan başlığın özetidir (bir sonraki isteğin konum belirleyicisi için).
func (p *Presync) Last() string { return p.last }

// Count doğrulanan yeni (indekste olmayan) başlık sayısıdır.
func (p *Presync) Count() int64 { return p.count }

func (p *Presync) remember(hash string, a ancestor) {
	p.window[hash] = a
	p.windowOrder = append(p.windowOrder, hash)
	if len(p.windowOrder) > presyncWindow {
		delete(p.window, p.windowOrder[0])
		p.windowOrder = p.windowOrder[1:]
	}
}

// Add bir başlık yanıtını sabit bellekte doğrular ve eklenen başlık sayısını döndürür.
// Doğrulama HeaderSync.Add ile aynıdır (checkHeaderOnly). Eşik geçilince durur.
func (p *Presync) Add(hs []BlockHeader) (int, error) {
	if p.done {
		return 0, nil
	}
	if len(hs) == 0 || len(hs) > MaxHeadersPerMsg {
		return 0, fmt.Errorf("%w: %d başlık", ErrBadHeaders, len(hs))
	}
	bc := p.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if p.last != "" && hs[0].PrevHash != p.last {
		return 0, fmt.Errorf("%w: önceki yanıtın devamı değil", ErrBadHeaders)
	}
	lookup := func(h string) (ancestor, bool) {
		a, ok := p.window[h]
		return a, ok
	}
	for i := range hs {
		h := &hs[i]
		if i > 0 && h.PrevHash != hs[i-1].Hash {
			return i, fmt.Errorf("%w: kesintili dizi", ErrBadHeaders)
		}
		var parentHeight, parentAnchor int64
		var parentWork *big.Int
		if p.last != "" {
			parentHeight, parentWork, parentAnchor = p.height, p.work, p.anchorTime
		} else if e, ok := bc.index[h.PrevHash]; ok {
			if e.Status == statusInvalid {
				return i, ErrKnownInvalid
			}
			parentHeight, parentWork, parentAnchor = e.Height, e.work, e.AnchorTime
		} else {
			return i, ErrBadPrevHash
		}
		if e, ok := bc.index[h.Hash]; ok {
			if e.Status == statusInvalid {
				return i, ErrKnownInvalid
			}
			if e.Prev != h.PrevHash || e.Height != h.Index || e.Height != parentHeight+1 {
				return i, fmt.Errorf("%w: bilinen blokla tutarsız başlık", ErrBadHeaders)
			}
			p.advance(h.Hash, e.Height, e.Timestamp, e.Prev, e.AnchorTime, e.work)
		} else {
			if err := bc.checkHeaderOnly(h, parentHeight, lookup); err != nil {
				return i, err
			}
			w, err := blockWork(h.Bits)
			if err != nil {
				return i, err
			}
			p.advance(h.Hash, h.Index, h.Timestamp, h.PrevHash, childAnchorTime(h.Index, h.Timestamp, parentAnchor),
				new(big.Int).Add(parentWork, w))
			p.count++
		}
		if bc.enoughWorkLocked(p.work) {
			p.done = true
			p.finalHeight, p.finalHash, p.finalWork = p.height, p.last, p.work
			p.checkpoints[p.height] = p.last
			return i + 1, nil
		}
	}
	return len(hs), nil
}

func (p *Presync) advance(hash string, height, ts int64, prev string, anchorTime int64, work *big.Int) {
	p.last, p.height, p.work, p.anchorTime = hash, height, work, anchorTime
	p.remember(hash, ancestor{ts: ts, height: height, prev: prev, anchorTime: anchorTime})
	if height%p.every == 0 {
		p.checkpoints[height] = hash
	}
}

// NewProvenSync tamamlanmış bir ön-senkronizasyonun yeniden indirme oturumudur. Oturum
// penceresi ön-senkronizasyonunkiyle aynı olmalıdır; son başlığın ötesine geçmez.
func (bc *Blockchain) NewProvenSync(p *Presync, window int) *HeaderSync {
	s := bc.NewHeaderSync()
	s.limit = window
	s.proof = p
	return s
}

// checkProof yeniden indirilen başlığı ön-senkronizasyonun kontrol noktalarıyla karşılaştırır;
// eşleşen bir kontrol noktası (ya da son başlık) oturumdaki tüm öneki yetkilendirir.
func (s *HeaderSync) checkProof(h *BlockHeader) (matched bool, err error) {
	if s.proof == nil {
		return false, nil
	}
	if h.Index > s.proof.finalHeight {
		return false, ErrSyncLimit
	}
	cp, ok := s.proof.checkpoints[h.Index]
	if !ok {
		return false, nil
	}
	if cp != h.Hash {
		return false, fmt.Errorf("%w: ön-senkronizasyon kontrol noktası tutmuyor (yükseklik %d)", ErrBadHeaders, h.Index)
	}
	return true, nil
}

// AuthorizedTip yetkilendirilen son başlığın özetidir (yoksa boş): gövdeleri indirildikten
// sonra bir sonraki parça buradan devam eder.
func (s *HeaderSync) AuthorizedTip() string { return s.authorizedHash }

// ReachedFinal yeniden indirmenin ön-senkronizasyonun son başlığına ulaştığını söyler.
func (s *HeaderSync) ReachedFinal() bool { return s.reachedFinal }
