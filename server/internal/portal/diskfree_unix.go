//go:build linux || darwin || freebsd

package portal

import "syscall"

// diskFree reports the bytes available to unprivileged users on the
// filesystem holding path. ok is false when it can't be determined.
func diskFree(path string) (free, total uint64, ok bool) {
	var st syscall.Statfs_t
	if path == "" || syscall.Statfs(path, &st) != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, true
}
