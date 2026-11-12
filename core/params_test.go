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
		txid:     "54f085cebb3fa821023f53b81ae03b730da4c70f3e95231ee827fe3b16989902",
		merkle:   "841387cd49373b33bccb11744eb190571afcf9981cd24c5976f2552c152ebd28",
		nonce:    6,
		hash:     "02f739de53b008231b18af8ef6c028edf6f6f80e560895994577bfbdf9986157",
	},
	{
		p:        TestnetParams,
		receiver: "QBTCd3a378937e0e43e9baf18c21b24dd539e398f1bf",
		txid:     "bafcc5b0d453c1cc5a1dcfe347e8143930231ae19e09e2f9fe6d472537608978",
		merkle:   "c1482363f3671350bc86ca85065248afbd8c2be5e22093565c357845aee6ea88",
		nonce:    129686,
		hash:     "00004fb2d4e92c6947ed1d8b9c039fd4ea0d23f2dd48babeeded9082f7e6b0ae",
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
		{MainnetParams, 4},
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
		Sender:    "QBTC" + "1111111111111111111111111111111111111111",
		Receiver:  "QBTC" + "2222222222222222222222222222222222222222",
		Amount:    123456789,
		Fee:       1000,
		Timestamp: 1700000000,
		Nonce:     7,
		PublicKey: pub,
		Signature: sig,
	}
}

func TestTransactionKAT(t *testing.T) {
	tx := katTx()
	if id := tx.ID(); id != "e46583615df3337b28a3270c9b2111d0ff7e7a177044186401caaa28f343925e" {
		t.Fatalf("işlem kimliği %s", id)
	}
	want := map[*Params]string{
		RegtestParams: "649a103137cb25d30faae55a48f3b326cd5011435559322e4a37e8f6e19f5fb1",
		TestnetParams: "2dcd83e9241e9b5c3bbfae0185efe05cee7dd5261d48f6b4de5e26e6ee69ce93",
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
	tx := Transaction{Receiver: bob.Address, Amount: Coin, Fee: 7, Timestamp: 1, Nonce: 1}
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

	for _, old := range []string{"QBTC-TX-v1|qbtc-mainnet", "QBTC-TX-v2|qbtc-mainnet"} {
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
	tx := Transaction{Receiver: bob.Address, Amount: Coin, Timestamp: 1, Nonce: 1}
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
