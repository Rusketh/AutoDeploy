//go:build !windows

package main

import "os"

// readSystemUUIDPlatform reads /sys/class/dmi/id/product_uuid on
// Linux. Returns "" on read failure (other platforms, or the file is
// unreadable). The Linux path is mainly for dev hosts and CI; the
// supported deploy target is Windows.
func readSystemUUIDPlatform() string {
	b, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
	if err != nil {
		return ""
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}
