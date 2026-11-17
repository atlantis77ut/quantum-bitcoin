//go:build linux

package network

import (
	"net"
	"syscall"
	"unsafe"
)

// unackedBytes bağlantının gönderme kuyruğunda alıcının henüz onaylamadığı bayt sayısıdır
// (Linux TIOCOUTQ, SIOCOUTQ ile aynı: write_seq - snd_una; yazma yönü kapatıldıysa FIN dahil).
// Okunamazsa (ör. bağlantı kapandı) ok false döner. push.go "Teslim" teslim ilerlemesini bununla
// izler.
func unackedBytes(c *net.TCPConn) (int, bool) {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var n int32 // C int
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCOUTQ, uintptr(unsafe.Pointer(&n)))
	}); err != nil || errno != 0 {
		return 0, false
	}
	return int(n), true
}
