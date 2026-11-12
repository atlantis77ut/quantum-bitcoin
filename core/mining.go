package core

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"quantum-bitcoin/crypto"
)

// Kazım zincir kilidi tutulmadan yapılır (CLAUDE.md madde 4): şablon okuma kilidiyle kurulur,
// nonce araması kilitsizdir, bulunan blok ağdan gelen bloklarla aynı doğrulama yolundan geçerek
// yazma kilidiyle eklenir. Böylece uzun bir kazım sırasında eşlerden gelen bloklar, API
// sorguları ve senkronizasyon beklemez.

var (
	// ErrStaleTemplate kazım sürerken zincirin ucu değişti: şablon artık ucun üzerine kurulu
	// değil, yeni şablonla yeniden kazılmalı.
	ErrStaleTemplate = errors.New("kazım şablonu eskidi: zincirin ucu değişti")
	// ErrMiningStopped kazım dışarıdan durduruldu.
	ErrMiningStopped = errors.New("kazım durduruldu")
)

// mineChunk zaman damgası tazelenmeden, durdurma ve uç değişimi denetlenmeden önce her
// iş parçacığının denediği nonce sayısıdır (~birkaç ms; testler küçültür).
const mineChunk = 1 << 16

// Template ucun üzerine kurulmuş, kazılmaya hazır bir bloktur.
type Template struct {
	Block  *Block
	parent string // şablonun kurulduğu uç
	mtp    int64  // ebeveynin medyan zamanı: zaman damgası bunun üzerinde kalmalı
}

// NewTemplate işlemleri ucun durumuna göre doğrular, geçersizleri ayıklar, madenci ödülünü
// ekler ve kazılacak bloğu kurar. Okuma kilidiyle çalışır.
func (bc *Blockchain) NewTemplate(transactions []Transaction, minerAddress string) (*Template, error) {
	if !crypto.IsValidAddress(minerAddress) {
		return nil, fmt.Errorf("%w: madenci adresi", ErrBadReceiver)
	}
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	tip := bc.tip
	height := tip.Height + 1
	subsidy := BlockSubsidy(height, bc.params)

	view := newStateView(bc)
	// Doğrulayıcıyla aynı sıra: olgunlaşan coinbase işlemlerden önce.
	if err := bc.creditMaturedLocked(view, height, tip.Hash); err != nil {
		return nil, err
	}
	var accepted []Transaction
	fees := int64(0)
	for i := range transactions {
		if len(accepted) == MaxTxPerBlock {
			bc.log.Warn("blok dolu, kalan işlemler bekletiliyor", "kalan", len(transactions)-i)
			break
		}
		tx := transactions[i]
		// Süresi dolmuş işlem atlanır: bloğa girerse kendi bloğumuz doğrulamada düşerdi.
		if err := tx.CheckExpiry(height); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		if err := tx.VerifyStateless(bc.params); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		// Ücret toplamı (ödül dahil) taşacaksa işlemi bloğa alma; aksi halde coinbase geçersiz olur.
		if subsidy+fees > math.MaxInt64-tx.Fee {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", "ücret toplamı taşar")
			continue
		}
		if err := view.apply(&tx); err != nil {
			bc.log.Warn("işlem reddedildi", "tx", tx.String(), "neden", err)
			continue
		}
		fees += tx.Fee
		accepted = append(accepted, tx)
	}

	coinbase := Transaction{
		Sender:     CoinbaseSender,
		Receiver:   minerAddress,
		Amount:     subsidy + fees,
		Nonce:      uint64(height), // yükseklik: her coinbase'in kimliği benzersiz olur
		IsCoinbase: true,
	}
	mtp, err := bc.mtpLocked(tip.Hash, nil)
	if err != nil {
		return nil, err
	}
	b := &Block{
		Version:      BlockVersion,
		Index:        height,
		Timestamp:    bc.minerTime(mtp, true),
		Transactions: append([]Transaction{coinbase}, accepted...),
		PrevHash:     tip.Hash,
		// Hedef yalnızca ebeveyne bağlıdır: zaman damgası tazelense de değişmez (asert.go).
		Bits: bc.params.nextBits(tip.Height, tip.Timestamp, tip.AnchorTime),
	}
	b.MerkleRoot = b.ComputeMerkleRoot()
	return &Template{Block: b, parent: tip.Hash, mtp: mtp}, nil
}

// minerTime kazılan bloğun zaman damgasıdır: yerel saat, ama ebeveynin medyan zamanının
// üzerinde. Saat FTL'den fazla geride kaldıysa kendi bloğumuz da ErrTimestampFuture ile düşer;
// nedeni görünür olsun. Aynı saniyede birkaç blok (regtest) olağandır; yalnızca belirgin geri
// kalma uyarılır.
func (bc *Blockchain) minerTime(mtp int64, warn bool) int64 {
	ts := bc.now()
	if ts <= mtp {
		if warn && mtp-ts >= bc.params.TargetSpacing {
			bc.log.Warn("yerel saat son blokların medyanının gerisinde; saatinizi NTP ile doğrulayın",
				"medyan", mtp, "yerel", ts)
		}
		ts = mtp + 1
	}
	return ts
}

// Mine şablonu zincir kilidi tutmadan, mineWorkerCount iş parçacığıyla kazar. Her turda
// parçacıklar ardışık mineChunk'lık nonce dilimlerini tarar; turlar arasında koordinatör
// parçacık durdurmayı ve uç değişimini denetler ve zaman damgasını yerel saate tazeler: uzun
// kazımda blok eskimiş damgayla (eşlerin medyanına göre geride) üretilmez. Bir turdaki tüm
// parçacıklar aynı damgayla kazar ve tur bitmeden dönülmez. stop nil olabilir.
func (bc *Blockchain) Mine(t *Template, stop <-chan struct{}) (*Block, error) {
	b := *t.Block // şablon değişmez; tekrar kazılabilir
	target, err := TargetFromBits(b.Bits)
	if err != nil {
		return nil, err
	}
	hdr, ok := b.headerBytes()
	if !ok {
		return nil, ErrBadHash
	}
	tgt := targetBytes(target)
	chunk := bc.mineChunk
	if chunk <= 0 {
		chunk = mineChunk
	}
	workers := bc.mineWorkerCount()
	round := uint64(chunk) * uint64(workers)
	for nonce, first := uint64(0), true; ; first = false {
		if bc.mineHook != nil {
			bc.mineHook()
		}
		if stop != nil {
			select {
			case <-stop:
				return nil, ErrMiningStopped
			default:
			}
		}
		if bc.TipHash() != t.parent {
			return nil, ErrStaleTemplate
		}
		if ts := bc.minerTime(t.mtp, false); !first && ts != b.Timestamp {
			b.Timestamp = ts
			binary.BigEndian.PutUint64(hdr[20:28], uint64(ts))
		}
		if box := bc.mineBackend.Load(); box != nil {
			be := box.b
			var n uint64
			var found bool
			var err error
			count := be.RoundSize()
			if count == 0 {
				err = errZeroRound
			} else {
				n, found, err = be.Scan(hdr, tgt, nonce, count)
			}
			if err == nil && !found {
				var more bool
				if nonce, more = nextRound(nonce, count); !more { // 2^64 nonce tükendi
					return nil, ErrStaleTemplate
				}
				continue
			}
			if err == nil {
				// Arka ucun sonucuna güvenilmez: aynı başlık CPU'da yeniden özetlenir.
				if h, ok := checkNonce(hdr, n, &tgt); ok {
					b.Nonce = n
					b.Hash = hex.EncodeToString(h[:])
					return &b, nil
				}
				err = fmt.Errorf("bulunan nonce %d CPU doğrulamasından geçmedi", n)
			}
			// Hatalı arka uç bir daha kullanılmaz; bu tur CPU ile taranır.
			bc.mineBackend.CompareAndSwap(box, nil)
			bc.log.Error("kazım arka ucu devre dışı bırakıldı; CPU ile devam ediliyor", "arka_uc", be.Name(), "err", err)
		}
		if n, h, ok := mineRound(hdr, nonce, uint64(chunk), workers, &tgt); ok {
			b.Nonce = n
			b.Hash = hex.EncodeToString(h[:])
			return &b, nil
		}
		var more bool
		if nonce, more = nextRound(nonce, round); !more { // 2^64 nonce tükendi
			return nil, ErrStaleTemplate
		}
	}
}

// SubmitBlock kazılan bloğu ağdan gelen bloklarla aynı doğrulamadan geçirip zincire ekler.
// Blok ucun yeni hali olmazsa (uç bu arada değiştiyse) ErrStaleTemplate döner.
func (bc *Blockchain) SubmitBlock(b *Block) error {
	if err := bc.checkStateless(b); err != nil {
		return fmt.Errorf("üretilen blok doğrulanamadı: %w", err)
	}
	bc.mu.Lock()
	defer bc.mu.Unlock()
	if bc.lastHash != b.PrevHash {
		return ErrStaleTemplate
	}
	if err := bc.storeAndActivateLocked(b); err != nil {
		return fmt.Errorf("üretilen blok doğrulanamadı: %w", err)
	}
	if bc.lastHash != b.Hash {
		return fmt.Errorf("üretilen blok zincire eklenmedi")
	}
	return nil
}
