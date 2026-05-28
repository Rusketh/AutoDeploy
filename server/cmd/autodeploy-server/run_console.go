package main

import (
	"context"
	"log/slog"
	"os/signal"
	"syscall"
)

// runConsole runs the server in normal foreground / console mode,
// translating SIGINT and SIGTERM into a cancelled context. Used on
// Linux, macOS and on Windows when the binary is launched
// interactively rather than by the Service Control Manager.
func runConsole(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, logger)
}
