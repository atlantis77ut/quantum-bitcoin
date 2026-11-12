package network

import (
	"math/big"
	"net"
	"testing"
	"time"

	"quantum-bitcoin/core"
)

func listenNode(t *testing.T, p *core.Params) (*core.Blockchain, *P2PServer, string) {
	t.Helper()
	bc, err := core.NewBlockchain(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bc.Close() })
	ps := NewP2PServer("127.0.0.1:0", bc)
	ps.syncEvery = time.Hour
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go ps.Serve(ln)
	t.Cleanup(func() { ln.Close() })
	return bc, ps, ln.Addr().String()
}

// Ucu en az zincir işinin altındaki düğüm duyurulan blokları bırakır ve kazmaz. Aynı yayını
// alan eşiksiz bir kontrol düğümü bloğu kabul eder: blok gerçekten ulaşmış ve kapıda kalmıştır.
func TestMinChainWorkRelayAndMiner(t *testing.T) {
	p := *core.RegtestParams
	p.MinChainWork = new(big.Int).Lsh(big.NewInt(1), 200) // hiçbir test zincirinin ulaşamayacağı eşik
	bc, ps, addr := listenNode(t, &p)
	control, _, controlAddr := listenNode(t, core.RegtestParams)
	genesis := bc.TipHash()

	src := startNode(t, time.Hour, controlAddr, addr)
	mineN(t, src.bc, 1)
	b, err := src.bc.GetLatestBlock()
	if err != nil {
		t.Fatal(err)
	}
	src.ps.BroadcastBlock(b)
	waitTip(t, control, b.Hash) // yayın kontrol düğümüne ulaştı

	old := minWorkWait
	minWorkWait = 10 * time.Millisecond
	t.Cleanup(func() { minWorkWait = old })
	m, err := NewMiner(ps, minerAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	runMiner(t, m)

	time.Sleep(300 * time.Millisecond)
	if bc.TipHash() != genesis {
		t.Fatal("en az zincir işinin altındaki blok kabul edildi")
	}
	if m.mined.Load() != 0 {
		t.Fatalf("senkronize olmayan düğüm %d blok kazdı", m.mined.Load())
	}
}
