//go:build !windows

package wol

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// setBroadcast enables SO_BROADCAST on the socket so magic packets may be
// written to the limited broadcast address.
func setBroadcast(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
