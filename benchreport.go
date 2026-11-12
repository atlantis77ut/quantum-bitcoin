package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/gpu"
)

// Hashrate ölçüm raporu (docs/MAINNET-ONCESI.md madde 6a, docs/GPU-OLCUM.md). Rapor kamuya
// açıklanabilir: makine adı, kullanıcı adı, yol ya da adres içermez.

const (
	benchReportSchema = 1
	maxBenchRuns      = 100
)

type benchReport struct {
	Schema        int         `json:"schema"`
	TimeUTC       string      `json:"time_utc"`
	Commit        string      `json:"commit"`
	Modified      bool        `json:"modified"` // derlenen ağaçta commit edilmemiş değişiklik vardı
	GoVersion     string      `json:"go_version"`
	OS            string      `json:"os"`
	Arch          string      `json:"arch"`
	KernelSHA256  string      `json:"kernel_sha256,omitempty"`
	Network       string      `json:"network"`
	Backend       string      `json:"backend"` // "gpu" ya da "cpu"
	Device        *gpu.Device `json:"device,omitempty"`
	NonGPUAllowed bool        `json:"non_gpu_allowed"` // true ise sonuç GPU hashrate'i değildir
	Threads       int         `json:"threads,omitempty"`
	RoundSize     uint64      `json:"round_size,omitempty"`
	RunSeconds    float64     `json:"run_seconds"`
	WarmupRuns    int         `json:"warmup_runs"`
	Runs          []float64   `json:"runs_hash_per_sec"`
	Median        float64     `json:"median_hash_per_sec"`
	Min           float64     `json:"min_hash_per_sec"`
	Max           float64     `json:"max_hash_per_sec"`
	SpreadPct     float64     `json:"spread_pct"` // (en yüksek − en düşük) / ortanca · 100
	NetworkBits   string      `json:"network_bits_for_median,omitempty"`
	StartBits     string      `json:"mainnet_start_bits"`
	FloorBits     string      `json:"mainnet_floor_bits"`
	Reproduce     string      `json:"reproduce"`
}

var errBenchDevice = errors.New("ölçüm aygıtı uygun değil")

// benchDevice ölçülecek OpenCL aygıtını bulur. GPU olmayan aygıt (ör. CI'daki pocl) yalnızca
// allowNonGPU ile kabul edilir: aksi hâlde CPU sonucu GPU hashrate'i diye yayımlanabilir.
func benchDevice(devs []gpu.Device, idx int, allowNonGPU bool) (gpu.Device, error) {
	if idx < 0 || idx >= len(devs) {
		return gpu.Device{}, fmt.Errorf("%w: aygıt %d yok (%d aygıt)", errBenchDevice, idx, len(devs))
	}
	d := devs[idx]
	if d.Type != gpu.TypeGPU && !allowNonGPU {
		return gpu.Device{}, fmt.Errorf("%w: aygıt %d (%s) türü %s, GPU değil", errBenchDevice, idx, d.Name, d.Type)
	}
	return d, nil
}

// runBench runs > 1 ise önce bir ısınma ölçümü yapıp atar, sonra runs ölçüm döndürür.
func runBench(measure func() (float64, error), runs int) (rates []float64, warmup int, err error) {
	if runs < 1 || runs > maxBenchRuns {
		return nil, 0, fmt.Errorf("ölçüm tekrarı 1 ile %d arasında olmalı: %d", maxBenchRuns, runs)
	}
	if runs > 1 {
		if _, err := measure(); err != nil {
			return nil, 0, err
		}
		warmup = 1
	}
	for i := 0; i < runs; i++ {
		r, err := measure()
		if err != nil {
			return nil, 0, err
		}
		if !(r > 0) {
			return nil, 0, fmt.Errorf("geçersiz ölçüm: %v", r)
		}
		rates = append(rates, r)
	}
	return rates, warmup, nil
}

// summarize ortanca, en düşük, en yüksek ve yüzde yayılımı döndürür (rates boş olmamalı).
func summarize(rates []float64) (median, lo, hi, spreadPct float64) {
	s := append([]float64(nil), rates...)
	sort.Float64s(s)
	n := len(s)
	median = s[n/2]
	if n%2 == 0 {
		median = (s[n/2-1] + s[n/2]) / 2
	}
	lo, hi = s[0], s[n-1]
	return median, lo, hi, (hi - lo) / median * 100
}

// vcsInfo derlemenin commit'ini ve kirli ağaç bayrağını okur (go build git deposunda).
func vcsInfo() (commit string, modified bool) {
	commit = "bilinmiyor"
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return commit, false
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	return commit, modified
}

// newBenchReport ölçümleri ve ortam bilgisini rapora dönüştürür; dev nil ise CPU ölçümüdür.
func newBenchReport(params *core.Params, dev *gpu.Device, allowNonGPU bool, threads int, roundSize uint64,
	d time.Duration, rates []float64, warmup int) (benchReport, error) {
	if len(rates) == 0 {
		return benchReport{}, errors.New("ölçüm yok")
	}
	med, lo, hi, spread := summarize(rates)
	start, floor, err := core.LaunchTargets(med, core.MainnetParams.TargetSpacing)
	if err != nil {
		return benchReport{}, err
	}
	commit, modified := vcsInfo()
	r := benchReport{
		Schema: benchReportSchema, TimeUTC: time.Now().UTC().Format(time.RFC3339),
		Commit: commit, Modified: modified, GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Network: params.Name, RunSeconds: d.Seconds(), WarmupRuns: warmup, Runs: rates,
		Median: med, Min: lo, Max: hi, SpreadPct: spread,
		StartBits: fmt.Sprintf("0x%08x", start), FloorBits: fmt.Sprintf("0x%08x", floor),
	}
	if bits, err := core.BitsForHashrate(med, params.TargetSpacing, params.PowLimitBits); err == nil {
		r.NetworkBits = fmt.Sprintf("0x%08x", bits)
	}
	cmd := fmt.Sprintf("qbtc -network %s -bench-hashrate %s -bench-runs %d", params.Name, d, len(rates))
	if dev != nil {
		r.Backend, r.Device, r.NonGPUAllowed, r.RoundSize = "gpu", dev, allowNonGPU, roundSize
		r.KernelSHA256 = gpu.KernelSHA256()
		cmd += fmt.Sprintf(" -mine-gpu -gpu-device %d -gpu-batch %d", dev.Index, roundSize)
		if allowNonGPU {
			cmd += " -bench-allow-non-gpu"
		}
	} else {
		r.Backend, r.Threads = "cpu", threads
		cmd += fmt.Sprintf(" -mine-threads %d", threads)
	}
	r.Reproduce = "go build -tags opencl -o qbtc . && ./" + cmd
	return r, nil
}

// writeBenchReport raporu girintili JSON olarak yazar.
func writeBenchReport(path string, r benchReport) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
