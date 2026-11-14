package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/network"
)

// Düğüm adları benzetimin ölçüm sırasıyla aynıdır.
func TestPropNodes(t *testing.T) {
	cases := []struct {
		topo string
		want string
	}{
		{network.TopologyLine, "atlama 1,atlama 2,atlama 3"},
		{network.TopologyStarLeaf, "merkez,yaprak 2,yaprak 3"},
		{network.TopologyStarHub, "yaprak 1,yaprak 2,yaprak 3"},
	}
	for _, c := range cases {
		got := strings.Join(propNodes(network.PropConfig{Topology: c.topo, Hops: 3}), ",")
		if got != c.want {
			t.Fatalf("%s: %q", c.topo, got)
		}
	}
}

// Ortanca ve en çok yalnızca ulaşılan ölçümlerden; hiç ulaşılmayan düğüm -1 ve uyarı sayısıyla.
func TestNewPropReport(t *testing.T) {
	c := network.PropConfig{Txs: 2, Hops: 2, Topology: network.TopologyLine, Mbit: 20, Latency: 50 * time.Millisecond,
		Timeout: time.Minute}
	f := &network.PropFixture{Block: &core.Block{Transactions: make([]core.Transaction, 3)}, WireBytes: 1234}
	blocks := []network.BlockTiming{{Decode: time.Millisecond, Accept: 10 * time.Millisecond},
		{Decode: 3 * time.Millisecond, Accept: 30 * time.Millisecond}}
	runs := []network.PropRun{
		{Arrival: []time.Duration{100 * time.Millisecond, -1}},
		{Arrival: []time.Duration{300 * time.Millisecond, -1}},
		{Arrival: []time.Duration{200 * time.Millisecond, -1}},
	}
	r := newPropReport(c, f, blocks, runs, 3)
	if r.Block.Txs != 2 || r.Block.WireBytes != 1234 || r.Block.AcceptMed != 20 || r.Block.DecodeMed != 2 {
		t.Fatalf("blok raporu %+v", r.Block)
	}
	s := r.Simulation
	if s.MedianMs[0] != 200 || s.MaxMs[0] != 300 || s.Reached[0] != 3 {
		t.Fatalf("ilk düğüm %v %v %v", s.MedianMs, s.MaxMs, s.Reached)
	}
	if s.MedianMs[1] != -1 || s.MaxMs[1] != -1 || s.Reached[1] != 0 || s.WorstMedMs != 200 {
		t.Fatalf("ulaşılmayan düğüm %v %v %v %v", s.MedianMs, s.MaxMs, s.Reached, s.WorstMedMs)
	}
	if len(s.Runs) != 3 || s.Runs[0][1] != -1 || s.Runs[1][0] != 300 || s.LatencyMs != 50 {
		t.Fatalf("ölçümler %v, gecikme %v", s.Runs, s.LatencyMs)
	}
	if !strings.Contains(r.Reproduce, "-bench-prop-latency 50ms") || !strings.Contains(r.Reproduce, "-bench-runs 3") {
		t.Fatalf("yeniden üretme komutu %q", r.Reproduce)
	}
	path := filepath.Join(t.TempDir(), "yayilim.json")
	if err := writeJSONFile(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back propReport
	if err := json.Unmarshal(data, &back); err != nil || back.Simulation.Delivery != "push" {
		t.Fatalf("rapor geri okunamadı: %v", err)
	}
}

// Geçersiz tekrar ve ayar ölçüm başlamadan reddedilir.
func TestRunPropBenchRejects(t *testing.T) {
	ok := network.PropConfig{Txs: 1, Hops: 1, Topology: network.TopologyLine, Mbit: 10, Timeout: time.Second}
	if code := runPropBench(ok, 0, ""); code != 2 {
		t.Fatalf("0 tekrar: çıkış %d", code)
	}
	bad := ok
	bad.Topology = "ring"
	if code := runPropBench(bad, 1, ""); code != 2 {
		t.Fatalf("bozuk topoloji: çıkış %d", code)
	}
}
