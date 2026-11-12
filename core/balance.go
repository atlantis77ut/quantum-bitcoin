package core

import (
	"fmt"
	"math"
)

// Bakiye ayrıntısı (salt okunur, uzlaşma kuralı değil): onaylı bakiyenin yanında olgunlaşmamış
// coinbase'ler ve havuzdaki bekleyen tutarlar. H yüksekliğindeki bloğun coinbase'i (ödül +
// ücretler) H+CoinbaseMaturity bloğu bağlanırken bakiyeye geçer (creditMaturedLocked); uç T iken
// olgunlaşmamışlar ana zincirin max(1, T−M+1)..T yükseklikleridir.

// ImmatureCoinbase henüz bakiyeye geçmemiş bir coinbase ödemesidir.
type ImmatureCoinbase struct {
	Height    int64  // coinbase'i taşıyan bloğun yüksekliği
	BlockHash string // ana zincirdeki blok
	Amount    int64  // ödül + ücretler
	MaturesAt int64  // bu yükseklikteki blok bağlanınca onaylı bakiyeye geçer
}

// BalanceInfo bir adresin aynı anlık görüntüdeki onaylı bakiyesi ve olgunlaşmamış coinbase'leridir.
type BalanceInfo struct {
	TipHeight         int64
	TipHash           string
	Balance           int64              // onaylı, harcanabilir (GetBalance ile aynı)
	Immature          int64              // ImmatureCoinbases toplamı
	ImmatureCoinbases []ImmatureCoinbase // yükseklik sırasıyla
}

// Balance address'in onaylı bakiyesini ve olgunlaşmamış coinbase'lerini döndürür.
func (bc *Blockchain) Balance(address string) (BalanceInfo, error) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.balanceLocked(address)
}

func (bc *Blockchain) balanceLocked(address string) (BalanceInfo, error) {
	bal, err := bc.readInt(prefixBalance + address)
	if err != nil {
		return BalanceInfo{}, err
	}
	tip := bc.tip.Height
	info := BalanceInfo{TipHeight: tip, TipHash: bc.tip.Hash, Balance: bal}
	start := tip - bc.params.CoinbaseMaturity + 1
	if start < 1 {
		start = 1 // genesis hiçbir şey alacaklandırmaz
	}
	for h := start; h <= tip; h++ {
		e, ok := bc.index[bc.main[h]]
		// creditMaturedLocked ile aynı değişmez: ana zincirdeki her bloğun coinbase alıcısı bilinir.
		if !ok || e.Height != h || e.CbTo == "" || e.CbValue < 0 {
			return BalanceInfo{}, fmt.Errorf("%w: %d yüksekliğinde coinbase kaydı yok", ErrCorruptState, h)
		}
		if e.CbTo != address || e.CbValue == 0 {
			continue
		}
		if info.Immature > math.MaxInt64-e.CbValue {
			return BalanceInfo{}, fmt.Errorf("%w: olgunlaşmamış toplam", ErrOverflow)
		}
		info.Immature += e.CbValue
		info.ImmatureCoinbases = append(info.ImmatureCoinbases, ImmatureCoinbase{
			Height: h, BlockHash: e.Hash, Amount: e.CbValue, MaturesAt: h + bc.params.CoinbaseMaturity})
	}
	return info, nil
}

// PoolBalance bakiye ayrıntısına havuzdaki bekleyen tutarları ekler (aynı anlık görüntü).
type PoolBalance struct {
	BalanceInfo
	// PendingOutgoing adresin havuzdaki işlemlerinin tutar + ücret toplamıdır.
	PendingOutgoing int64
	// PendingIncoming başkalarının havuzdaki işlemleriyle adrese gelen tutarlardır; onaylanana
	// kadar harcanamaz.
	PendingIncoming int64
	// Spendable havuzun yeni bir işlemi kabul ederken kullandığı bakiyedir: onaylı bakiye + uç+1
	// bağlanırken olgunlaşan coinbase (MaturesAt == TipHeight+1 olan kayıt, ImmatureCoinbases'te
	// de görünür) − PendingOutgoing.
	Spendable int64
}

// Balance address'in bakiye ayrıntısını havuzdaki bekleyen tutarlarla döndürür.
func (m *Mempool) Balance(address string) (PoolBalance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	bc := m.bc
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	if err := m.syncLocked(); err != nil {
		return PoolBalance{}, err
	}
	info, err := bc.balanceLocked(address)
	if err != nil {
		return PoolBalance{}, err
	}
	matured, err := m.maturedLocked()
	if err != nil {
		return PoolBalance{}, err
	}
	avail, err := matured.balance(address)
	if err != nil {
		return PoolBalance{}, err
	}
	out := PoolBalance{BalanceInfo: info}
	for _, p := range m.senders[address] {
		if p.tx.Amount < 0 || p.tx.Fee < 0 || p.tx.Amount > math.MaxInt64-p.tx.Fee ||
			out.PendingOutgoing > math.MaxInt64-(p.tx.Amount+p.tx.Fee) {
			return PoolBalance{}, fmt.Errorf("%w: bekleyen giden toplam", ErrOverflow)
		}
		out.PendingOutgoing += p.tx.Amount + p.tx.Fee
	}
	// Havuzdaki zincirler uç+1'de geçerli tutulur (syncLocked), giden toplam bakiyeyi aşamaz.
	if out.PendingOutgoing > avail {
		return PoolBalance{}, fmt.Errorf("%w: bekleyen giden %d, kullanılabilir %d", ErrCorruptState, out.PendingOutgoing, avail)
	}
	out.Spendable = avail - out.PendingOutgoing
	for sender, list := range m.senders {
		if sender == address { // kendine transfer kuralla yasak; yine de iki yanda sayılmaz
			continue
		}
		for _, p := range list {
			if p.tx.Receiver != address {
				continue
			}
			if p.tx.Amount < 0 || out.PendingIncoming > math.MaxInt64-p.tx.Amount {
				return PoolBalance{}, fmt.Errorf("%w: bekleyen gelen toplam", ErrOverflow)
			}
			out.PendingIncoming += p.tx.Amount
		}
	}
	return out, nil
}
