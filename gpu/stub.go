//go:build !opencl

package gpu

// Devices OpenCL desteği olmadan derlenmiş ikilide ErrNoOpenCL döndürür.
func Devices() ([]Device, error) { return nil, ErrNoOpenCL }

// Open OpenCL desteği olmadan derlenmiş ikilide ErrNoOpenCL döndürür.
func Open(device int, batch uint64) (Backend, error) { return nil, ErrNoOpenCL }
