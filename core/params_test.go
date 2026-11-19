package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"quantum-bitcoin/crypto"
)

// Bağımsız Python uygulamasıyla (Go kodundan ayrı yazılmış; scripts/genesis_vektor.py) üretilmiş
// bilinen-yanıt vektörleri. Params'taki sabitlerden ayrı tutulur: biri değişirse test kırmızıya
// döner. Betiğin ürettiği dosyalar testdata altındadır (TestGenesisVectorFiles); buradaki
// sabitler elle yazılır ve betikle birlikte yeniden üretilmez.
var genesisVectors = []struct {
	p        *Params
	receiver string
	txid     string
	merkle   string
	nonce    uint64
	hash     string
}{
	{
		p:        RegtestParams,
		receiver: "QBTC4e6964f9e432957c47b419629131c28aea8f3e6e",
		txid:     "63a8d5fbe5f111b7aca94c3cac5210d99d4765b98924229404a3f9a57ddb554b",
		merkle:   "82ab1a2dd38881206fbdbf76cc3c03a76694e3994d6a4c2241d3d3068f4d5d72",
		nonce:    2,
		hash:     "0c9f0d89a173a869e2293a062ddbf9c2a1c3c71c8dc4f7a0d2d8828ebcf6e79d",
	},
	{
		p:        TestnetParams,
		receiver: "QBTCd3a378937e0e43e9baf18c21b24dd539e398f1bf",
		txid:     "239630bf032f8b41f44ee5bac43d6b4026bf4acd9995fe6d9f13e5001c7789ed",
		merkle:   "9329198a9da4c1a5ca1e84fd22da73c0d1ae5ad091cc8ea6ebf8f00996a2db45",
		nonce:    168553,
		hash:     "00009ece2400256262b63f068e80aea26f7f6a5682a6688428783a01ace7b34c",
	},
}

// grindGenesis teşhis içindir: eşleşmezse doğru nonce ve özeti test çıktısına yazdırır.
func grindGenesis(b *Block) (uint64, string) {
	c := *b
	for c.Nonce = 0; c.Nonce < 50_000_000; c.Nonce++ {
		if h := c.CalculateHash(); MeetsTarget(h, c.Bits) {
			return c.Nonce, h
		}
	}
	return 0, ""
}

func TestGenesisPinned(t *testing.T) {
	for _, v := range genesisVectors {
		t.Run(v.p.Name, func(t *testing.T) {
			g, err := NewGenesisBlock(v.p)
			if err != nil {
				t.Fatal(err)
			}
			cb := g.Transactions[0]
			if len(g.Transactions) != 1 || !cb.IsCoinbase || cb.Amount != 0 || cb.Fee != 0 {
				t.Fatalf("genesis sıfır değerli tek coinbase değil: %+v", g.Transactions)
			}
			if cb.Receiver != v.receiver || !crypto.IsValidAddress(cb.Receiver) {
				t.Fatalf("alıcı %q, beklenen %q (geçerli adres olmalı)", cb.Receiver, v.receiver)
			}
			if id := cb.ID(); id != v.txid {
				t.Fatalf("coinbase kimliği %s, beklenen %s", id, v.txid)
			}
			if g.MerkleRoot != v.merkle {
				t.Fatalf("Merkle kökü %s, beklenen %s", g.MerkleRoot, v.merkle)
			}
			if g.Nonce != v.nonce || g.Hash != v.hash || g.Hash != v.p.GenesisHash {
				n, h := grindGenesis(g)
				t.Fatalf("genesis %s (nonce %d); beklenen %s; bu kodlamayla doğru nonce %d, özet %s",
					g.Hash, g.Nonce, v.hash, n, h)
			}
			if !MeetsTarget(g.Hash, v.p.Bits) {
				t.Fatal("genesis kendi hedefini karşılamıyor")
			}
			bc := &Blockchain{params: v.p, bits: v.p.Bits}
			if err := bc.checkHeader(g); err != nil {
				t.Fatalf("genesis başlık kontrolünden geçmedi: %v", err)
			}
		})
	}
}

func TestParamsBits(t *testing.T) {
	cases := []struct {
		p          *Params
		difficulty int
	}{
		{RegtestParams, 1},
		{TestnetParams, 4},
		// Mainnet başlangıç hedefi lansmanda seçilir (yer tutucu, powlimit_test.go).
	}
	for _, c := range cases {
		want, err := DifficultyToBits(c.difficulty)
		if err != nil || c.p.Bits != want {
			t.Errorf("%s bits %#x, beklenen %#x (%v)", c.p.Name, c.p.Bits, want, err)
		}
	}
}

// katTx bağımsız betikteki (scripts/genesis_vektor.py, KAT_* sabitleri) sabit baytlı işlemdir;
// imzası geçerli olmak zorunda değildir.
func katTx() Transaction {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = byte(i)
	}
	sig := make([]byte, 16)
	for i := range sig {
		sig[i] = 0xAB
	}
	return Transaction{
		Sender:       "QBTC" + "1111111111111111111111111111111111111111",
		Receiver:     "QBTC" + "2222222222222222222222222222222222222222",
		Amount:       123456789,
		Fee:          1000,
		ExpiryHeight: 500000,
		Nonce:        7,
		PublicKey:    pub,
		Signature:    sig,
	}
}

func TestTransactionKAT(t *testing.T) {
	tx := katTx()
	if id := tx.ID(); id != "f9ac5dab63d144232a87371c58c0795d67bfd7ba5ec573c2dcb0c708bb07765f" {
		t.Fatalf("işlem kimliği %s", id)
	}
	want := map[*Params]string{
		RegtestParams: "be7b15ee7a9a1ddec37440b73248dae72b36f7537fd6827c2ab534af2d153449",
		TestnetParams: "cbba7779b2f52f5f37173baccc5cfe7af8b35c0f9804dd7166bbc2a521a3cca3",
	}
	for p, w := range want {
		d, err := p.SigDomain()
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(tx.SigningBytes(d))
		if got := hex.EncodeToString(h[:]); got != w {
			t.Errorf("%s imza mesajı özeti %s, beklenen %s", p.Name, got, w)
		}
	}
}

// İmza ağa ve genesis'e bağlıdır: başka ağın veya eski alan etiketlerinin imzası geçersizdir.
func TestSignatureBoundToNetwork(t *testing.T) {
	alice, bob := newKey(t), newKey(t)
	tx := Transaction{Receiver: bob.Address, Amount: Coin, Fee: 7, ExpiryHeight: testExpiry, Nonce: 1}
	if err := tx.Sign(alice, TestnetParams); err != nil {
		t.Fatal(err)
	}
	if err := tx.VerifyStateless(TestnetParams); err != nil {
		t.Fatalf("testnet imzası testnet'te reddedildi: %v", err)
	}
	if err := tx.VerifyStateless(RegtestParams); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("testnet imzası regtest'te: %v", err)
	}

	// Kimlik ağdan bağımsızdır; imzalanan mesaj ağa bağlıdır.
	dt, _ := TestnetParams.SigDomain()
	dr, _ := RegtestParams.SigDomain()
	if string(tx.SigningBytes(dt)) == string(tx.SigningBytes(dr)) {
		t.Fatal("imza mesajı ağdan bağımsız")
	}

	dt3 := "QBTC-SIG-v3|testnet|" + TestnetParams.GenesisHash // v3 gövdesiyle bayt düzeyinde çakışabilirdi
	dt4 := "QBTC-SIG-v4|testnet|" + TestnetParams.GenesisHash // v4 gövdesi açık anahtarı içeriyordu
	for _, old := range []string{"QBTC-TX-v1|qbtc-mainnet", "QBTC-TX-v2|qbtc-mainnet", dt3, dt4} {
		legacy := tx
		sig, err := crypto.Sign(alice.SecretKey, legacy.SigningBytes(old))
		if err != nil {
			t.Fatal(err)
		}
		legacy.Signature = sig
		for _, p := range []*Params{TestnetParams, RegtestParams} {
			if err := legacy.VerifyStateless(p); !errors.Is(err, ErrBadSignature) {
				t.Errorf("%q imzası %s'te kabul edildi: %v", old, p.Name, err)
			}
		}
	}
}

// Genesis'i tanımsız mainnet ne başlatılabilir ne de onun için imza atılabilir/doğrulanabilir.
func TestMainnetRefusedBeforeLaunch(t *testing.T) {
	if _, err := NewBlockchain(t.TempDir(), MainnetParams); !errors.Is(err, ErrNoGenesis) {
		t.Fatalf("NewBlockchain: %v", err)
	}
	if _, err := NewGenesisBlock(MainnetParams); !errors.Is(err, ErrNoGenesis) {
		t.Fatalf("NewGenesisBlock: %v", err)
	}
	alice, bob := newKey(t), newKey(t)
	tx := Transaction{Receiver: bob.Address, Amount: Coin, ExpiryHeight: testExpiry, Nonce: 1}
	if err := tx.Sign(alice, MainnetParams); !errors.Is(err, ErrNoGenesis) {
		t.Fatalf("Sign: %v", err)
	}
	if err := tx.Sign(alice, RegtestParams); err != nil {
		t.Fatal(err)
	}
	if err := tx.VerifyStateless(MainnetParams); !errors.Is(err, ErrNoGenesis) {
		t.Fatalf("VerifyStateless: %v", err)
	}
	if _, err := ParamsByName("yoknet"); err == nil {
		t.Fatal("bilinmeyen ağ adı kabul edildi")
	}
}

// Kodlama sessizce değişirse (ya da sabitler tutarsızsa) düğüm başlamaz.
func TestBadGenesisParamsRefused(t *testing.T) {
	wrongNonce := *RegtestParams
	wrongNonce.GenesisNonce++
	wrongHash := *RegtestParams
	wrongHash.GenesisHash = TestnetParams.GenesisHash
	for name, p := range map[string]*Params{"nonce": &wrongNonce, "özet": &wrongHash} {
		if _, err := NewBlockchain(t.TempDir(), p); !errors.Is(err, ErrBadGenesis) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Bir ağın veri dizini başka ağın parametreleriyle açılamaz; eski şema reddedilir.
func TestDataDirBoundToNetwork(t *testing.T) {
	dir := t.TempDir()
	bc, err := NewBlockchain(dir, RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := bc.db.Get([]byte(keyGenesis), nil); err != nil || string(v) != RegtestParams.GenesisHash {
		t.Fatalf("genesis anahtarı yazılmadı: %q %v", v, err)
	}
	if got := mustBalance(t, bc, RegtestParams.GenesisReceiver()); got != 0 {
		t.Fatalf("genesis alıcısına %d yazıldı (premine yok)", got)
	}
	bc.Close()
	if _, err := NewBlockchain(dir, TestnetParams); !errors.Is(err, ErrGenesisMismatch) {
		t.Fatalf("regtest dizini testnet ile açıldı: %v", err)
	}
	bc, err = NewBlockchain(dir, RegtestParams)
	if err != nil {
		t.Fatalf("aynı ağla yeniden açılamadı: %v", err)
	}
	if err := bc.db.Put([]byte(keySchema), []byte("2"), nil); err != nil {
		t.Fatal(err)
	}
	bc.Close()
	if _, err := NewBlockchain(dir, RegtestParams); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("şema 2 dizini açıldı: %v", err)
	}

	dir2 := t.TempDir()
	bc2, err := NewBlockchain(dir2, RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc2.db.Delete([]byte(keyGenesis), nil); err != nil {
		t.Fatal(err)
	}
	bc2.Close()
	if _, err := NewBlockchain(dir2, RegtestParams); !errors.Is(err, ErrGenesisMismatch) {
		t.Fatalf("genesis anahtarsız dizin açıldı: %v", err)
	}

	// Son blok kaydı silinmiş ama kayıt içeren dizine yeni genesis yazılmaz.
	dir3 := t.TempDir()
	bc3, err := NewBlockchain(dir3, RegtestParams)
	if err != nil {
		t.Fatal(err)
	}
	if err := bc3.db.Delete([]byte(keyLastHash), nil); err != nil {
		t.Fatal(err)
	}
	bc3.Close()
	if _, err := NewBlockchain(dir3, TestnetParams); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("boş olmayan dizine genesis yazıldı: %v", err)
	}
	if _, err := NewBlockchain(t.TempDir(), nil); !errors.Is(err, ErrNoGenesis) {
		t.Fatalf("nil parametre: %v", err)
	}
}
