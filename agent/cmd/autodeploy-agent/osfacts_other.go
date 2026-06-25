//go:build !windows

package main

// collectOSFacts is a no-op off Windows (dev/CI): there is no Windows OS
// caption to match a step's FilterOS against, so OS-filtered steps are skipped
// while unfiltered steps run exactly as before. The real implementation lives
// in osfacts_windows.go.
func collectOSFacts() (caption, version string) { return "", "" }
