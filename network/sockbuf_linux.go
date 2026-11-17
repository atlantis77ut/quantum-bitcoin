//go:build linux

package network

import "syscall"

// setRcvBuf soketin alma tamponunu n bayta ayarlar (çekirdek ikiye katlar, kendi ayarlamasını
// kapatır). Dinleyicide listen'dan önce ayarlanırsa kabul edilen bağlantılar da onu alır ve ilk
// pencere küçük ilan edilir (propbench.go throttleProxy).
func setRcvBuf(c syscall.RawConn, n int) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, n)
	}); err != nil {
		return err
	}
	return serr
}
