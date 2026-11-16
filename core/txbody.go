package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// Hafif istemci (SPV) için işlem gövdesi ve tanık özeti. GET /merkleproof bir işlemin gövdesini
// (~153 bayt) ve tanık özetini (32 bayt) verir; istemci kimliği TxIDFromParts ile kendisi
// hesaplar, Merkle kanıtını kendi doğruladığı başlığın köküne karşı denetler ve gövdeyi
// DecodeTxBody ile çözer. Bu dosyadaki çözücü uzlaşma yolunda kullanılmaz (blok ve havuz işlemleri
// JSON'dan gelir); istemciler için başvuru uygulamasıdır.

// ErrBadTxBody gövde baytlarının kanonik gövde düzenine (transaction.go) uymadığını söyler.
var ErrBadTxBody = errors.New("geçersiz işlem gövdesi")

// txBodyFields gövdedeki alan sayısıdır (transaction.go "İşlem kodlaması").
const txBodyFields = 8

// BodyBytes işlemin kanonik gövdesidir (tanık hariç; düzen transaction.go). SHA256(BodyBytes())
// kimliğin gövde özetidir.
func (tx *Transaction) BodyBytes() []byte {
	var buf bytes.Buffer
	tx.writeBody(&buf)
	return buf.Bytes()
}

// WitnessHash tanığın özetidir: SHA256(field(PublicKey) ‖ field(Signature)).
func (tx *Transaction) WitnessHash() [32]byte {
	return tx.witnessHash()
}

// TxIDFromParts kimliği gövde ve tanık özetinden hesaplar:
// SHA256(field("QBTC-TXID-v3") ‖ SHA256(gövde) ‖ tanık özeti). Gövde çözülmeden de hesaplanabilir;
// istemci kanıtı doğruladıktan sonra içeriği DecodeTxBody ile okur.
func TxIDFromParts(body []byte, witnessHash [32]byte) [32]byte {
	bh := sha256.Sum256(body)
	var buf bytes.Buffer
	writeField(&buf, []byte(txIDTag))
	buf.Write(bh[:])
	buf.Write(witnessHash[:])
	return sha256.Sum256(buf.Bytes())
}

// DecodeTxBody kanonik gövdeyi tanıksız bir işleme çözer (PublicKey ve Signature boş kalır).
// Katıdır: tam 8 alan, tamsayılar tam 8 bayt, coinbase işareti tam 1 bayt ve 0 ya da 1, artık
// bayt yok; sonuç yeniden kodlandığında girdiyle bayt bayt aynı olmalıdır. Biçim denetimi yapar,
// geçerlilik (adres, tutar, imza) denetlemez: hafif istemci zincirin geçerli olduğuna güvenir.
func DecodeTxBody(body []byte) (*Transaction, error) {
	fields := make([][]byte, 0, txBodyFields)
	rest := body
	for len(rest) > 0 {
		if len(fields) == txBodyFields || len(rest) < 4 {
			return nil, ErrBadTxBody
		}
		n := binary.BigEndian.Uint32(rest[:4])
		rest = rest[4:]
		if uint64(n) > uint64(len(rest)) {
			return nil, ErrBadTxBody
		}
		fields = append(fields, rest[:n])
		rest = rest[n:]
	}
	if len(fields) != txBodyFields {
		return nil, ErrBadTxBody
	}
	u64 := func(b []byte) (uint64, bool) {
		if len(b) != 8 {
			return 0, false
		}
		return binary.BigEndian.Uint64(b), true
	}
	amount, ok1 := u64(fields[2])
	fee, ok2 := u64(fields[3])
	expiry, ok3 := u64(fields[4])
	nonce, ok4 := u64(fields[5])
	if !ok1 || !ok2 || !ok3 || !ok4 || len(fields[6]) != 1 || fields[6][0] > 1 {
		return nil, ErrBadTxBody
	}
	tx := &Transaction{
		Sender:       string(fields[0]),
		Receiver:     string(fields[1]),
		Amount:       int64(amount),
		Fee:          int64(fee),
		ExpiryHeight: int64(expiry),
		Nonce:        nonce,
		IsCoinbase:   fields[6][0] == 1,
	}
	if len(fields[7]) > 0 {
		tx.Commitment = append([]byte(nil), fields[7]...)
	}
	if !bytes.Equal(tx.BodyBytes(), body) {
		return nil, ErrBadTxBody
	}
	return tx, nil
}
