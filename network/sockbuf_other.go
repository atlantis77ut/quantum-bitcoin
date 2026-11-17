//go:build !linux

package network

import "syscall"

// setRcvBuf Linux dışında bir şey yapmaz: vekilin çekirdeği alma tamponu kadar baytı erken
// onaylar (propbench.go throttleProxy).
func setRcvBuf(syscall.RawConn, int) error { return nil }
