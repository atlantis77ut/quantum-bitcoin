package core

import (
	"errors"
	"sync"
	"testing"
)

// MerkleProofFromIDs kimliklerden MerkleProof ile aynı kanıtı üretir; kanıt köke bağımsız
// doğrulayıcıyla (merkleFold) ulaşır. Tek, çift ve ikinin kuvveti boyutlar denenir.
func TestMerkleProofFromIDs(t *testing.T) {
	for n := 1; n <= 9; n++ {
		txs := make([]Transaction, n)
		for i := range txs {
			txs[i] = Transaction{Receiver: "r", Amount: int64(i + 1), Nonce: uint64(i + 1), ExpiryHeight: 1}
		}
		ids := TxIDs(txs)
		root := MerkleRoot(txs)
		for i := range txs {
			if ids[i] != txs[i].IDBytes() {
				t.Fatalf("n=%d: %d. kimlik IDBytes ile tutmuyor", n, i)
			}
			got, err := MerkleProofFromIDs(ids, i)
			if err != nil || !VerifyMerkleProof(ids[i], i, n, got, root) {
				t.Fatalf("n=%d, i=%d: kanıt köke ulaşmıyor: %v", n, i, err)
			}
			want, _ := MerkleProof(txs, i)
			if len(got) != len(want) {
				t.Fatalf("n=%d, i=%d: kanıt uzunluğu %d, beklenen %d", n, i, len(got), len(want))
			}
			for j := range got {
				if got[j] != want[j] {
					t.Fatalf("n=%d, i=%d: %d. kardeş farklı", n, i, j)
				}
			}
		}
		for _, bad := range []int{-1, n} {
			if _, err := MerkleProofFromIDs(ids, bad); !errors.Is(err, ErrMerkleIndex) {
				t.Fatalf("n=%d, sıra %d: %v", n, bad, err)
			}
		}
	}
}

// BlockTxIDs önbellekteki blokta kimlikleri bir kez hesaplayıp aynı dilimi döndürür (eşzamanlı
// çağrılarda da); önbellek dışındaki işaretçi için her seferinde yeniden hesaplar.
func TestBlockTxIDsCache(t *testing.T) {
	bc := newTestChain(t)
	alice, bob := newKey(t), newKey(t)
	fund(t, bc, alice, 1)
	m := NewMempool(bc)
	for nonce := uint64(1); nonce <= 3; nonce++ {
		mustAdd(t, m, mpTx(t, bc, alice, bob.Address, Coin, MempoolMinFee, nonce))
	}
	blk := mineFromPool(t, bc, m)
	if len(blk.Transactions) != 4 {
		t.Fatalf("blokta %d işlem", len(blk.Transactions))
	}

	b, _, err := bc.Reads(nil).MainBlockByHash(blk.Hash)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([][][32]byte, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = bc.BlockTxIDs(b)
		}()
	}
	wg.Wait()
	for i, ids := range got {
		if len(ids) != len(b.Transactions) || &ids[0] != &got[0][0] {
			t.Fatalf("%d. çağrı başka dilim döndürdü (önbellekte bir kez hesaplanmadı)", i)
		}
	}
	for i := range b.Transactions {
		if got[0][i] != b.Transactions[i].IDBytes() {
			t.Fatalf("%d. kimlik IDBytes ile tutmuyor", i)
		}
	}

	other := *b // aynı özet, önbellekteki işaretçi değil
	ids := bc.BlockTxIDs(&other)
	if &ids[0] == &got[0][0] || len(ids) != len(got[0]) {
		t.Fatal("önbellek dışı işaretçi önbellekteki kimlikleri aldı")
	}
	for i := range ids {
		if ids[i] != got[0][i] {
			t.Fatalf("%d. kimlik farklı", i)
		}
	}
	if again := bc.BlockTxIDs(&other); &again[0] == &ids[0] {
		t.Fatal("önbellek dışı işaretçinin kimlikleri saklandı")
	}
}
