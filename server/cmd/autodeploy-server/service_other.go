//go:build !windows

package main

import "log/slog"

// runPlatform on non-Windows platforms is a thin alias for runConsole;
// there is no equivalent service control manager to delegate to.
func runPlatform(logger *slog.Logger) error {
	return runConsole(logger)
}
