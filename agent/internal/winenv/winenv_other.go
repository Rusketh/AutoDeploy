//go:build !windows

package winenv

import "os"

// Expand resolves $VAR / ${VAR} references on non-Windows hosts (dev and CI).
// Windows %VAR% syntax passes through untouched -- a rule or step authored for
// Windows must not silently resolve against a Linux environment, the
// fail-closed behaviour the detection docs promise. The synthetic Program
// Files fallback is Windows-only and not needed here.
func Expand(s string) string {
	if s == "" {
		return s
	}
	return os.ExpandEnv(s)
}
