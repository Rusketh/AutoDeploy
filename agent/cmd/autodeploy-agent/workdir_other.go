//go:build !windows

package main

import "log/slog"

// secureWorkDir is a no-op off Windows (the directory is created 0755 by
// the agent's own user).
func secureWorkDir(log *slog.Logger, dir string) {}

// secureDir is a no-op off Windows; files are written 0600.
func secureDir(log *slog.Logger, dir string) {}
