//go:build !windows

package main

// setNextBootNetwork is a no-op off Windows; the deployed agent only runs
// on Windows. Empty status => the caller just reboots.
func setNextBootNetwork() string { return "" }
