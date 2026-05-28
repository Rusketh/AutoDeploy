// Command autodeploy-server runs the management portal, HTTP API,
// Deployment Service and Domain Integration Service in a single process.
package main

import (
	"context"
	cryptoRand "crypto/rand"
	base64URLpkg "encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/api"
	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/httpx"
	"github.com/rusketh/autodeploy/server/internal/logging"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/portal"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/retention"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

var base64URL = base64URLpkg.RawURLEncoding

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

	// Open the at-rest secrets box (Phase 12).
	bx, err := secrets.Open(cfg.SecretsKeyHex, filepath.Join(cfg.DataDir, "secrets-key.bin"))
	if err != nil {
		return err
	}

	r := repos(db, bx)

	// Bootstrap a default admin account if no users exist yet, so an
	// operator can log in on first boot. The password is logged ONCE at
	// startup — the operator is expected to change it immediately. The
	// log line is recognisable so it shows up in any first-time setup
	// pipeline.
	if err := bootstrapAdmin(ctx, r.Users, cfg.DataDir, logger); err != nil {
		return err
	}

	mux, handler := httpx.New(cfg, logger)
	api.Register(mux, api.Repos{
		ISOs: r.ISOs, Unattend: r.Unattend, Drivers: r.Drivers,
		Software: r.Software, Loadouts: r.Loadouts,
		Images: r.Images, Inventory: r.Inventory,
		Resolver: r.Resolver,
		Users:    r.Users, Settings: r.Settings,
		BitLocker: r.BitLocker, Bulk: r.Bulk,
		Logs: r.Logs, Branding: r.Branding,
	})

	pl := &payload.Service{
		Blobs:    blobs,
		ISOs:     r.ISOs,
		Drivers:  r.Drivers,
		Software: r.Software,
		Resolver: r.Resolver,
	}
	pl.Register(mux)

	// Optional AD Domain Integration Service (Phase 10).
	var adSvc *addomain.Service
	if cfg.ADLDAPURL != "" {
		adSvc = &addomain.Service{
			Dir: addomain.NewLDAPDirectory(addomain.LDAPConfig{
				URL:           cfg.ADLDAPURL,
				BindDN:        cfg.ADBindDN,
				BindPassword:  cfg.ADBindPassword,
				SearchBase:    cfg.ADSearchBase,
				SkipTLSVerify: cfg.ADSkipTLSVerify,
			}),
			Log:     logger,
			Enabled: true,
		}
		logger.Info("addomain.configured",
			slog.String("url", cfg.ADLDAPURL),
			slog.String("search_base", cfg.ADSearchBase),
			slog.String("bind_dn", cfg.ADBindDN),
		)
	}

	mh := &payload.ManifestHandler{
		Resolver:  r.Resolver,
		AD:        adSvc,
		Inventory: r.Inventory,
		Unattend:  r.Unattend,
	}
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())

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

	// Start the retention scheduler (Phase 16). Hourly tick, prunes
	// log_event rows older than AUTODEPLOY_LOG_RETENTION_DAYS. Zero
	// disables pruning.
	if cfg.LogRetentionDays > 0 {
		sch := &retention.Scheduler{
			Logs:         r.Logs,
			LogRetention: time.Duration(cfg.LogRetentionDays) * 24 * time.Hour,
			Logger:       logger,
		}
		go sch.Start(ctx)
		logger.LogAttrs(ctx, slog.LevelInfo, "retention.scheduler_started",
			slog.Int("log_retention_days", cfg.LogRetentionDays))
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
	ISOs      *model.ISORepo
	Unattend  *model.UnattendRepo
	Drivers   *model.DriverPackageRepo
	Software  *model.SoftwarePackageRepo
	Loadouts  *model.SoftwareLoadoutRepo
	Images    *model.ImageRepo
	Inventory *model.InventoryRepo
	Resolver  *resolve.Resolver
	Users     *auth.Repo
	Settings  *auth.SettingsRepo
	BitLocker *model.BitLockerRepo
	Bulk      *model.BulkRepo
	Logs      *model.LogRepo
	Branding  *branding.Repo
}

func repos(db *storage.DB, bx *secrets.Box) appRepos {
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	loadouts := model.NewSoftwareLoadoutRepo(db)
	images := model.NewImageRepo(db)
	inventory := model.NewInventoryRepo(db)
	users := auth.New(db)
	settings := auth.MustNewSettingsRepo(users)
	bitlocker := model.NewBitLockerRepo(db, bx)
	bulk := model.NewBulkRepo(db, inventory)
	logs := model.NewLogRepo(db)
	brandRepo := branding.New(db)
	return appRepos{
		ISOs: isos, Unattend: unattend, Drivers: drivers,
		Software: software, Loadouts: loadouts, Images: images,
		Inventory: inventory,
		Resolver: resolve.New(images, isos, unattend).
			WithDrivers(drivers).WithLoadouts(loadouts),
		Users: users, Settings: settings,
		BitLocker: bitlocker, Bulk: bulk,
		Logs: logs, Branding: brandRepo,
	}
}

// bootstrapAdmin creates an "admin" account on first start if no users
// exist. The generated password is written to data/admin-bootstrap.txt
// with 0600 permissions; the log records ONLY the path. The operator
// reads the password from disk once, logs in, changes it, and deletes
// the file. The secret never appears in any log line.
func bootstrapAdmin(ctx context.Context, users *auth.Repo, dataDir string, logger *slog.Logger) error {
	existing, err := users.ListUsers(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	pw, err := randomPassword()
	if err != nil {
		return err
	}
	if _, err := users.CreateUser(ctx, "admin", pw); err != nil {
		return err
	}
	path := filepath.Join(dataDir, "admin-bootstrap.txt")
	if err := os.WriteFile(path, []byte("username: admin\npassword: "+pw+"\n"+
		"# Read this once, log in, change the password, then delete this file.\n"),
		0o600); err != nil {
		return err
	}
	// Log ONLY the path — never the password. This is the one place we
	// emit a "go look at this file" pointer; the file itself is the
	// secret store and CONVENTIONS.md §4 stays intact.
	logger.Warn("auth.bootstrap_admin",
		slog.String("actor", "system"),
		slog.String("target", "user:admin"),
		slog.String("password_file", path),
		slog.String("action", "Read the file, log in as admin, change the password via POST /api/v1/accounts/{id}/password, then delete the file"),
	)
	return nil
}

func randomPassword() (string, error) {
	var b [16]byte
	if _, err := cryptoRand.Read(b[:]); err != nil {
		return "", err
	}
	return base64URL.EncodeToString(b[:]), nil
}
