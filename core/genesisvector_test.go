package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// genesisVectorFile bağımsız betiğin (scripts/genesis_vektor.py) ürettiği vektör dosyasıdır:
// testdata/<ağ>_vector.json. Betik Go kodunu çağırmaz; ilk altı alandan (girdi) kalanları kendisi
// hesaplar: genesis'in alıcısı, coinbase kimliği, Merkle kökü ve özeti, ağın imza alanı ve
// bilinen-yanıt işleminin (katTx) kimliği ile o alanda imzalanan mesajının özeti. Zorluk tabanı
// (pow_limit_bits) başlığa girmez; betik onu başlangıç hedefiyle karşılaştırır, değerini ağın
// parametresine bu test bağlar. Dosyanın betiğin üreteceği baytlarla aynı olduğunu CI'da betik
// denetler; buradaki test her değerin Go kodunun hesapladığıyla aynı olduğunu denetler. Mainnet
// vektörü lansmanda Bitcoin alanlarıyla gelir (docs/LANSMAN.md §4.2); o alanlar buraya mainnet
// genesis'iyle birlikte eklenir.
type genesisVectorFile struct {
	Network        string `json:"network"`
	Timestamp      int64  `json:"timestamp"`
	Message        string `json:"message"`
	Bits           string `json:"bits"`
	PowLimitBits   string `json:"pow_limit_bits"`
	Nonce          uint64 `json:"nonce"`
	Receiver       string `json:"receiver"`
	CoinbaseTxID   string `json:"coinbase_txid"`
	MerkleRoot     string `json:"merkle_root"`
	Hash           string `json:"hash"`
	SigDomain      string `json:"sig_domain"`
	KATTxID        string `json:"kat_txid"`
	KATSigningHash string `json:"kat_signing_hash"`
}

func loadGenesisVector(t *testing.T, path string) genesisVectorFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("vektör dosyası okunamadı: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var v genesisVectorFile
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s çözülemedi: %v", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("%s: nesneden sonra artık veri var (%v)", path, err)
	}
	return v
}

// Genesis'i tanımlı her ağın vektör dosyası vardır ve Go kodunun hesabıyla aynıdır; genesis'i
// tanımsız ağın (lansman öncesi mainnet) vektör dosyası olamaz. Kodlama iki uygulamadan yalnızca
// birinde değişirse bu test ya da betiğin CI adımı kırmızıya döner. Elle yazılmış sabitler
// (genesisVectors, TestGenesisPinned) ayrıca durur: betikle dosyayı birlikte yeniden üreten bir
// değişiklik onları sessizce değiştiremez.
func TestGenesisVectorFiles(t *testing.T) {
	defined := 0
	for _, p := range []*Params{MainnetParams, TestnetParams, RegtestParams} {
		path := filepath.Join("testdata", p.Name+"_vector.json")
		if p.GenesisHash == "" {
			if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s: genesis'i tanımsız ağın vektör dosyası olamaz (%v)", path, err)
			}
			continue
		}
		defined++
		t.Run(p.Name, func(t *testing.T) {
			file := loadGenesisVector(t, path)
			g, err := NewGenesisBlock(p)
			if err != nil {
				t.Fatal(err)
			}
			domain, err := p.SigDomain()
			if err != nil {
				t.Fatal(err)
			}
			cb, tx := g.Transactions[0], katTx()
			signing := sha256.Sum256(tx.SigningBytes(domain))
			want := genesisVectorFile{
				Network:        p.Name,
				Timestamp:      p.GenesisTimestamp,
				Message:        p.GenesisMessage,
				Bits:           fmt.Sprintf("%08x", p.Bits),
				PowLimitBits:   fmt.Sprintf("%08x", p.PowLimitBits),
				Nonce:          p.GenesisNonce,
				Receiver:       cb.Receiver,
				CoinbaseTxID:   cb.ID(),
				MerkleRoot:     g.MerkleRoot,
				Hash:           g.Hash,
				SigDomain:      domain,
				KATTxID:        tx.ID(),
				KATSigningHash: hex.EncodeToString(signing[:]),
			}
			if file != want {
				t.Fatalf("vektör dosyası Go kodunun hesabıyla aynı değil:\ndosya %+v\nGo    %+v", file, want)
			}
		})
	}
	// Bilinmeyen bir ağ adıyla bırakılmış vektör dosyası da kalamaz.
	files, err := filepath.Glob(filepath.Join("testdata", "*_vector.json"))
	if err != nil || len(files) != defined {
		t.Fatalf("testdata altında %d vektör dosyası var, genesis'i tanımlı ağ sayısı %d (%v)", len(files), defined, err)
	}
}
