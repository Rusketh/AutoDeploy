//go:build windows

package detect

import (
	"syscall"
	"unsafe"
)

// expandPath resolves environment variable references in a detection
// rule's file_path. Operators write paths like
//
//	%ProgramFiles%\Microsoft VS Code\code.exe
//
// rather than hard-coding C:\Program Files, since the latter doesn't
// work on x86 hosts (or for per-user installs under %LOCALAPPDATA%).
// We call into kernel32!ExpandEnvironmentStringsW so the rules
// expand against the agent's process environment -- the same one
// the install steps run in -- so detection and install agree about
// where the file lives.
//
// On any expansion failure we fall back to the raw path. os.Stat
// will then fail with not-exist, which the caller already treats as
// "not detected", so a malformed env var doesn't turn into a confusing
// runtime error.
func expandPath(path string) string {
	if path == "" {
		return path
	}
	src, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return path
	}
	// 32KB buffer is comfortably more than MAX_PATH * 4 -- env
	// expansion can lengthen the string considerably (a single
	// %PATH% reference can blow past 1KB on its own) and we'd
	// rather one allocation than two.
	const bufLen = 32 * 1024
	buf := make([]uint16, bufLen)
	n, err := expandEnvironmentStringsW(src, &buf[0], uint32(bufLen))
	if err != nil || n == 0 || n > bufLen {
		return path
	}
	// n includes the terminating NUL, so trim it.
	return syscall.UTF16ToString(buf[:n-1])
}

// expandEnvironmentStringsW is the W (wide-char) variant of
// kernel32!ExpandEnvironmentStrings. We bind via syscall.NewLazyDLL
// rather than importing golang.org/x/sys/windows so the agent keeps
// zero external dependencies.
var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procExpandEnvironmentW  = kernel32.NewProc("ExpandEnvironmentStringsW")
)

func expandEnvironmentStringsW(src *uint16, dst *uint16, size uint32) (uint32, error) {
	r1, _, e1 := procExpandEnvironmentW.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(dst)),
		uintptr(size),
	)
	if r1 == 0 {
		if e1 != nil {
			return 0, e1
		}
		return 0, syscall.EINVAL
	}
	return uint32(r1), nil
}
