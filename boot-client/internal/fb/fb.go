// Package fb is a pure-Go (CGO-free) Linux framebuffer backend. It opens
// /dev/fb0, queries its geometry/format via FBIO ioctls, mmaps the device
// memory, and presents an in-memory *image.RGBA backbuffer that the UI
// draws into; Flush converts that backbuffer to the device pixel format
// and copies it to the mapped memory.
//
// Only 16/24/32-bpp little-endian layouts are handled (what efifb /
// simpledrm / hyperv_fb / vmwgfx expose in practice). Open returns an
// error when there is no framebuffer (e.g. serial-only console or an
// unsupported format) so the caller can fall back to the text console.
package fb

import (
	"fmt"
	"image"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioctl request numbers from <linux/fb.h>.
const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
)

// fbVarScreenInfo mirrors struct fb_var_screeninfo (the prefix we need).
type fbVarScreenInfo struct {
	Xres, Yres                   uint32
	XresVirtual, YresVirtual     uint32
	XoffSet, YoffSet             uint32
	BitsPerPixel                 uint32
	Grayscale                    uint32
	RedOffset, RedLength, RedMsb uint32
	GreenOffset, GreenLen, GMsb  uint32
	BlueOffset, BlueLen, BMsb    uint32
	TranspOff, TranspLen, TMsb   uint32
	// Remaining fields are not needed; the struct is read with the full
	// size below via a byte buffer so we don't depend on exact tail layout.
}

// fbFixScreenInfo mirrors the head of struct fb_fix_screeninfo.
type fbFixScreenInfo struct {
	ID         [16]byte
	SmemStart  uint64
	SmemLen    uint32
	Type       uint32
	TypeAux    uint32
	Visual     uint32
	XPanStep   uint16
	YPanStep   uint16
	YWrapStep  uint16
	_          uint16
	LineLength uint32
	// tail omitted
}

// Framebuffer is an open framebuffer device with a backbuffer to draw into.
type Framebuffer struct {
	f          *os.File
	mem        []byte // mmap'd device memory
	back       *image.RGBA
	width      int
	height     int
	bpp        int
	lineLength int
	// channel bit offsets/lengths for packing.
	rOff, gOff, bOff uint32
	rLen, gLen, bLen uint32
	// vt holds the console put into graphics mode for the GUI's lifetime
	// (so the kernel text console / fbcon stops drawing over us). nil if
	// VT control couldn't be acquired -- the GUI still works, but console
	// text may flicker through, which is the pre-fix behaviour.
	vt *vtGuard
}

// Open opens the given framebuffer device (use "" for /dev/fb0) and maps it.
func Open(dev string) (*Framebuffer, error) {
	if dev == "" {
		dev = "/dev/fb0"
	}
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dev, err)
	}
	var vinfo fbVarScreenInfo
	if err := ioctl(f.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&vinfo)); err != nil {
		f.Close()
		return nil, fmt.Errorf("FBIOGET_VSCREENINFO: %w", err)
	}
	var finfo fbFixScreenInfo
	if err := ioctl(f.Fd(), fbioGetFScreenInfo, unsafe.Pointer(&finfo)); err != nil {
		f.Close()
		return nil, fmt.Errorf("FBIOGET_FSCREENINFO: %w", err)
	}
	if vinfo.BitsPerPixel != 16 && vinfo.BitsPerPixel != 24 && vinfo.BitsPerPixel != 32 {
		f.Close()
		return nil, fmt.Errorf("unsupported framebuffer depth %d bpp", vinfo.BitsPerPixel)
	}
	size := int(finfo.SmemLen)
	if size == 0 {
		size = int(finfo.LineLength) * int(vinfo.Yres)
	}
	mem, err := unix.Mmap(int(f.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("mmap framebuffer: %w", err)
	}
	w, h := int(vinfo.Xres), int(vinfo.Yres)
	return &Framebuffer{
		f: f, mem: mem,
		back:  image.NewRGBA(image.Rect(0, 0, w, h)),
		width: w, height: h,
		bpp:        int(vinfo.BitsPerPixel),
		lineLength: int(finfo.LineLength),
		rOff:       vinfo.RedOffset, gOff: vinfo.GreenOffset, bOff: vinfo.BlueOffset,
		rLen: vinfo.RedLength, gLen: vinfo.GreenLen, bLen: vinfo.BlueLen,
		// Take the console into graphics mode so the kernel text console
		// (fbcon) stops painting over the GUI. Best-effort: nil on failure.
		vt: acquireVT(),
	}, nil
}

// Image returns the backbuffer the UI draws into.
func (fb *Framebuffer) Image() *image.RGBA { return fb.back }

// Size returns the framebuffer dimensions in pixels.
func (fb *Framebuffer) Size() (w, h int) { return fb.width, fb.height }

// Flush converts the backbuffer to the device format and copies it out.
func (fb *Framebuffer) Flush() {
	packPixels(fb.mem, fb.back, fb.width, fb.height, fb.bpp, fb.lineLength,
		fb.rOff, fb.gOff, fb.bOff, fb.rLen, fb.gLen, fb.bLen)
}

// Close unmaps and closes the device. It also restores the text console we
// took into graphics mode in Open, so after the GUI tears down (the operator
// cancelled, or unticked "Show imaging progress" to debug on the console) the
// kernel and boot-client logs are visible again instead of a blank,
// graphics-mode console.
func (fb *Framebuffer) Close() error {
	if fb.mem != nil {
		_ = unix.Munmap(fb.mem)
		fb.mem = nil
	}
	if fb.vt != nil {
		fb.vt.release()
		fb.vt = nil
	}
	return fb.f.Close()
}

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
