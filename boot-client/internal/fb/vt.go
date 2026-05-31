package fb

import (
	"os"

	"golang.org/x/sys/unix"
)

// KD ioctls from <linux/kd.h>.
const (
	kdSetMode      = 0x4B3A
	kdModeText     = 0x00
	kdModeGraphics = 0x01
)

// vtGuard holds a console fd switched to KD_GRAPHICS so the kernel's text
// console (fbcon) stops rendering onto the framebuffer while the GUI owns
// it. release() restores text mode.
type vtGuard struct {
	tty *os.File
}

// acquireVT puts the active console into graphics mode. It tries the
// controlling terminal first, then /dev/tty0 and /dev/console (in the
// initramfs the GUI usually runs on the console). Returns nil if no
// console could be switched -- the caller treats that as "no VT control"
// and proceeds (the GUI still draws; console text may flicker through,
// which is the pre-fix behaviour).
func acquireVT() *vtGuard {
	for _, dev := range []string{"/dev/tty0", "/dev/console", "/dev/tty"} {
		f, err := os.OpenFile(dev, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if err := kdSetGraphics(f.Fd(), kdModeGraphics); err != nil {
			_ = f.Close()
			continue
		}
		return &vtGuard{tty: f}
	}
	return nil
}

// release restores the console to text mode and closes it.
func (v *vtGuard) release() {
	if v == nil || v.tty == nil {
		return
	}
	_ = kdSetGraphics(v.tty.Fd(), kdModeText)
	_ = v.tty.Close()
	v.tty = nil
}

func kdSetGraphics(fd uintptr, mode uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, kdSetMode, mode)
	if errno != 0 {
		return errno
	}
	return nil
}
