package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"quantum-bitcoin/crypto"
)

// Bağımsız Python uygulamasıyla (Go kodundan ayrı yazılmış) üretilmiş bilinen-yanıt
// vektörleri. Params'taki sabitlerden ayrı tutulur: biri değişirse test kırmızıya döner.
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
		txid:     "682fafb80b27cd1a85943b5696646041a8a2755eafa8a11cadce9b88bf143ec8",
		merkle:   "d00f872bbd9209465309226f74f0dcabd89b55f9a0ab8684457e16e108d219e4",
		nonce:    10,
		hash:     "0c361f2df64b66a48865cda689233554a7faa517a584dd034ce882a987d20eef",
	},
	{
		p:        TestnetParams,
		receiver: "QBTCd3a378937e0e43e9baf18c21b24dd539e398f1bf",
		txid:     "ee2a1aeeebaa6321ca9da0a84065e0e1445301b400855bebc6ec896e9d6bb478",
		merkle:   "564a54cc73968328cd201c00030c47e68aac18f42c81f8de96b8a87929e09c70",
		nonce:    19141,
		hash:     "0000f47b1643fb44236004eadf3a39b5e1999ccfa4f6cb941f14975ee07b1be0",
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

// katTx Python vektöründeki sabit baytlı işlemdir (imzası geçerli olmak zorunda değil).
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
	if id := tx.ID(); id != "6e2d6d43c9f199651188a98063ca1730567b503a67c273691c6adbfc68129445" {
		t.Fatalf("işlem kimliği %s", id)
	}
	want := map[*Params]string{
		RegtestParams: "ccbc6fbacceccc15bd08cdf514aa55fbf75d5ec1db8389e3ffd80eb1fe17855a",
		TestnetParams: "60a95f866d91661e7d8c7fcba7ede1275e62a71de4836ca47424b532bf2fa1f7",
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
	for _, old := range []string{"QBTC-TX-v1|qbtc-mainnet", "QBTC-TX-v2|qbtc-mainnet", dt3} {
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
