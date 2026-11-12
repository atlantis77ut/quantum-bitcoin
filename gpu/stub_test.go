//go:build !opencl

package gpu

import (
	"errors"
	"testing"
)

func TestStubReportsNoOpenCL(t *testing.T) {
	if _, err := Devices(); !errors.Is(err, ErrNoOpenCL) {
		t.Fatalf("Devices: %v", err)
	}
	if b, err := Open(0, 0); b != nil || !errors.Is(err, ErrNoOpenCL) {
		t.Fatalf("Open: %v %v", b, err)
	}
}
