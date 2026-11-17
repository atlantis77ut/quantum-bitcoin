//go:build !linux

package network

import "net"

// unackedBytes Linux dışında okunmaz: teslim bekleyen tutana eski kural (yalnızca hız sınırı)
// uygulanır (push.go "Teslim").
func unackedBytes(*net.TCPConn) (int, bool) { return 0, false }
