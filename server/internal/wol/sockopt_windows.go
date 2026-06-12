//go:build windows

package wol

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// setBroadcast enables SO_BROADCAST on the socket so magic packets may be
// written to the limited broadcast address.
func setBroadcast(_, _ string, c syscall.RawConn) error {
	var serr error
	err := c.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_BROADCAST, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
