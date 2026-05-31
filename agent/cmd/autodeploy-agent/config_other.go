//go:build !windows

package main

// loadConfig has no registry off Windows; callers fall back to flags.
func loadConfig() (serverURL, agentID string) { return "", "" }
