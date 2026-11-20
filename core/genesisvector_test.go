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
	"reflect"
	"testing"
)

// genesisVectorFile bağımsız betiğin (scripts/genesis_vektor.py) ürettiği vektör dosyasıdır:
// testdata/<ağ>_vector.json. Betik Go kodunu çağırmaz; girdi alanlarından (ağ, zaman damgası,
// mesaj, başlangıç hedefi, taban, nonce; mainnet'te iki Bitcoin başlığı) kalanları kendisi
// hesaplar: genesis'in alıcısı, coinbase kimliği, Merkle kökü ve özeti, ağın imza alanı,
// bilinen-yanıt işleminin (katTx) kimliği ile o alanda imzalanan mesajının özeti ve mainnet'te
// mesajdaki Bitcoin bloğunun özeti, zamanı (t_b) ve onu onaylayan bloğun özeti. Zorluk tabanı
// (pow_limit_bits) başlığa girmez; betik onu başlangıç hedefiyle karşılaştırır, değerini ağın
// parametresine bu test bağlar. Dosyanın betiğin üreteceği baytlarla aynı olduğunu CI'da betik
// denetler; buradaki test her değerin Go kodunun hesapladığıyla aynı olduğunu denetler.
type genesisVectorFile struct {
	Network                 string   `json:"network"`
	Timestamp               int64    `json:"timestamp"`
	Message                 string   `json:"message"`
	Bits                    string   `json:"bits"`
	PowLimitBits            string   `json:"pow_limit_bits"`
	Nonce                   uint64   `json:"nonce"`
	BitcoinHeaders          []string `json:"bitcoin_headers"`
	Receiver                string   `json:"receiver"`
	CoinbaseTxID            string   `json:"coinbase_txid"`
	MerkleRoot              string   `json:"merkle_root"`
	Hash                    string   `json:"hash"`
	SigDomain               string   `json:"sig_domain"`
	KATTxID                 string   `json:"kat_txid"`
	KATSigningHash          string   `json:"kat_signing_hash"`
	BitcoinHash             string   `json:"bitcoin_hash"`
	BitcoinTime             int64    `json:"bitcoin_time"`
	BitcoinConfirmationHash string   `json:"bitcoin_confirmation_hash"`
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

// expectedVector Go kodunun p ağı için hesapladığı vektördür. lf nil değilse (mainnet) Bitcoin
// alanları lansman dosyasındaki başlıklardan, Go'nun kendi başlık doğrulamasıyla doldurulur.
func expectedVector(t *testing.T, p *Params, lf *launchFile, rules launchRules) genesisVectorFile {
	t.Helper()
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
	v := genesisVectorFile{
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
	if lf == nil {
		return v
	}
	limit, ok := compactToBig(rules.bitcoinMaxBits)
	if !ok || len(lf.BitcoinHeaders) != 2 {
		t.Fatalf("lansman dosyasının Bitcoin başlıkları okunamadı: %+v", lf)
	}
	block, err := parseBitcoinHeader(lf.BitcoinHeaders[0], limit)
	if err != nil {
		t.Fatal(err)
	}
	confirmation, err := parseBitcoinHeader(lf.BitcoinHeaders[1], limit)
	if err != nil {
		t.Fatal(err)
	}
	v.BitcoinHeaders = lf.BitcoinHeaders
	v.BitcoinHash, v.BitcoinTime, v.BitcoinConfirmationHash = block.hash, block.time, confirmation.hash
	return v
}

// Genesis'i tanımlı her ağın vektör dosyası vardır ve Go kodunun hesabıyla aynıdır; genesis'i
// tanımsız ağın (lansman öncesi mainnet) vektör dosyası olamaz. Kodlama iki uygulamadan yalnızca
// birinde değişirse bu test ya da betiğin CI adımı kırmızıya döner. Lansman commit'i yalnızca
// mainnet.json ile testdata/mainnet_vector.json dosyalarını getirir: biri gelip öbürü gelmezse ya
// da ikisi tutmazsa bu test kırmızıya döner. Elle yazılmış sabitler (genesisVectors,
// TestGenesisPinned) ayrıca durur: betikle dosyayı birlikte yeniden üreten bir değişiklik onları
// sessizce değiştiremez.
func TestGenesisVectorFiles(t *testing.T) {
	launch, err := parseLaunchFile(mainnetLaunchJSON)
	if err != nil {
		t.Fatal(err)
	}
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
			var lf *launchFile
			if p == MainnetParams {
				// Mainnet vektörü Bitcoin alanlarını da taşır; onlar lansman dosyasındaki
				// başlıklardan gelir.
				if lf = launch; lf == nil {
					t.Fatal("mainnet genesis'i dolu ama lansman dosyası boş")
				}
			}
			file := loadGenesisVector(t, path)
			if want := expectedVector(t, p, lf, mainnetLaunchRules); !reflect.DeepEqual(file, want) {
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
