package network

import (
	"net"
	"testing"
	"time"
)

// Ucun işini ancak oturum penceresinden derinde geçen eş zinciri ön-senkronizasyonla
// benimsenir (ağ katmanı uçtan uca: eş başına durum, parçalı yeniden indirme, olağan
// senkronizasyonla kalan).
func TestP2PPresyncAdoptsDeepBranch(t *testing.T) {
	src := startNode(t, time.Hour)
	mineN(t, src.bc, 30)

	bc := newChain(t)
	mineN(t, bc, 25)
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ps.syncWindow = 8 // pencere 8 başlık: 25 blokluk ucu ilk pencere geçemez
	ps.AddPeer(src.addr)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })

	want := src.bc.TipHash()
	deadline := time.Now().Add(20 * time.Second)
	for bc.TipHash() != want {
		if time.Now().After(deadline) {
			t.Fatalf("derin dal benimsenmedi: uç %s", bc.TipHash())
		}
		ps.TriggerSync() // ön-senkronizasyon son başlıkta biter; kalan iş sonraki turda
		time.Sleep(50 * time.Millisecond)
	}
}
