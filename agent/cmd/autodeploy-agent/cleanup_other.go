//go:build !windows

package main

// cleanupMedia is a no-op off Windows; the deployed agent only ever runs
// on Windows. Empty status tells the caller there was nothing to do.
func cleanupMedia() string { return "" }
