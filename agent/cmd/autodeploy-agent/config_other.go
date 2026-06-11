//go:build !windows

package main

// loadConfig has no registry off Windows; callers fall back to flags.
func loadConfig() (serverURL, agentID string) { return "", "" }

// saveConfig has nowhere to persist off Windows; the in-memory agent_id
// still serves the current process (tests, dev runs).
func saveConfig(serverURL, agentID string) error { return nil }
