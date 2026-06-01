//go:build !windows

package main

// expandEnv is a no-op off Windows: %VAR% is a Windows convention and the
// deployed agent only ever runs on Windows. Keeping the value untouched lets
// the resident loop build and the step-rewrite logic be tested on the dev host.
func expandEnv(s string) string { return s }
