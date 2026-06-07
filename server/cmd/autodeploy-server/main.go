// Command autodeploy-server runs the management portal, HTTP API,
// Deployment Service and Domain Integration Service in a single process.
package main

import (
	"context"
	cryptoRand "crypto/rand"
	base64URLpkg "encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/api"
	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/bulksched"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/httpx"
	"github.com/rusketh/autodeploy/server/internal/logging"
	"github.com/rusketh/autodeploy/server/internal/metrics"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/portal"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/retention"
	"github.com/rusketh/autodeploy/server/internal/runtime"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/tftp"
)

var base64URL = base64URLpkg.RawURLEncoding

// Version is set at build time via -ldflags
// "-X main.Version=v0.1.2". Empty in dev builds; the CI release
// workflow populates it from the tag that triggered the build.
var Version = "dev"

// Version returns the build-time version string, falling back to
// "dev" for unstamped local builds. Exposed via the /api/v1/version
// endpoint so the portal Settings -> Updates page can show what's
// currently running.
func ServerVersion() string { return Version }

func main() {
	// Honour the `--version` flag for parity with how CI tools and
	// Windows package managers inspect binaries.
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "-v" {
			os.Stdout.WriteString(Version + "\n")
			return
		}
	}
	logger := logging.New(os.Stdout, "server")
	logger.Info("server.version", slog.String("version", Version))
	if err := runPlatform(logger); err != nil {
		logger.Error("server.fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run is the actual server lifecycle, parameterised by a context the
// caller cancels to trigger shutdown. The console entrypoint
// (runConsole) wires it to SIGINT/SIGTERM; the Windows service
// entrypoint (service_windows.go) wires it to the SCM stop control.
func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

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

	// Runtime settings: the operator-facing settings the portal can
	// change. AD config, log retention, payload throttle. The env-var
	// values seed the table on first start so existing env-driven
	// installs keep working.
	rt, err := runtime.New(ctx, db, bx, cfg)
	if err != nil {
		return err
	}
	// Push any operator-configured storage path overrides into the
	// blob store so payload writes/reads route to the configured
	// directories instead of $DATA_DIR/<category>. Errors here are
	// non-fatal -- a missing override directory means the operator
	// edited the setting before creating the path; the server still
	// starts, the warning is logged, and subsequent writes will fail
	// until the path exists.
	if errs := rt.ApplyStorageOverrides(blobs); len(errs) > 0 {
		for cat, e := range errs {
			logger.Warn("storage.override.failed",
				slog.String("category", cat),
				slog.String("error", e.Error()))
		}
	}

	// Bootstrap a default admin account if no users exist yet, so an
	// operator can log in on first boot. The password is logged ONCE at
	// startup — the operator is expected to change it immediately. The
	// log line is recognisable so it shows up in any first-time setup
	// pipeline.
	if err := bootstrapAdmin(ctx, r.Users, cfg.DataDir, logger); err != nil {
		return err
	}

	mtr := metrics.New()
	mux, rawHandler := httpx.New(cfg, logger, mtr)
	handler := httpx.DynamicTrustedProxyMiddleware(httpx.ProxyConfig{
		CIDRsFn:        rt.TrustedProxies,
		ExternalAccess: rt.ExternalAccess,
	}, rawHandler)

	// AD Domain Integration Service (Phase 10). Always-on; the
	// EnabledFunc reads the portal's current AD URL setting so an
	// operator can turn AD on/off through the UI without restarting.
	// Built BEFORE API registration so handlers that coordinate with
	// AD (bulk rename, manifest's pre-deploy delete-and-replace) can
	// reference it.
	adSvc := &addomain.Service{
		Dir: &addomain.DynamicDirectory{
			Provider: func(_ context.Context) addomain.LDAPConfig {
				return rt.ADConfig(ctx)
			},
		},
		Log:         logger,
		EnabledFunc: rt.ADEnabled,
	}
	if rt.ADEnabled() {
		c := rt.ADConfig(ctx)
		logger.Info("addomain.configured",
			slog.String("url", c.URL),
			slog.String("search_base", c.SearchBase),
			slog.String("bind_dn", c.BindDN),
			slog.String("source", "portal/env"))
	}

	// Build the notification emitter. SMTP and webhook channels are
	// configured lazily from runtime settings so the emitter starts
	// even when email isn't set up yet.
	webhookSender := notify.NewWebhookSender(r.Webhooks, logger)
	emitter := &notify.Emitter{
		Notifications: r.Notifications,
		Webhooks:      r.Webhooks,
		Users:         r.Users,
		Logger:        logger,
		Email: &notify.EmailSender{
			ConfigFn: func() notify.EmailConfig {
				if !rt.NotifyEmailEnabled() {
					return notify.EmailConfig{}
				}
				cfg := rt.NotifyEmailConfig(ctx)
				return notify.EmailConfig{
					Host:        cfg.Host,
					Port:        cfg.Port,
					User:        cfg.User,
					Password:    cfg.Password,
					TLSMode:     cfg.TLSMode,
					FromAddress: cfg.FromAddress,
					FromName:    cfg.FromName,
				}
			},
		},
		WebhookSender: webhookSender,
	}
	emitter.Start()
	defer emitter.Stop()
	r.Emitter = emitter

	// Expose the build-time version through the api package so the
	// /api/v1/version handler and the agent update-info handler
	// return the correct value.
	api.SetServerVersion(Version)

	// Build the api.Repos value once so we can share it with the
	// payload auth check below without duplicating field lists.
	apiRepos := api.Repos{
		ISOs: r.ISOs, Unattend: r.Unattend, Drivers: r.Drivers,
		Software: r.Software, Loadouts: r.Loadouts,
		Images: r.Images, Inventory: r.Inventory,
		Resolver: r.Resolver,
		Users:    r.Users, Settings: r.Settings,
		BitLocker: r.BitLocker, Bulk: r.Bulk,
		Logs: r.Logs, Branding: r.Branding,
		Mirrors: r.Mirrors, Runtime: rt,
		AD:            adSvc,
		Blobs:         blobs,
		DomainJoin:    r.DomainJoin,
		Updates:       r.Updates,
		Notifications: r.Notifications,
		WebhookRepo:   r.Webhooks,
		Emitter:       emitter,
	}

	api.Register(mux, apiRepos)

	pl := &payload.Service{
		Blobs:      blobs,
		ISOs:       r.ISOs,
		Drivers:    r.Drivers,
		Software:   r.Software,
		Resolver:   r.Resolver,
		Inventory:  r.Inventory,
		DomainJoin: r.DomainJoin,
		Updates:    r.Updates,
		RequireAuth: func(w http.ResponseWriter, req *http.Request) bool {
			if _, ok := api.UserFromRequest(req, apiRepos); !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return false
			}
			return true
		},
	}
	// Throttle /payload/* so a thundering herd queues rather than thrashes.
	pl.Throttle = payload.NewThrottle(cfg.PayloadMaxInFlight, 0, func() {
		mtr.PayloadQueuedWaits.Inc()
	})
	pl.OnBytesServed = func(n int64) { mtr.PayloadBytesServed.Add(n) }
	pl.Register(mux)

	mh := &payload.ManifestHandler{
		Resolver:   r.Resolver,
		AD:         adSvc,
		Inventory:  r.Inventory,
		Unattend:   r.Unattend,
		Software:   r.Software,
		Mirrors:    r.Mirrors,
		DomainJoin: r.DomainJoin,
	}
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())

	api.RegisterIPXE(mux)
	// Static iPXE asset tree (kernel / initrd / bootstrap binaries)
	// lives under data/ipxe/ by default, but the operator can
	// relocate it via the Settings -> Storage page. Resolve on every
	// request so a runtime change picks up without restart.
	mux.Handle("GET /ipxe/static/", http.StripPrefix("/ipxe/static/",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.FileServer(http.Dir(blobs.CategoryRoot("ipxe"))).ServeHTTP(w, r)
		})))

	if err := portal.Register(mux, portal.Repos{
		ISOs: r.ISOs, Unattend: r.Unattend, Drivers: r.Drivers,
		Software: r.Software, Loadouts: r.Loadouts,
		Images: r.Images, Inventory: r.Inventory,
		BitLocker: r.BitLocker, Bulk: r.Bulk, Logs: r.Logs,
		Users: r.Users, Settings: r.Settings, Branding: r.Branding,
		Mirrors:       r.Mirrors,
		Runtime:       rt,
		Resolver:      r.Resolver,
		Blobs:         blobs,
		AD:            adSvc,
		DomainJoin:    r.DomainJoin,
		Updates:       r.Updates,
		Notifications: r.Notifications,
		WebhookRepo:   r.Webhooks,
		Emitter:       emitter,
		SecretsBox:    bx,
		DataDir:       cfg.DataDir,
		ServerVersion: Version,
	}); err != nil {
		return err
	}

	// Inject the runtime settings into the portal so the AD /
	// retention pages can read and write through them.
	// (Wired in r.Runtime — see appRepos.)
	r.Runtime = rt

	// Start the retention scheduler. Always on; it re-reads the
	// retention setting each tick so an operator who changes it in
	// the portal sees the new value take effect within an interval.
	sch := &retention.Scheduler{
		Logs:                r.Logs,
		RetentionDays:       rt.LogRetentionDays,
		Logger:              logger,
		SessionPrune:        r.Users.PruneExpiredSessions,
		Notifications:       r.Notifications,
		NotifyRetentionDays: rt.NotifyPortalRetentionDays,
		WebhookRepo:         r.Webhooks,
	}
	go sch.Start(ctx)
	logger.LogAttrs(ctx, slog.LevelInfo, "retention.scheduler_started",
		slog.Int("log_retention_days", rt.LogRetentionDays()))

	// Start the bulk-operation scheduler. It fires one-time and recurring
	// bulk operations when their next_run_at arrives, materialising a fresh
	// run of agent jobs for each.
	bsch := &bulksched.Scheduler{Bulk: r.Bulk, Logger: logger}
	go bsch.Start(ctx)
	logger.LogAttrs(ctx, slog.LevelInfo, "bulksched.scheduler_started")

	// Apply portal-managed network overrides. The runtime settings
	// layer seeds from env on first start, so env-only installs work
	// unchanged; once an operator edits the Network page, those values
	// win.
	netCfg := rt.NetworkConfig()
	if netCfg.HTTPAddr != "" {
		cfg.HTTPAddr = netCfg.HTTPAddr
	}
	if netCfg.HTTPSAddr != "" {
		cfg.HTTPSAddr = netCfg.HTTPSAddr
	}
	if netCfg.TLSCertPath != "" {
		cfg.TLSCertFile = netCfg.TLSCertPath
	}
	if netCfg.TLSKeyPath != "" {
		cfg.TLSKeyFile = netCfg.TLSKeyPath
	}

	logger.LogAttrs(ctx, slog.LevelInfo, "server.start",
		slog.String("actor", "system"),
		slog.String("target", cfg.HTTPAddr),
		slog.String("https_addr", cfg.HTTPSAddr),
		slog.String("data_dir", cfg.DataDir),
		slog.String("db_path", dbPath),
		slog.Bool("dev_mode", cfg.DevMode),
	)

	// Run HTTP, HTTPS and (optionally) TFTP in parallel.
	errs := make(chan error, 3)
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
	if cfg.TFTPAddr != "" {
		// Built-in TFTP serves the iPXE category root read-only so a
		// classic PXE setup can grab undionly.kpxe / ipxe.efi etc.
		// without a separate TFTP daemon. Port 69 needs
		// CAP_NET_BIND_SERVICE. RootFunc consults the live BlobStore
		// router so a portal-driven storage relocation takes effect
		// without a restart.
		// httpPort lets the synthesised autoexec.ipxe chain to the right
		// HTTP port without baking the full URL — the script resolves
		// the host from DHCP option 66 (${next-server}) at boot time.
		httpPort := ""
		if _, p, err := net.SplitHostPort(cfg.HTTPAddr); err == nil {
			httpPort = p
		}
		ts := &tftp.Server{
			Addr:     cfg.TFTPAddr,
			RootFunc: func() string { return blobs.CategoryRoot("ipxe") },
			// Hand stock iPXE binaries a bootstrap script the instant
			// they probe for autoexec.ipxe — no embedded-build step and
			// no file to drop on disk. A real autoexec.ipxe placed in
			// the iPXE dir still wins (the synth only fires when the
			// name matches and nothing else has claimed it).
			Synth: func(name string) ([]byte, bool) {
				if name == "autoexec.ipxe" {
					return []byte(api.IPXEAutoexecNextServer(httpPort)), true
				}
				return nil, false
			},
			Logger: logger,
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ts.ListenAndServe(ctx); err != nil {
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
	ISOs          *model.ISORepo
	Unattend      *model.UnattendRepo
	Drivers       *model.DriverPackageRepo
	Software      *model.SoftwarePackageRepo
	Loadouts      *model.SoftwareLoadoutRepo
	Images        *model.ImageRepo
	Inventory     *model.InventoryRepo
	Resolver      *resolve.Resolver
	Users         *auth.Repo
	Settings      *auth.SettingsRepo
	BitLocker     *model.BitLockerRepo
	Bulk          *model.BulkRepo
	Logs          *model.LogRepo
	Branding      *branding.Repo
	Mirrors       *model.PayloadMirrorRepo
	Runtime       *runtime.Settings
	DomainJoin    *model.DomainJoinRepo
	Updates       *model.WindowsUpdateRepo
	Notifications *model.NotificationRepo
	Webhooks      *model.WebhookRepo
	Emitter       *notify.Emitter
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
	mirrors := model.NewPayloadMirrorRepo(db)
	domainJoin := model.NewDomainJoinRepo(db, bx)
	updates := model.NewWindowsUpdateRepo(db, inventory)
	notifications := model.NewNotificationRepo(db)
	webhooks := model.NewWebhookRepo(db, bx)
	return appRepos{
		ISOs: isos, Unattend: unattend, Drivers: drivers,
		Software: software, Loadouts: loadouts, Images: images,
		Inventory: inventory,
		Resolver: resolve.New(images, isos, unattend).
			WithDrivers(drivers).WithLoadouts(loadouts),
		Users: users, Settings: settings,
		BitLocker: bitlocker, Bulk: bulk,
		Logs: logs, Branding: brandRepo, Mirrors: mirrors,
		DomainJoin:    domainJoin,
		Updates:       updates,
		Notifications: notifications,
		Webhooks:      webhooks,
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
