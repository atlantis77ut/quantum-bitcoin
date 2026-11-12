// Package gpu Q-BTC başlığı için OpenCL 1.2 referans kazıcısıdır.
//
// Varsayılan derlemede yalnızca saplama vardır (ErrNoOpenCL); OpenCL desteği `-tags opencl`
// ile derlenir ve sistemde OpenCL ICD yükleyicisi ile bir sürücü gerektirir (Linux:
// ocl-icd-libopencl1 + üretici sürücüsü ya da pocl). Kazıcı core.MineBackend'i uygular;
// bulduğu her nonce core tarafından CPU'da yeniden doğrulanır.
//
// Çekirdek (sha256d.cl, gen/genkernel.py ile üretilir) tamamen açılmıştır ve durumu
// yazmaçlarda tutar; şablon başına sabit olan ikinci başlık bloğunun mesaj takvimini
// konakta bir kez hesaplar ve ilk bloğun W2–W15 sözcüklerini tur başına verir. Midstate
// yoktur: nonce ilk SHA-256 bloğundadır (core/header.go).
package gpu

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"

	"quantum-bitcoin/core"
)

//go:embed sha256d.cl
var kernelSource string

// KernelSHA256 kazıcının kullandığı çekirdek kaynağının (gpu/sha256d.cl) SHA-256'sıdır; ölçüm
// raporunda commit'in yanında yer alır.
func KernelSHA256() string {
	s := sha256.Sum256([]byte(kernelSource))
	return hex.EncodeToString(s[:])
}

// ErrNoOpenCL ikili OpenCL desteği olmadan derlendiğinde döner.
var ErrNoOpenCL = errors.New("OpenCL desteği olmadan derlendi; -tags opencl ile derleyin")

// Bir Scan çağrısının (tek çekirdek başlatmasının) nonce sınırları.
const (
	MinBatch = 1 << 10
	MaxBatch = 1 << 30
)

// Device türleri (CL_DEVICE_TYPE).
const (
	TypeGPU         = "GPU"
	TypeCPU         = "CPU"
	TypeAccelerator = "ACCELERATOR"
	TypeOther       = "OTHER"
)

// Device bir OpenCL aygıtıdır; Index -gpu-device bayrağına verilir. Okunamayan alanlar "?" olur.
type Device struct {
	Index           int    `json:"index"`
	Name            string `json:"name"`
	Type            string `json:"type"` // TypeGPU, TypeCPU, TypeAccelerator ya da TypeOther
	Platform        string `json:"platform"`
	PlatformVersion string `json:"platform_version"`
	DeviceVersion   string `json:"device_version"`
	Driver          string `json:"driver"`
}

// Backend kapatılabilen bir kazım arka ucudur.
type Backend interface {
	core.MineBackend
	Close() error
}
