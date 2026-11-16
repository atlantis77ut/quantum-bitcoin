package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"quantum-bitcoin/crypto"
)

// txIDTag işlem kimliği ve Merkle kökü içindir ve ağdan bağımsızdır. Genesis bir işlem
// içerdiğinden, kimlik genesis özetine bağlı olsaydı genesis özeti kendine bağlı olurdu.
// İmza ise ayrı bir alanda atılır (Params.SigDomain, "QBTC-SIG-v5|<ağ>|<genesis özeti>"):
// testnet'te atılan imza mainnet'te ya da regtest'te geçersizdir. v4'te gövdedeki zaman
// damgası yerini son kullanma yüksekliğine bıraktı; alan sayısı aynı kaldığından v3 ve v4
// gövdeleri bayt düzeyinde çakışabilirdi, iki etiket de bu yüzden artırıldı. v3'te açık anahtar
// gövdeden tanığa geçti ve kimlik gövde ile tanığı ayrı özetler (aşağıda); coinbase'e taahhüt
// yuvası eklendi (kullanıcı kararı 2026-11-15).
const txIDTag = "QBTC-TXID-v3"

// İşlem kodlaması (üçüncü taraf uygulamalar için; field(x) = 4 baytlık büyük-endian uzunluk ‖ x):
//
//	gövde = field(Sender) ‖ field(Receiver) ‖ field(Amount) ‖ field(Fee) ‖ field(ExpiryHeight) ‖
//	        field(Nonce) ‖ field(coinbase ise 0x01, değilse 0x00) ‖ field(Commitment)
//	tanık = field(PublicKey) ‖ field(Signature)
//	kimlik = SHA256(field("QBTC-TXID-v3") ‖ SHA256(gövde) ‖ SHA256(tanık))
//	imzalanan = field(imza alanı, Params.SigDomain) ‖ gövde
//
// Tamsayılar 8 baytlık büyük-endian yazılır (int64 alanlar uint64'e çevrilerek). Kimlik imzayı
// kapsamaya devam eder: imzası değiştirilmiş bir kopya başka kimlik taşır, ret önbellekleri bir
// dürüst kimliği zehirleyemez. Tanık ayrı özetlendiği için ileride eski bloklarda imza ve açık
// anahtar 32 baytlık özetle budanabilir ve hafif istemci işlem içeriğini gövde ve tanık özetiyle
// (~185 bayt) Merkle köküne karşı doğrulayabilir. Açık anahtar imzalanan gövdede yoktur ama
// imzaya bağlıdır: gövdedeki Sender açık anahtardan türetilir (checkFormat) ve ML-DSA (FIPS 204)
// imzalanan mesajla birlikte açık anahtarın özetini (tr) de imzalar.

// CoinbaseCommitmentSize coinbase taahhüt yuvasının uzunluğudur. Yuvanın içeriği uzlaşmada
// denetlenmez (kullanıcı kararı 2026-11-15): ileride bir durum kökü ya da başka bir taahhüt
// yumuşak çatalla buraya bağlanabilir. Böyle bir taahhüt coinbase'in kendi kimliğine ya da Merkle
// köküne bağlı olamaz (döngüsel olurdu). Coinbase dışı işlemlerde yuva boş olmalıdır.
const CoinbaseCommitmentSize = 32

var (
	ErrNoGenesis          = errors.New("bu ağın genesis bloğu henüz tanımlı değil")
	ErrNonPositiveAmount  = errors.New("tutar pozitif olmalı")
	ErrNegativeFee        = errors.New("işlem ücreti negatif olamaz")
	ErrBadSender          = errors.New("gönderen adresi açık anahtarla eşleşmiyor")
	ErrBadReceiver        = errors.New("geçersiz alıcı adresi")
	ErrSelfTransfer       = errors.New("kendine transfer desteklenmiyor")
	ErrBadSignature       = errors.New("geçersiz imza")
	ErrUnexpectedCoinbase = errors.New("coinbase işlemi yalnızca bloğun ilk işlemi olabilir")
	ErrBadExpiry          = errors.New("son kullanma yüksekliği en az 1 olmalı")
	ErrTxExpired          = errors.New("işlemin son kullanma yüksekliği geçti")
	ErrUnexpectedCommit   = errors.New("taahhüt yuvası yalnızca coinbase'de olabilir")
)

// Transaction bir hesaptan diğerine imzalı transferdir.
//
// ExpiryHeight işlemin dahil edilebileceği son blok yüksekliğidir (dahil): h <= ExpiryHeight.
// Zorunludur (>= 1); "süresiz" değeri yoktur, alanı eksik istemci reddedilir. Uzlaşmada azami
// ufuk yoktur: ufuk ömrü sınırlamaz, yalnızca örtük bir "şu yükseklikten itibaren geçerli"
// kilidi yaratırdı; azami ömür ve varsayılan süre mempool/cüzdan politikasıdır. Coinbase'de 0'dır.
// Reorg bir işlemi ExpiryHeight'ın ötesine iterse işlem bir daha zincire giremez; alıcı için
// etkin kesinlik onay sayısı ile ExpiryHeight'a kalan bloğun küçüğüdür.
type Transaction struct {
	Sender       string `json:"sender"`
	Receiver     string `json:"receiver"`
	Amount       int64  `json:"amount"`        // Taban birim (1 QBTC = Coin)
	Fee          int64  `json:"fee"`           // Madenciye giden ücret, taban birim; göndericiden Amount+Fee düşülür
	ExpiryHeight int64  `json:"expiry_height"` // Dahil edilebileceği son yükseklik (dahil); bkz. tür belgesi
	Nonce        uint64 `json:"nonce"`         // Gönderenin hesap sırası; her işlemde 1 artar (replay koruması)
	PublicKey    []byte `json:"public_key"`    // Gönderenin ML-DSA-65 açık anahtarı
	Signature    []byte `json:"signature"`
	IsCoinbase   bool   `json:"is_coinbase"`          // Sistemin ödül olarak bastığı para mı?
	Commitment   []byte `json:"commitment,omitempty"` // Coinbase'de tam CoinbaseCommitmentSize bayt, diğerlerinde boş
}

func writeField(buf *bytes.Buffer, b []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	buf.Write(l[:])
	buf.Write(b)
}

// SigningBytes imzalanan kanonik veridir: ağın imza alanı + gövde. Gövde, tanık (PublicKey,
// Signature) dışındaki tüm alanları uzunluk önekli ve sabit sırayla içerir; böylece iki farklı
// işlem aynı baytlara serileşemez.
func (tx *Transaction) SigningBytes(domain string) []byte {
	var buf bytes.Buffer
	writeField(&buf, []byte(domain))
	tx.writeBody(&buf)
	return buf.Bytes()
}

// writeBody imza ve kimlik için ortak, alan etiketsiz gövdeyi yazar (düzen yukarıda).
func (tx *Transaction) writeBody(buf *bytes.Buffer) {
	writeField := func(b []byte) { writeField(buf, b) }
	writeField([]byte(tx.Sender))
	writeField([]byte(tx.Receiver))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(tx.Amount))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(tx.Fee))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], uint64(tx.ExpiryHeight))
	writeField(n[:])
	binary.BigEndian.PutUint64(n[:], tx.Nonce)
	writeField(n[:])
	if tx.IsCoinbase {
		writeField([]byte{1})
	} else {
		writeField([]byte{0})
	}
	writeField(tx.Commitment)
}

// bodyHash gövdenin, witnessHash tanığın (field(PublicKey) ‖ field(Signature)) özetidir.
func (tx *Transaction) bodyHash() [32]byte {
	var buf bytes.Buffer
	tx.writeBody(&buf)
	return sha256.Sum256(buf.Bytes())
}

func (tx *Transaction) witnessHash() [32]byte {
	var buf bytes.Buffer
	writeField(&buf, tx.PublicKey)
	writeField(&buf, tx.Signature)
	return sha256.Sum256(buf.Bytes())
}

// IDBytes işlemin imza dahil, ağdan bağımsız kimliğidir:
// SHA256(field(txIDTag) ‖ SHA256(gövde) ‖ SHA256(tanık)). İmza alanını (SigningBytes) asla
// kullanmaz.
func (tx *Transaction) IDBytes() [32]byte {
	body, wit := tx.bodyHash(), tx.witnessHash()
	var buf bytes.Buffer
	writeField(&buf, []byte(txIDTag))
	buf.Write(body[:])
	buf.Write(wit[:])
	return sha256.Sum256(buf.Bytes())
}

// ID işlem kimliğinin hex gösterimidir.
func (tx *Transaction) ID() string {
	id := tx.IDBytes()
	return hex.EncodeToString(id[:])
}

// Sign işlemi gönderenin anahtarıyla, verilen ağın imza alanında imzalar; Sender ve
// PublicKey alanlarını doldurur. Genesis'i tanımsız ağ (lansman öncesi mainnet) için
// imza atılmaz: aksi halde lansmandan önce atılan imzalar lansmandan sonra geçerli kalırdı.
func (tx *Transaction) Sign(kp *crypto.KeyPair, p *Params) error {
	domain, err := p.SigDomain()
	if err != nil {
		return err
	}
	tx.Sender = kp.Address
	tx.PublicKey = append([]byte(nil), kp.PublicKey...)
	sig, err := crypto.Sign(kp.SecretKey, tx.SigningBytes(domain))
	if err != nil {
		return err
	}
	tx.Signature = sig
	return nil
}

// VerifyStateless durumdan bağımsız kontrolleri yapar: biçim (checkFormat) ve verilen
// ağın imza alanında imza. Coinbase işlemleri burada reddedilir; onlar blok düzeyinde
// doğrulanır. Son kullanma yüksekliği bloğa bağlıdır, bkz. CheckExpiry.
func (tx *Transaction) VerifyStateless(p *Params) error {
	if err := tx.checkFormat(); err != nil {
		return err
	}
	return tx.verifySignature(p)
}

// CheckExpiry işlemin height yüksekliğindeki bir bloğa girip giremeyeceğini söyler.
// Kural tek yerdedir: blok doğrulaması ve blok üretimi (ileride mempool) aynısını kullanır.
func (tx *Transaction) CheckExpiry(height int64) error {
	if height > tx.ExpiryHeight {
		return fmt.Errorf("%w: yükseklik %d, son geçerli %d", ErrTxExpired, height, tx.ExpiryHeight)
	}
	return nil
}

// checkFormat imza doğrulaması gerektirmeyen ucuz kontrolleri yapar.
func (tx *Transaction) checkFormat() error {
	if tx.IsCoinbase {
		return ErrUnexpectedCoinbase
	}
	if len(tx.Commitment) != 0 {
		return ErrUnexpectedCommit
	}
	if tx.Amount <= 0 {
		return ErrNonPositiveAmount
	}
	if tx.Fee < 0 {
		return ErrNegativeFee
	}
	if tx.ExpiryHeight < 1 {
		return ErrBadExpiry
	}
	if !crypto.IsValidAddress(tx.Receiver) {
		return ErrBadReceiver
	}
	if len(tx.PublicKey) != crypto.PublicKeySize || crypto.AddressFromPublicKey(tx.PublicKey) != tx.Sender {
		return ErrBadSender
	}
	if tx.Sender == tx.Receiver {
		return ErrSelfTransfer
	}
	return nil
}

// verifySignature imzayı verilen ağın imza alanında doğrular (ML-DSA, pahalı).
func (tx *Transaction) verifySignature(p *Params) error {
	domain, err := p.SigDomain()
	if err != nil {
		return err
	}
	if !crypto.Verify(tx.PublicKey, tx.SigningBytes(domain), tx.Signature) {
		return ErrBadSignature
	}
	return nil
}

func (tx *Transaction) String() string {
	return fmt.Sprintf("tx{%s -> %s, %s QBTC, ücret=%s, nonce=%d, son=%d}", tx.Sender, tx.Receiver,
		FormatAmount(tx.Amount), FormatAmount(tx.Fee), tx.Nonce, tx.ExpiryHeight)
}
