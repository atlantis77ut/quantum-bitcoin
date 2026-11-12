package network

import (
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

// Eşe itme yayını olmadan da işlem havuz eşitlemesiyle gelir ve çekilen işlem yeniden
// yayılmaz.
func TestMempoolPullFromPeer(t *testing.T) {
	src := startNode(t, time.Hour)
	alice := fundKey(t, src.bc)
	tx := policyTx(t, src.ps.Mempool, alice, core.Coin)
	if _, err := src.ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}

	// Sunucu başlatılmaz: yayın döngüsü çalışmadığından yayın kuyruğu çekilen işlemi
	// tutardı.
	ps := NewP2PServer("127.0.0.1:0", newChain(t))
	stop := make(chan struct{})
	defer close(stop)
	if err := ps.syncFrom(src.addr, stop); err != nil {
		t.Fatal(err)
	}
	if n, err := ps.pullMempool(src.addr, stop); err != nil || n != 1 {
		t.Fatalf("çekme %d, %v", n, err)
	}
	if n, err := ps.pullMempool(src.addr, stop); err != nil || n != 0 {
		t.Fatalf("ikinci çekme %d, %v", n, err)
	}
	if got := ps.Mempool.Get([]string{tx.ID()}); len(got) != 1 {
		t.Fatal("çekilen işlem havuzda değil")
	}
	if len(ps.txRelayCh) != 0 {
		t.Fatal("çekilen işlem yeniden yayın kuyruğuna girdi")
	}
}

// gettxs yanıtı yalnızca istenen, yinelenmeyen işlemleri ve istenenden fazla olmayan sayıda
// işlemi içerebilir.
func TestCheckPulled(t *testing.T) {
	bc := newChain(t)
	alice := fundKey(t, bc)
	m := core.NewMempool(bc)
	a := policyTx(t, m, alice, core.Coin)
	b := policyTx(t, m, alice, 2*core.Coin) // farklı tutar: farklı kimlik
	req := []string{a.ID()}
	for _, c := range []struct {
		name string
		resp message
		ok   bool
	}{
		{"istenen", message{Type: msgTxs, Txs: []core.Transaction{a}}, true},
		{"boş", message{Type: msgTxs}, true},
		{"istenmeyen", message{Type: msgTxs, Txs: []core.Transaction{b}}, false},
		{"fazla", message{Type: msgTxs, Txs: []core.Transaction{a, a}}, false},
		{"yanlış tür", message{Type: msgBlock}, false},
	} {
		if _, err := checkPulled(&c.resp, req); (err == nil) != c.ok {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	if _, err := checkPulled(&message{Type: msgTxs, Txs: []core.Transaction{a, b}}, []string{a.ID(), a.ID()}); err == nil {
		t.Fatal("yinelenen istek kimliğiyle istenmeyen işlem kabul edildi")
	}
}

// Sunucu: sınırı aşan ya da geçersiz kimlikli gettxs yanıtsız kapanır; geçerli istek yalnızca
// havuzdaki işlemleri döndürür; envanter havuzu listeler.
func TestServeMempoolAndTxs(t *testing.T) {
	src := startNode(t, time.Hour)
	alice := fundKey(t, src.bc)
	tx := policyTx(t, src.ps.Mempool, alice, core.Coin)
	if _, err := src.ps.Mempool.Add(tx); err != nil {
		t.Fatal(err)
	}
	client := NewP2PServer("127.0.0.1:0", newChain(t))

	inv, err := client.request(src.addr, message{Type: msgGetMempool}, maxInvMessageBytes)
	if err != nil || inv.Type != msgInv || len(inv.TxIDs) != 1 || inv.TxIDs[0] != tx.ID() {
		t.Fatalf("envanter %+v, %v", inv, err)
	}
	unknown := strings.Repeat("0", 64)
	resp, err := client.request(src.addr, message{Type: msgGetTxs, TxIDs: []string{unknown, tx.ID()}}, maxPullMessageBytes)
	if err != nil || resp.Type != msgTxs || len(resp.Txs) != 1 || resp.Txs[0].ID() != tx.ID() {
		t.Fatalf("gettxs yanıtı %+v, %v", resp, err)
	}
	tooMany := make([]string, maxPullTxs+1)
	for i := range tooMany {
		tooMany[i] = unknown
	}
	for name, ids := range map[string][]string{"çok uzun": tooMany, "geçersiz": {"ZZ"}} {
		if _, err := client.request(src.addr, message{Type: msgGetTxs, TxIDs: ids}, maxPullMessageBytes); err == nil {
			t.Fatalf("%s gettxs yanıtlandı", name)
		}
	}
}
