//go:build !(linux || darwin || freebsd)

package portal

// diskFree is not implemented on this platform; the settings hub simply
// leaves free space out.
func diskFree(path string) (free, total uint64, ok bool) { return 0, 0, false }
