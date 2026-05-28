// Command autodeploy-server runs the management portal, HTTP API,
// Deployment Service and Domain Integration Service in a single process.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/rusketh/autodeploy/server/internal/api"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/httpx"
	"github.com/rusketh/autodeploy/server/internal/logging"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
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

	blobs, err := storage.NewBlobStore(cfg.DataDir)
	if err != nil {
		return err
	}

	r := repos(db)

	mux, handler := httpx.New(cfg, logger)
	api.Register(mux, api.Repos{
		ISOs: r.ISOs, Unattend: r.Unattend, Drivers: r.Drivers,
		Software: r.Software, Images: r.Images, Resolver: r.Resolver,
	})

	pl := &payload.Service{
		Blobs: blobs, ISOs: r.ISOs, Drivers: r.Drivers, Software: r.Software,
	}
	pl.Register(mux)

	mh := &payload.ManifestHandler{Resolver: r.Resolver}
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())

	api.RegisterIPXE(mux)
	// Static iPXE asset tree (kernel/initrd) lives under data/ipxe/.
	staticDir := filepath.Join(cfg.DataDir, "ipxe")
	_ = os.MkdirAll(staticDir, 0o755)
	mux.Handle("GET /ipxe/static/", http.StripPrefix("/ipxe/static/", http.FileServer(http.Dir(staticDir))))

	if err := portal.Register(mux, portal.Repos{
		ISOs: r.ISOs, Unattend: r.Unattend, Drivers: r.Drivers,
		Software: r.Software, Images: r.Images, Resolver: r.Resolver,
	}); err != nil {
		return err
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "server.start",
		slog.String("actor", "system"),
		slog.String("target", cfg.HTTPAddr),
		slog.String("https_addr", cfg.HTTPSAddr),
		slog.String("data_dir", cfg.DataDir),
		slog.String("db_path", dbPath),
		slog.Bool("dev_mode", cfg.DevMode),
	)

	// Run HTTP and HTTPS in parallel if both are configured.
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	if cfg.HTTPAddr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := httpx.ListenAndServe(ctx, cfg, handler, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	if cfg.HTTPSAddr != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := httpx.ListenAndServeTLS(ctx, cfg, handler, logger); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
	}
	go func() { wg.Wait(); close(errs) }()
	if err, ok := <-errs; ok && err != nil {
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
