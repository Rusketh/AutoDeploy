//go:build !windows

package main

import "log/slog"

// secureWorkDir is a no-op off Windows (the directory is created 0755 by
// the agent's own user).
func secureWorkDir(log *slog.Logger, dir string) {}
