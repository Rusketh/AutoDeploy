//go:build !windows

package detect

import "os"

// expandPath resolves $VAR / ${VAR} references on non-Windows hosts.
// The agent runs on Windows in production but the dev / CI fakes
// happen on Linux, where operators occasionally write rules that
// use $HOME for a per-user file. os.ExpandEnv handles both
// $VAR and ${VAR}; %VAR% (the Windows syntax used in production
// rules) passes through unchanged on Linux, which is exactly what we
// want -- a rule that expects Windows env vars should not silently
// "detect" on a Linux dev host.
func expandPath(path string) string {
	if path == "" {
		return path
	}
	return os.ExpandEnv(path)
}
