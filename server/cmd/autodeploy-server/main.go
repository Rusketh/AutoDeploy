// Command autodeploy-server runs the management portal, HTTP API,
// Deployment Service and Domain Integration Service in a single process.
//
// Phase 1: SQLite-backed artifact and image management. The portal lives
// under /portal; the JSON API under /api/v1. /healthz reports liveness.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rusketh/autodeploy/server/internal/api"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/httpx"
	"github.com/rusketh/autodeploy/server/internal/logging"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/portal"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
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

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(cfg.DataDir, "autodeploy.sqlite")
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	repos := repos(db)

	mux, handler := httpx.New(cfg, logger)
	api.Register(mux, api.Repos{
		ISOs: repos.ISOs, Unattend: repos.Unattend, Drivers: repos.Drivers,
		Software: repos.Software, Images: repos.Images, Resolver: repos.Resolver,
	})
	if err := portal.Register(mux, portal.Repos{
		ISOs: repos.ISOs, Unattend: repos.Unattend, Drivers: repos.Drivers,
		Software: repos.Software, Images: repos.Images, Resolver: repos.Resolver,
	}); err != nil {
		return err
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "server.start",
		slog.String("actor", "system"),
		slog.String("target", cfg.HTTPAddr),
		slog.String("data_dir", cfg.DataDir),
		slog.String("db_path", dbPath),
		slog.Bool("dev_mode", cfg.DevMode),
	)

	if err := httpx.ListenAndServe(ctx, cfg, handler, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type appRepos struct {
	ISOs     *model.ISORepo
	Unattend *model.UnattendRepo
	Drivers  *model.DriverPackageRepo
	Software *model.SoftwarePackageRepo
	Images   *model.ImageRepo
	Resolver *resolve.Resolver
}

func repos(db *storage.DB) appRepos {
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	images := model.NewImageRepo(db)
	return appRepos{
		ISOs: isos, Unattend: unattend, Drivers: drivers,
		Software: software, Images: images,
		Resolver: resolve.New(images, isos, unattend),
	}
}
