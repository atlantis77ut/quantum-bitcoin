package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"quantum-bitcoin/network"
)

// Yayılım ölçümü raporu (docs/YAYILIM-OLCUM.md). Hashrate raporu gibi kamuya açıklanabilir:
// makine adı, kullanıcı adı, yol ya da adres içermez. Block bu makinede gerçek bileşenlerle
// ölçülür; Simulation'daki ağ değerleri seçilen bağlantı profilinin sonucudur, gerçek ağ ölçümü
// değildir.

const propReportSchema = 1

type propReport struct {
	Schema     int             `json:"schema"`
	TimeUTC    string          `json:"time_utc"`
	Commit     string          `json:"commit"`
	Modified   bool            `json:"modified"`
	GoVersion  string          `json:"go_version"`
	OS         string          `json:"os"`
	Arch       string          `json:"arch"`
	CPUs       int             `json:"cpus"`
	Block      propBlockReport `json:"block"`
	Simulation propSimReport   `json:"simulation"`
	Reproduce  string          `json:"reproduce"`
}

// propBlockReport ölçülen dolu bloktur: kablo boyutu, çözümleme ve taze zincirde tam doğrulama.
type propBlockReport struct {
	Txs       int       `json:"txs"`
	WireBytes int       `json:"wire_bytes"`
	DecodeMs  []float64 `json:"decode_ms"`
	AcceptMs  []float64 `json:"accept_ms"`
	DecodeMed float64   `json:"decode_ms_median"`
	AcceptMed float64   `json:"accept_ms_median"`
}

// propSimReport yayılım benzetimidir. Runs[i][k] i. ölçümde k. düğümün (Nodes sırasıyla)
// kazıcının kabulünden kendi kabulüne kadar geçen süresidir; ulaşmadıysa -1. Ortanca ve en çok
// yalnızca ulaşılan ölçümlerden; Reached kaç ölçümde ulaşıldığıdır.
type propSimReport struct {
	Topology   string      `json:"topology"`
	Hops       int         `json:"hops"`
	Mbit       float64     `json:"uplink_mbit"`
	LatencyMs  float64     `json:"one_way_latency_ms"`
	TimeoutSec float64     `json:"timeout_sec"`
	Delivery   string      `json:"delivery"`
	Nodes      []string    `json:"nodes"`
	Runs       [][]float64 `json:"runs_ms"`
	MedianMs   []float64   `json:"median_ms"`
	MaxMs      []float64   `json:"max_ms"`
	Reached    []int       `json:"reached"`
	WorstMedMs float64     `json:"slowest_node_median_ms"`
	// Takeovers ölçüm başına itme yuvası devralmalarıdır (bütün düğümler, nedene göre).
	Takeovers []network.PushTakeovers `json:"push_takeovers"`
}

func millis(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// propNodes benzetimde ölçülen düğümlerin adlarıdır (network.PropRun.Arrival sırası).
func propNodes(c network.PropConfig) []string {
	var out []string
	switch c.Topology {
	case network.TopologyLine:
		for i := 1; i <= c.Hops; i++ {
			out = append(out, fmt.Sprintf("atlama %d", i))
		}
	case network.TopologyStarLeaf:
		out = append(out, "merkez")
		for i := 2; i <= c.Hops; i++ {
			out = append(out, fmt.Sprintf("yaprak %d", i))
		}
	case network.TopologyStarHub:
		for i := 1; i <= c.Hops; i++ {
			out = append(out, fmt.Sprintf("yaprak %d", i))
		}
	}
	return out
}

// newPropReport ölçümleri rapora dönüştürür.
func newPropReport(c network.PropConfig, f *network.PropFixture, blocks []network.BlockTiming,
	runs []network.PropRun, nRuns int) propReport {
	commit, modified := vcsInfo()
	r := propReport{
		Schema: propReportSchema, TimeUTC: time.Now().UTC().Format(time.RFC3339),
		Commit: commit, Modified: modified, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(),
	}
	r.Block = propBlockReport{Txs: len(f.Block.Transactions) - 1, WireBytes: f.WireBytes}
	for _, b := range blocks {
		r.Block.DecodeMs = append(r.Block.DecodeMs, millis(b.Decode))
		r.Block.AcceptMs = append(r.Block.AcceptMs, millis(b.Accept))
	}
	if len(blocks) > 0 {
		r.Block.DecodeMed, _, _, _ = summarize(r.Block.DecodeMs)
		r.Block.AcceptMed, _, _, _ = summarize(r.Block.AcceptMs)
	}
	nodes := propNodes(c)
	s := propSimReport{
		Topology: c.Topology, Hops: c.Hops, Mbit: c.Mbit, LatencyMs: millis(c.Latency), TimeoutSec: c.Timeout.Seconds(),
		Delivery: "push", Nodes: nodes, MedianMs: make([]float64, len(nodes)), MaxMs: make([]float64, len(nodes)),
		Reached: make([]int, len(nodes)),
	}
	for k := range nodes {
		var got []float64
		for _, run := range runs {
			if k < len(run.Arrival) && run.Arrival[k] >= 0 {
				got = append(got, millis(run.Arrival[k]))
			}
		}
		s.Reached[k] = len(got)
		s.MedianMs[k], s.MaxMs[k] = -1, -1
		if len(got) > 0 {
			s.MedianMs[k], _, s.MaxMs[k], _ = summarize(got)
			s.WorstMedMs = max(s.WorstMedMs, s.MedianMs[k])
		}
	}
	for _, run := range runs {
		row := make([]float64, len(run.Arrival))
		for k, d := range run.Arrival {
			row[k] = -1
			if d >= 0 {
				row[k] = millis(d)
			}
		}
		s.Runs = append(s.Runs, row)
		s.Takeovers = append(s.Takeovers, run.Takeovers)
	}
	r.Simulation = s
	r.Reproduce = fmt.Sprintf("go build -o qbtc . && ./qbtc -bench-propagation -bench-prop-txs %d -bench-prop-hops %d"+
		" -bench-prop-topology %s -bench-prop-mbit %g -bench-prop-latency %s -bench-prop-timeout %s -bench-runs %d",
		c.Txs, c.Hops, c.Topology, c.Mbit, c.Latency, c.Timeout, nRuns)
	return r
}

// runPropBench blok ölçümünü ve yayılım benzetimini runs kez yapar, sonucu loglar ve out
// verilmişse JSON yazar; çıkış kodunu döndürür. Zincirler geçici bir dizinde kurulur ve silinir;
// çalışan düğümün veri dizinine dokunulmaz.
func runPropBench(c network.PropConfig, runs int, out string) int {
	if runs < 1 || runs > maxBenchRuns {
		slog.Error("ölçüm tekrarı 1 ile sınır arasında olmalı", "bench-runs", runs, "en_cok", maxBenchRuns)
		return 2
	}
	dir, err := os.MkdirTemp("", "qbtc-yayilim-")
	if err != nil {
		slog.Error("geçici dizin açılamadı", "err", err)
		return 1
	}
	defer os.RemoveAll(dir)
	c.Dir = dir
	if err := c.Validate(); err != nil {
		slog.Error("yayılım ölçümü başlatılamadı", "err", err)
		return 2
	}
	// Zincir ve düğüm logları ölçüm boyunca yalnızca uyarıları gösterir (ör. itme hatası).
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	blocks, sims, f, err := measureProp(c, runs)
	slog.SetDefault(prev)
	if err != nil {
		slog.Error("yayılım ölçülemedi", "err", err)
		return 1
	}
	rep := newPropReport(c, f, blocks, sims, runs)
	s := rep.Simulation
	slog.Info("blok ölçümü (bu makine, gerçek bileşenler)", "islem", rep.Block.Txs, "kablo_bayt", rep.Block.WireBytes,
		"cozumleme_ms_ortanca", rep.Block.DecodeMed, "dogrulama_ms_ortanca", rep.Block.AcceptMed, "olcum", runs)
	slog.Info("yayılım benzetimi (ağ değerleri seçilen profilden)", "topoloji", s.Topology, "atlama", s.Hops,
		"mbit", s.Mbit, "tek_yon_gecikme_ms", s.LatencyMs, "dugumler", s.Nodes, "ortanca_ms", s.MedianMs,
		"en_cok_ms", s.MaxMs, "ulasilan", s.Reached, "en_gec_dugum_ortanca_ms", s.WorstMedMs, "olcum", runs)
	for k, n := range s.Reached {
		if n < runs {
			slog.Warn("blok bazı ölçümlerde düğüme zaman aşımı içinde ulaşmadı", "dugum", s.Nodes[k],
				"ulasilan", n, "olcum", runs, "zaman_asimi", c.Timeout)
		}
	}
	if out != "" {
		if err := writeJSONFile(out, rep); err != nil {
			slog.Error("rapor yazılamadı", "err", err)
			return 1
		}
	}
	return 0
}

// measureProp dolu bloğu bir kez kurar, sonra blok ölçümünü ve benzetimi runs kez yapar. Her
// ölçüm kendi dizininde taze zincirlerle çalışır ve dizini siler.
func measureProp(c network.PropConfig, runs int) ([]network.BlockTiming, []network.PropRun, *network.PropFixture, error) {
	f, err := network.NewPropFixture(c.Dir, c.Txs)
	if err != nil {
		return nil, nil, nil, err
	}
	var blocks []network.BlockTiming
	var sims []network.PropRun
	for i := 0; i < runs; i++ {
		d := filepath.Join(c.Dir, fmt.Sprintf("block%d", i))
		bt, err := f.BenchBlock(d)
		os.RemoveAll(d)
		if err != nil {
			return nil, nil, nil, err
		}
		blocks = append(blocks, bt)
	}
	for i := 0; i < runs; i++ {
		rc := c
		rc.Dir = filepath.Join(c.Dir, fmt.Sprintf("sim%d", i))
		r, err := f.BenchPropagation(rc)
		os.RemoveAll(rc.Dir)
		if err != nil {
			return nil, nil, nil, err
		}
		sims = append(sims, r)
	}
	return blocks, sims, f, nil
}
