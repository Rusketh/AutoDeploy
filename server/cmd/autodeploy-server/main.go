// Command autodeploy-server runs the management portal, HTTP API,
// Deployment Service and Domain Integration Service in a single process.
//
// Phase 0 skeleton: configuration loading, structured logging, an HTTP
// listener exposing /healthz, and the production-safety guard that refuses
// cleartext HTTP on non-loopback addresses.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/httpx"
	"github.com/rusketh/autodeploy/server/internal/logging"
)

func main() {
	logger := logging.New(os.Stdout, "server")
	if err := run(logger); err != nil {
		logger.Error("server.fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := httpx.New(cfg, logger)
	logger.LogAttrs(ctx, slog.LevelInfo, "server.start",
		slog.String("actor", "system"),
		slog.String("target", cfg.HTTPAddr),
		slog.Bool("dev_mode", cfg.DevMode),
		slog.String("data_dir", cfg.DataDir),
	)

	if err := httpx.ListenAndServe(ctx, cfg, h, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
