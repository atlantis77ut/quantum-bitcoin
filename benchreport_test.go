package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quantum-bitcoin/core"
	"quantum-bitcoin/gpu"
)

// GPU olmayan aygıt yalnızca açık izinle ölçülür; olmayan aygıt her zaman reddedilir.
func TestBenchDevice(t *testing.T) {
	devs := []gpu.Device{{Index: 0, Name: "pocl", Type: gpu.TypeCPU}, {Index: 1, Name: "kart", Type: gpu.TypeGPU}}
	if _, err := benchDevice(devs, 0, false); !errors.Is(err, errBenchDevice) {
		t.Fatalf("CPU aygıtı izinsiz kabul edildi: %v", err)
	}
	if d, err := benchDevice(devs, 0, true); err != nil || d.Name != "pocl" {
		t.Fatalf("izinle CPU aygıtı: %+v %v", d, err)
	}
	if d, err := benchDevice(devs, 1, false); err != nil || d.Type != gpu.TypeGPU {
		t.Fatalf("GPU aygıtı: %+v %v", d, err)
	}
	for _, idx := range []int{-1, 2} {
		if _, err := benchDevice(devs, idx, true); !errors.Is(err, errBenchDevice) {
			t.Fatalf("aygıt %d kabul edildi: %v", idx, err)
		}
	}
}

// Birden çok ölçümde ilk (ısınma) ölçüm atılır; tek ölçümde ısınma yoktur; hatalı ölçüm döner.
func TestRunBench(t *testing.T) {
	calls := 0
	measure := func() (float64, error) { calls++; return float64(calls), nil }
	rates, warmup, err := runBench(measure, 3)
	if err != nil || warmup != 1 || len(rates) != 3 || rates[0] != 2 || calls != 4 {
		t.Fatalf("ölçümler %v, ısınma %d, çağrı %d, %v", rates, warmup, calls, err)
	}
	calls = 0
	if rates, warmup, err := runBench(measure, 1); err != nil || warmup != 0 || len(rates) != 1 || calls != 1 {
		t.Fatalf("tek ölçüm %v %d %v", rates, warmup, err)
	}
	for _, n := range []int{0, maxBenchRuns + 1} {
		if _, _, err := runBench(measure, n); err == nil {
			t.Fatalf("%d ölçüm kabul edildi", n)
		}
	}
	zero := func() (float64, error) { return 0, nil }
	if _, _, err := runBench(zero, 1); err == nil {
		t.Fatal("sıfır hashrate kabul edildi")
	}
}

func TestSummarize(t *testing.T) {
	if m, lo, hi, s := summarize([]float64{30, 10, 20}); m != 20 || lo != 10 || hi != 30 || s != 100 {
		t.Fatalf("tek sayıda: %v %v %v %v", m, lo, hi, s)
	}
	if m, _, _, _ := summarize([]float64{4, 1, 3, 2}); m != 2.5 {
		t.Fatalf("çift sayıda ortanca %v", m)
	}
}

// Rapor ortancayı, lansman önerisini ve yeniden üretme komutunu (sabit tur boyuyla) taşır.
func TestBenchReport(t *testing.T) {
	dev := gpu.Device{Index: 1, Name: "kart", Type: gpu.TypeGPU, Driver: "1.0"}
	rates := []float64{3e9, 2.9e9, 3.1e9}
	rep, err := newBenchReport(core.TestnetParams, &dev, false, 4, 1<<24, time.Minute, rates, 1)
	if err != nil {
		t.Fatal(err)
	}
	start, floor, _ := core.LaunchTargets(3e9, core.MainnetParams.TargetSpacing)
	if rep.Backend != "gpu" || rep.Median != 3e9 || rep.Device == nil || rep.Device.Driver != "1.0" ||
		rep.RoundSize != 1<<24 || rep.WarmupRuns != 1 || len(rep.KernelSHA256) != 64 ||
		rep.StartBits != fmt.Sprintf("0x%08x", start) || rep.FloorBits != fmt.Sprintf("0x%08x", floor) || rep.Threads != 0 {
		t.Fatalf("rapor %+v", rep)
	}
	if !strings.Contains(rep.Reproduce, "-gpu-device 1 -gpu-batch 16777216") ||
		!strings.Contains(rep.Reproduce, "-bench-hashrate 1m0s -bench-runs 3") ||
		strings.Contains(rep.Reproduce, "non-gpu") {
		t.Fatalf("yeniden üretme komutu %q", rep.Reproduce)
	}

	path := filepath.Join(t.TempDir(), "olcum.json")
	if err := writeBenchReport(path, rep); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]interface{}
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "commit", "device", "runs_hash_per_sec", "median_hash_per_sec",
		"mainnet_start_bits", "mainnet_floor_bits", "reproduce", "non_gpu_allowed", "kernel_sha256"} {
		if _, ok := back[k]; !ok {
			t.Fatalf("raporda %s yok", k)
		}
	}
	if host, _ := os.Hostname(); len(host) >= 4 && strings.Contains(string(data), host) {
		t.Fatal("rapor makine adını içeriyor")
	}

	cpu, err := newBenchReport(core.TestnetParams, nil, false, 8, 0, time.Second, []float64{1e6}, 0)
	if err != nil || cpu.Backend != "cpu" || cpu.Device != nil || cpu.Threads != 8 || cpu.KernelSHA256 != "" ||
		!strings.Contains(cpu.Reproduce, "-mine-threads 8") || strings.Contains(cpu.Reproduce, "-mine-gpu") {
		t.Fatalf("CPU raporu %+v %v", cpu, err)
	}
}
