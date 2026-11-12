// Package gpu Q-BTC başlığı için OpenCL 1.2 referans kazıcısıdır.
//
// Varsayılan derlemede yalnızca saplama vardır (ErrNoOpenCL); OpenCL desteği `-tags opencl`
// ile derlenir ve sistemde OpenCL ICD yükleyicisi ile bir sürücü gerektirir (Linux:
// ocl-icd-libopencl1 + üretici sürücüsü ya da pocl). Kazıcı core.MineBackend'i uygular;
// bulduğu her nonce core tarafından CPU'da yeniden doğrulanır.
//
// Çekirdek (sha256d.cl) şablon başına sabit olan ikinci başlık bloğunun mesaj takvimini
// konakta bir kez hesaplar ve ilk bloğun W2–W15 sözcüklerini tur başına verir. Midstate
// yoktur: nonce ilk SHA-256 bloğundadır (core/header.go).
package gpu

import (
	"errors"

	"quantum-bitcoin/core"
)

// ErrNoOpenCL ikili OpenCL desteği olmadan derlendiğinde döner.
var ErrNoOpenCL = errors.New("OpenCL desteği olmadan derlendi; -tags opencl ile derleyin")

// Bir Scan çağrısının (tek çekirdek başlatmasının) nonce sınırları.
const (
	MinBatch = 1 << 10
	MaxBatch = 1 << 30
)

// Device bir OpenCL aygıtıdır; Index -gpu-device bayrağına verilir.
type Device struct {
	Index int
	Name  string
}

// Backend kapatılabilen bir kazım arka ucudur.
type Backend interface {
	core.MineBackend
	Close() error
}
