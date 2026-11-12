//go:build opencl

package gpu

/*
#cgo CFLAGS: -DCL_TARGET_OPENCL_VERSION=120
#cgo !darwin LDFLAGS: -lOpenCL
#cgo darwin LDFLAGS: -framework OpenCL
#ifdef __APPLE__
#include <OpenCL/opencl.h>
#else
#include <CL/cl.h>
#endif
#include <stdlib.h>
#include <string.h>

#define QB_MAX_PLAT 16
#define QB_MAX_DEV 64
#define QB_OUT_WORDS 11

// qb_device platformlar boyunca düz sıralanmış idx'inci aygıtı bulur.
static cl_int qb_device(int idx, cl_device_id *dev) {
	cl_platform_id plats[QB_MAX_PLAT];
	cl_uint np = 0;
	cl_int err = clGetPlatformIDs(QB_MAX_PLAT, plats, &np);
	if (err != CL_SUCCESS) return err;
	if (np > QB_MAX_PLAT) np = QB_MAX_PLAT;
	int seen = 0;
	for (cl_uint p = 0; p < np; p++) {
		cl_device_id devs[QB_MAX_DEV];
		cl_uint nd = 0;
		if (clGetDeviceIDs(plats[p], CL_DEVICE_TYPE_ALL, QB_MAX_DEV, devs, &nd) != CL_SUCCESS) continue;
		if (nd > QB_MAX_DEV) nd = QB_MAX_DEV;
		if (idx >= seen && idx < seen + (int)nd) {
			*dev = devs[idx - seen];
			return CL_SUCCESS;
		}
		seen += (int)nd;
	}
	return CL_DEVICE_NOT_FOUND;
}

// qb_device_count aygıt sayısını ya da negatif OpenCL hata kodunu döndürür.
static int qb_device_count(void) {
	cl_platform_id plats[QB_MAX_PLAT];
	cl_uint np = 0;
	cl_int err = clGetPlatformIDs(QB_MAX_PLAT, plats, &np);
	if (err != CL_SUCCESS) return err < 0 ? err : -1;
	if (np > QB_MAX_PLAT) np = QB_MAX_PLAT;
	int n = 0;
	for (cl_uint p = 0; p < np; p++) {
		cl_uint nd = 0;
		if (clGetDeviceIDs(plats[p], CL_DEVICE_TYPE_ALL, 0, NULL, &nd) != CL_SUCCESS) continue;
		if (nd > QB_MAX_DEV) nd = QB_MAX_DEV;
		n += (int)nd;
	}
	return n;
}

static cl_int qb_device_name(int idx, char *name, size_t n) {
	cl_device_id d;
	cl_int err = qb_device(idx, &d);
	if (err != CL_SUCCESS) return err;
	memset(name, 0, n);
	size_t need = 0;
	err = clGetDeviceInfo(d, CL_DEVICE_NAME, 0, NULL, &need);
	if (err != CL_SUCCESS) return err;
	if (need >= n) return CL_INVALID_VALUE;
	return clGetDeviceInfo(d, CL_DEVICE_NAME, n - 1, name, NULL);
}

typedef struct {
	cl_context ctx;
	cl_command_queue q;
	cl_program prog;
	cl_kernel k;
	cl_mem w1, w2, tgt, out;
} qb_gpu;

static void qb_close(qb_gpu *g) {
	if (g->out) clReleaseMemObject(g->out);
	if (g->tgt) clReleaseMemObject(g->tgt);
	if (g->w2) clReleaseMemObject(g->w2);
	if (g->w1) clReleaseMemObject(g->w1);
	if (g->k) clReleaseKernel(g->k);
	if (g->prog) clReleaseProgram(g->prog);
	if (g->q) clReleaseCommandQueue(g->q);
	if (g->ctx) clReleaseContext(g->ctx);
	memset(g, 0, sizeof *g);
}

// qb_open aygıtı açar ve çekirdeği derler; derleme hatasında günlük log'a (NUL ile biten,
// gerekirse kesilmiş) yazılır. Her hata yolunda o ana kadar alınan kaynaklar bırakılır.
static cl_int qb_open(int idx, const char *src, size_t srclen, qb_gpu *g, char *log, size_t logn) {
	memset(g, 0, sizeof *g);
	if (logn > 0) log[0] = 0;
	cl_device_id d;
	cl_int err = qb_device(idx, &d);
	if (err != CL_SUCCESS) return err;
	g->ctx = clCreateContext(NULL, 1, &d, NULL, NULL, &err);
	if (err != CL_SUCCESS) goto fail;
	g->q = clCreateCommandQueue(g->ctx, d, 0, &err);
	if (err != CL_SUCCESS) goto fail;
	g->prog = clCreateProgramWithSource(g->ctx, 1, &src, &srclen, &err);
	if (err != CL_SUCCESS) goto fail;
	err = clBuildProgram(g->prog, 1, &d, "", NULL, NULL);
	if (err != CL_SUCCESS) {
		size_t need = 0;
		if (logn > 1 && clGetProgramBuildInfo(g->prog, d, CL_PROGRAM_BUILD_LOG, 0, NULL, &need) == CL_SUCCESS && need > 0) {
			char *buf = malloc(need);
			if (buf != NULL) {
				if (clGetProgramBuildInfo(g->prog, d, CL_PROGRAM_BUILD_LOG, need, buf, NULL) == CL_SUCCESS) {
					size_t m = need < logn - 1 ? need : logn - 1;
					memcpy(log, buf, m);
					log[m] = 0;
				}
				free(buf);
			}
		}
		goto fail;
	}
	g->k = clCreateKernel(g->prog, "scan", &err);
	if (err != CL_SUCCESS) goto fail;
	g->w1 = clCreateBuffer(g->ctx, CL_MEM_READ_ONLY, 16 * sizeof(cl_uint), NULL, &err);
	if (err != CL_SUCCESS) goto fail;
	g->w2 = clCreateBuffer(g->ctx, CL_MEM_READ_ONLY, 64 * sizeof(cl_uint), NULL, &err);
	if (err != CL_SUCCESS) goto fail;
	g->tgt = clCreateBuffer(g->ctx, CL_MEM_READ_ONLY, 8 * sizeof(cl_uint), NULL, &err);
	if (err != CL_SUCCESS) goto fail;
	g->out = clCreateBuffer(g->ctx, CL_MEM_READ_WRITE, QB_OUT_WORDS * sizeof(cl_uint), NULL, &err);
	if (err != CL_SUCCESS) goto fail;
	if ((err = clSetKernelArg(g->k, 0, sizeof(cl_mem), &g->w1)) != CL_SUCCESS) goto fail;
	if ((err = clSetKernelArg(g->k, 1, sizeof(cl_mem), &g->w2)) != CL_SUCCESS) goto fail;
	if ((err = clSetKernelArg(g->k, 2, sizeof(cl_mem), &g->tgt)) != CL_SUCCESS) goto fail;
	if ((err = clSetKernelArg(g->k, 5, sizeof(cl_mem), &g->out)) != CL_SUCCESS) goto fail;
	return CL_SUCCESS;
fail:
	qb_close(g);
	return err;
}

// qb_scan girdileri engelleyici yazmalarla yükler, çekirdeği count iş öğesiyle çalıştırır
// ve sonucu engelleyici okumayla out'a alır. Go belleğine işaret eden hiçbir komut çağrı
// dönünce kuyrukta kalmaz.
static cl_int qb_scan(qb_gpu *g, const cl_uint *w1, const cl_uint *w2, const cl_uint *tgt,
		cl_uint hi, cl_uint lo, size_t count, cl_uint *out) {
	cl_uint zero[QB_OUT_WORDS];
	memset(zero, 0, sizeof zero);
	cl_int err;
	if ((err = clEnqueueWriteBuffer(g->q, g->w1, CL_TRUE, 0, 16 * sizeof(cl_uint), w1, 0, NULL, NULL)) != CL_SUCCESS) return err;
	if ((err = clEnqueueWriteBuffer(g->q, g->w2, CL_TRUE, 0, 64 * sizeof(cl_uint), w2, 0, NULL, NULL)) != CL_SUCCESS) return err;
	if ((err = clEnqueueWriteBuffer(g->q, g->tgt, CL_TRUE, 0, 8 * sizeof(cl_uint), tgt, 0, NULL, NULL)) != CL_SUCCESS) return err;
	if ((err = clEnqueueWriteBuffer(g->q, g->out, CL_TRUE, 0, sizeof zero, zero, 0, NULL, NULL)) != CL_SUCCESS) return err;
	if ((err = clSetKernelArg(g->k, 3, sizeof(cl_uint), &hi)) != CL_SUCCESS) return err;
	if ((err = clSetKernelArg(g->k, 4, sizeof(cl_uint), &lo)) != CL_SUCCESS) return err;
	size_t gs = count;
	if ((err = clEnqueueNDRangeKernel(g->q, g->k, 1, NULL, &gs, NULL, 0, NULL, NULL)) != CL_SUCCESS) {
		clFinish(g->q);
		return err;
	}
	return clEnqueueReadBuffer(g->q, g->out, CL_TRUE, 0, QB_OUT_WORDS * sizeof(cl_uint), out, 0, NULL, NULL);
}
*/
import "C"

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"time"
	"unsafe"

	"quantum-bitcoin/core"
)

//go:embed sha256d.cl
var kernelSource string

// tuneTarget otomatik tur boyunun hedeflediği tek başlatma süresidir: durdurma ve uç
// değişimi en geç bu kadar gecikir.
const tuneTarget = 100 * time.Millisecond

const buildLogSize = 16 << 10

var errClosed = errors.New("GPU kazıcısı kapatıldı")

type clMiner struct {
	mu    sync.Mutex // tek komut kuyruğu: Scan çağrıları sıralanır
	g     C.qb_gpu
	open  bool
	batch uint64
	name  string
}

// Devices sistemdeki OpenCL aygıtlarını platformlar boyunca düz sırayla listeler.
func Devices() ([]Device, error) {
	n := int(C.qb_device_count())
	if n < 0 {
		return nil, fmt.Errorf("OpenCL platformu bulunamadı (hata %d): ICD yükleyicisi ve sürücü kurulu mu?", n)
	}
	devs := make([]Device, 0, n)
	var buf [256]C.char
	for i := 0; i < n; i++ {
		name := "?"
		if C.qb_device_name(C.int(i), &buf[0], C.size_t(len(buf))) == C.CL_SUCCESS {
			name = C.GoString(&buf[0])
		}
		devs = append(devs, Device{Index: i, Name: name})
	}
	return devs, nil
}

// Open device'ı açar ve çekirdeği derler. batch bir Scan çağrısının nonce sayısıdır; 0 ise
// tek başlatma ~tuneTarget sürecek biçimde ölçülerek seçilir.
func Open(device int, batch uint64) (Backend, error) {
	if device < 0 {
		return nil, fmt.Errorf("geçersiz OpenCL aygıtı: %d", device)
	}
	if batch != 0 && (batch < MinBatch || batch > MaxBatch) {
		return nil, fmt.Errorf("GPU tur boyu %d ile %d arasında olmalı: %d", MinBatch, MaxBatch, batch)
	}
	m := &clMiner{name: fmt.Sprintf("OpenCL aygıtı %d", device)}
	src := C.CString(kernelSource)
	defer C.free(unsafe.Pointer(src))
	var log [buildLogSize]C.char
	if rc := C.qb_open(C.int(device), src, C.size_t(len(kernelSource)), &m.g, &log[0], C.size_t(len(log))); rc != C.CL_SUCCESS {
		return nil, fmt.Errorf("OpenCL aygıtı %d açılamadı (hata %d): %s", device, int(rc), C.GoString(&log[0]))
	}
	m.open = true
	var buf [256]C.char
	if C.qb_device_name(C.int(device), &buf[0], C.size_t(len(buf))) == C.CL_SUCCESS {
		m.name = fmt.Sprintf("OpenCL aygıtı %d (%s)", device, C.GoString(&buf[0]))
	}
	m.batch = batch
	if batch == 0 {
		if err := m.tune(); err != nil {
			m.Close()
			return nil, err
		}
	}
	return m, nil
}

// tune tur boyunu MinBatch'ten ikiye katlayarak tek başlatma tuneTarget'ı geçene (ya da
// MaxBatch'e) kadar büyütür.
func (m *clMiner) tune() error {
	var hdr [core.HeaderSize]byte
	var zero [32]byte
	for b := uint64(MinBatch); ; b *= 2 {
		start := time.Now()
		if _, _, _, err := m.scan(hdr, zero, 0, b); err != nil {
			return err
		}
		if time.Since(start) >= tuneTarget || b >= MaxBatch {
			m.batch = b
			return nil
		}
	}
}

func (m *clMiner) RoundSize() uint64 { return m.batch }

func (m *clMiner) Name() string { return m.name }

// Scan core.MineBackend'i uygular.
func (m *clMiner) Scan(hdr [core.HeaderSize]byte, target [32]byte, start, count uint64) (uint64, bool, error) {
	n, _, ok, err := m.scan(hdr, target, start, count)
	return n, ok, err
}

// scan bulunan nonce'u ve çekirdeğin hesapladığı özeti döndürür (özet testler içindir).
func (m *clMiner) scan(hdr [core.HeaderSize]byte, target [32]byte, start, count uint64) (nonce uint64, hash [32]byte, found bool, err error) {
	if count == 0 || count > MaxBatch {
		return 0, hash, false, fmt.Errorf("GPU tur boyu geçersiz: %d", count)
	}
	var w1 [16]C.cl_uint
	for i := range w1 {
		w1[i] = C.cl_uint(binary.BigEndian.Uint32(hdr[4*i:]))
	}
	sched := secondBlockSchedule(hdr)
	var w2 [64]C.cl_uint
	for i, v := range sched {
		w2[i] = C.cl_uint(v)
	}
	var tg [8]C.cl_uint
	for i := range tg {
		tg[i] = C.cl_uint(binary.BigEndian.Uint32(target[4*i:]))
	}
	var out [11]C.cl_uint

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.open {
		return 0, hash, false, errClosed
	}
	if rc := C.qb_scan(&m.g, &w1[0], &w2[0], &tg[0], C.cl_uint(start>>32), C.cl_uint(start), C.size_t(count), &out[0]); rc != C.CL_SUCCESS {
		return 0, hash, false, fmt.Errorf("OpenCL taraması başarısız (hata %d)", int(rc))
	}
	if out[0] == 0 {
		return 0, hash, false, nil
	}
	nonce = uint64(out[1])<<32 | uint64(out[2])
	if nonce-start >= count { // sarma dahil [start, start+count) dışında: sürücü/çekirdek hatası
		return 0, hash, false, fmt.Errorf("OpenCL aralık dışı nonce döndürdü: %d (başlangıç %d, sayı %d)", nonce, start, count)
	}
	for i := 0; i < 8; i++ {
		binary.BigEndian.PutUint32(hash[4*i:], uint32(out[3+i]))
	}
	return nonce, hash, true, nil
}

// Close OpenCL kaynaklarını bırakır; sonraki Scan çağrıları hata döner.
func (m *clMiner) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.open {
		C.qb_close(&m.g)
		m.open = false
	}
	return nil
}

// secondBlockSchedule ikinci SHA-256 bloğunun (merkle kökü + 0x80 + uzunluk 768 bit)
// 64 sözcüklük mesaj takvimidir; şablon başına sabittir.
func secondBlockSchedule(hdr [core.HeaderSize]byte) [64]uint32 {
	var w [64]uint32
	for i := 0; i < 8; i++ {
		w[i] = binary.BigEndian.Uint32(hdr[64+4*i:])
	}
	w[8] = 0x80000000
	w[15] = core.HeaderSize * 8
	for i := 16; i < 64; i++ {
		s0 := bits.RotateLeft32(w[i-15], -7) ^ bits.RotateLeft32(w[i-15], -18) ^ (w[i-15] >> 3)
		s1 := bits.RotateLeft32(w[i-2], -17) ^ bits.RotateLeft32(w[i-2], -19) ^ (w[i-2] >> 10)
		w[i] = s1 + w[i-7] + s0 + w[i-16]
	}
	return w
}
