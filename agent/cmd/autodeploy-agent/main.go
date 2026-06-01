// Command autodeploy-agent is the resident, silent in-OS Deployment
// Client. It runs first as part of unattended setup (via the unattend's
// FirstLogonCommand), pulls the effective software set, evaluates
// detection rules, executes install steps for packages not already
// installed, and (Phase 13) becomes a long-running check-in service for
// bulk jobs.
//
// Phase 6: deploy-time configuration only. Resident check-in is wired in
// Phase 13.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/bitlocker"
	"github.com/rusketh/autodeploy/agent/internal/detect"
	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/logging"
	"github.com/rusketh/autodeploy/agent/internal/selfupdate"
	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

type agentFlags struct {
	server          string
	agentID         string
	imageID         int64
	uuid            string
	insecureTLS     bool
	workDir         string
	dryRun          bool
	checkInInterval time.Duration
	noSelfUpdate    bool
}

// Version is set at build time via -ldflags
// "-X main.Version=v0.1.2". Reported on every check-in so the
// server can decide whether to push a self-update.
var Version = "dev"

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "-v" {
			os.Stdout.WriteString(Version + "\n")
			return
		}
	}
	var f agentFlags
	flag.StringVar(&f.server, "server", "", "AutoDeploy server base URL")
	flag.Int64Var(&f.imageID, "image-id", 0, "Image id this machine was deployed from")
	flag.StringVar(&f.uuid, "uuid", "", "Machine SMBIOS UUID (override; defaults to read from firmware)")
	flag.BoolVar(&f.insecureTLS, "insecure-tls", false, "Skip TLS verification (dev only)")
	flag.StringVar(&f.workDir, "work", defaultWorkDir(), "Scratch directory for downloaded payloads")
	flag.BoolVar(&f.dryRun, "dry-run", false, "Log steps without executing them")
	flag.DurationVar(&f.checkInInterval, "check-in", 0, "Resident-mode check-in interval, e.g. 5m. Zero = one-shot.")
	flag.BoolVar(&f.noSelfUpdate, "no-self-update", false, "Don't apply self-updates even if the server advertises a newer version. Useful for testing or pinning.")
	flag.Parse()

	log, shipper := logging.NewWithShipper(os.Stdout, "agent", 2048)
	// Ship buffered records to the server when the agent exits (one-
	// shot mode) or periodically while it runs (resident mode -- the
	// check-in loop calls shipLogs each tick).
	defer shipLogs(log, shipper, f.server, f.insecureTLS)

	// Clean up any debris from a previous self-update on startup
	// (an .bak the prior swap left behind, a half-written .new from
	// a crashed download, etc).
	selfupdate.Cleanup()

	if f.uuid == "" {
		f.uuid = readSystemUUID()
	}

	// Service management subcommands (invoked by SetupComplete.cmd at
	// deploy time, or manually).
	switch flag.Arg(0) {
	case "install-service":
		if err := installService(); err != nil {
			log.Error("service.install", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("service.install.ok")
		return
	case "uninstall-service":
		if err := uninstallService(); err != nil {
			log.Error("service.uninstall", slog.String("error", err.Error()))
			os.Exit(1)
		}
		log.Info("service.uninstall.ok")
		return
	}

	// Registry-provisioned config (server URL + agent_id, written by the
	// Boot Client's SetupComplete) fills in anything not given on the CLI.
	if rs, ra := loadConfig(); true {
		if f.server == "" {
			f.server = rs
		}
		if f.agentID == "" {
			f.agentID = ra
		}
	}

	log.Info("agent.start",
		slog.String("actor", "system"),
		slog.String("target", "self"),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("server", f.server),
		slog.String("uuid", f.uuid),
		slog.String("agent_id", f.agentID),
		slog.Int64("image_id", f.imageID),
		slog.Bool("dry_run", f.dryRun),
	)

	// New model: identify by the server-minted agent_id and poll
	// /api/v1/agent/self for desired state. This is how the deployed agent
	// runs -- as a Windows service, polling forever; the SCM Stop handler
	// cancels the loop.
	if f.server != "" && f.agentID != "" {
		if err := os.MkdirAll(f.workDir, 0o755); err != nil {
			log.Error("workdir.create", slog.String("error", err.Error()))
			os.Exit(1)
		}
		c := httpc.New(f.server, f.uuid, f.insecureTLS)
		if isWindowsService() {
			if err := runService(func(sctx context.Context) {
				runSelfLoop(sctx, log, c, f, shipper, f.checkInInterval)
			}); err != nil {
				log.Error("service.run", slog.String("error", err.Error()))
				os.Exit(1)
			}
			return
		}
		// Console: a one-shot poll, or a foreground loop with --check-in.
		ctx := context.Background()
		if f.checkInInterval > 0 {
			runSelfLoop(ctx, log, c, f, shipper, f.checkInInterval)
		} else {
			runSelfOnce(ctx, log, c, f)
		}
		return
	}

	// Legacy deploy-time path (--server + --image-id). Retained for manual
	// runs and tests; the provisioned deployment uses the agent_id path
	// above.
	if f.server == "" || f.imageID == 0 {
		log.Info("agent.idle",
			slog.String("reason", "no registry config (server+agent_id) and no --server/--image-id; nothing to do"))
		return
	}

	ctx := context.Background()
	if err := os.MkdirAll(f.workDir, 0o755); err != nil {
		log.Error("workdir.create", slog.String("error", err.Error()))
		os.Exit(1)
	}

	c := httpc.New(f.server, f.uuid, f.insecureTLS)

	// Fetch the effective software set.
	var resp struct {
		Items    []softwareItem `json:"items"`
		Warnings []string       `json:"warnings,omitempty"`
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/software", map[string]any{
		"image_id": f.imageID,
		"identity": map[string]any{"system_uuid": f.uuid},
	}, &resp); err != nil {
		log.Error("software.fetch", slog.String("error", err.Error()))
		os.Exit(1)
	}
	for _, w := range resp.Warnings {
		log.Warn("software.warning", slog.String("message", w))
	}

	// Report opening so the server can record an in-progress deployment.
	var openResp struct {
		MachineID    int64  `json:"machine_id"`
		DeploymentID int64  `json:"deployment_id"`
		DeployToken  string `json:"deploy_token,omitempty"`
	}
	identityBody := map[string]any{
		"system_uuid": f.uuid,
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/report", map[string]any{
		"identity": identityBody,
		"image_id": f.imageID,
		"outcome":  "in_progress",
	}, &openResp); err != nil {
		log.Warn("report.open", slog.String("error", err.Error()))
	}
	depID := openResp.DeploymentID
	// The deploy token authenticates the agent to secret-returning
	// endpoints (BitLocker config) for the lifetime of this deploy.
	// Kept in memory only; never written to disk; never logged.
	c.DeployToken = openResp.DeployToken

	packageReports, failed := installPackages(ctx, log, c, f, resp.Items)

	// BitLocker (Phase 12): if the server has a PIN configured for this
	// machine, enable encryption and escrow the recovery key. Off-Windows
	// or on a host without TPM/PowerShell, the agent logs and skips.
	maybeEnableBitLocker(ctx, log, c, f, identityBody)

	// Branding (Phase 15 / design §12): write the operator's OEM
	// identity to HKLM\...\OEMInformation so System Properties shows
	// the right manufacturer / support URL. Best-effort: failures are
	// logged but do not break the deployment.
	applyOEMBranding(ctx, log, c, f)

	// Final report: mark the deployment ok/failed and ship per-package
	// detection state.
	outcome := "ok"
	if failed {
		outcome = "failed"
	}
	if depID != 0 {
		var ignore struct{}
		if err := c.PostJSON(ctx, "/api/v1/agent/report", map[string]any{
			"identity":      identityBody,
			"image_id":      f.imageID,
			"deployment_id": depID,
			"outcome":       outcome,
			"packages":      packageReports,
		}, &ignore); err != nil {
			log.Warn("report.close", slog.String("error", err.Error()))
		}
	}

	log.Info("agent.done", slog.String("actor", f.uuid), slog.String("outcome", outcome))

	// Resident check-in mode (Phase 13). Run forever, polling for queued
	// bulk jobs and executing them.
	if f.checkInInterval > 0 {
		runCheckInLoop(ctx, log, c, f, identityBody, shipper)
	}
}

// selfResponse mirrors the server's AgentSelfResponse from
// GET /api/v1/agent/self?id=<agent_id>.
type selfResponse struct {
	MachineID int64          `json:"machine_id"`
	ImageID   int64          `json:"image_id"`
	Software  []softwareItem `json:"software"`
	Jobs      []struct {
		ID      int64  `json:"id"`
		Action  string `json:"action"`
		Payload string `json:"payload"`
	} `json:"jobs"`
	Warnings            []string `json:"warnings"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
	// DeploymentID is the primary key of an open (in_progress) deployment
	// row for this machine. Non-zero when the boot client opened a deployment
	// that hasn't been closed yet; the agent reports the final outcome after
	// installing packages.
	DeploymentID int64 `json:"deployment_id"`
}

// runSelfOnce polls the server for this machine's desired state (by its
// server-minted agent_id) and acts on it: installs the bound image's
// software set and runs any queued bulk jobs. Best-effort -- a poll that
// fails is logged and retried on the next tick. Returns the server-advertised
// check-in interval (0 if none/unreachable) so the caller can adjust cadence.
func runSelfOnce(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) time.Duration {
	var resp selfResponse
	// agent_id is a UUID (URL-safe), so no escaping needed.
	if err := c.GetJSON(ctx, "/api/v1/agent/self?id="+f.agentID, &resp); err != nil {
		log.Warn("self.fetch", slog.String("error", err.Error()))
		return 0
	}
	for _, w := range resp.Warnings {
		log.Warn("self.warning", slog.String("message", w))
	}
	// Report collected hardware once per process run (the WMI sweep is
	// relatively expensive; specs change rarely). Best-effort.
	reportHardwareOnce(ctx, log, c, f)
	// Report the observed identity (current computer name + AD DN) EVERY
	// poll: a rename needs a reboot (which restarts us) but an AD move does
	// not, so once-per-process would miss moves. The server syncs the
	// binding from this. Best-effort.
	reportObservedIdentity(ctx, log, c, f)
	// Agent-driven AD domain join: if this machine's image is configured for
	// it and we're not already in the domain, join and reboot. Done before
	// software/jobs so a reboot doesn't interrupt a half-finished install; the
	// credentials are fetched per-call and never logged.
	if maybeDomainJoin(ctx, log, c, f) {
		return 0 // a join reboot is scheduled; the rest resumes after reboot
	}
	// Reclaim the boot-media partition (ADBOOT) once. SECURITY: it holds
	// autounattend.xml with the admin password in plaintext, so this must
	// happen on the deployed machine. Also extends C: into the freed space.
	cleanupMediaOnce(log)
	var packageReports []pkgReport
	var pkgFailed bool
	if len(resp.Software) > 0 {
		packageReports, pkgFailed = installPackages(ctx, log, c, f, resp.Software)
	}
	// If the server told us about an open deployment (opened by the boot
	// client), report the final outcome so the dashboard row transitions
	// from "in_progress" to "ok" or "failed". This fires at most once per
	// deployment: after reporting, the server's next /self response won't
	// include the deployment_id anymore.
	if resp.DeploymentID != 0 {
		outcome := "ok"
		if pkgFailed {
			outcome = "failed"
		}
		identityBody := map[string]any{"system_uuid": f.uuid}
		var ignore struct{}
		if err := c.PostJSON(ctx, "/api/v1/agent/report", map[string]any{
			"identity":      identityBody,
			"image_id":      resp.ImageID,
			"deployment_id": resp.DeploymentID,
			"outcome":       outcome,
			"packages":      packageReports,
		}, &ignore); err != nil {
			log.Warn("deploy.close", slog.String("error", err.Error()))
		} else {
			log.Info("deploy.closed",
				slog.String("actor", f.uuid),
				slog.Int64("deployment_id", resp.DeploymentID),
				slog.String("outcome", outcome))
		}
	}
	if len(resp.Jobs) > 0 {
		runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
		for _, j := range resp.Jobs {
			status, result := executeBulkJob(ctx, log, c, f, runner, j.Action, j.Payload)
			_ = c.PostJSON(ctx,
				fmt.Sprintf("/api/v1/agent/jobs/%d/result", j.ID),
				map[string]any{"status": status, "result_json": result}, nil)
		}
	}
	if resp.PollIntervalSeconds > 0 {
		return time.Duration(resp.PollIntervalSeconds) * time.Second
	}
	return 0
}

// domainJoined guards maybeDomainJoin so a successful join (which schedules a
// reboot) isn't re-attempted later in the same process.
var domainJoined bool

// maybeDomainJoin asks the server whether this machine's image is configured
// for agent-driven AD join and, if so and the machine isn't already in that
// domain, joins it and schedules a reboot. Returns true when a reboot was
// scheduled (so the caller should stop this poll). Credentials are fetched
// per-call and NEVER logged; failures are logged and retried on the next poll.
func maybeDomainJoin(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) bool {
	if domainJoined || f.agentID == "" {
		return false
	}
	var resp struct {
		Join     bool   `json:"join"`
		Domain   string `json:"domain"`
		OU       string `json:"ou"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/domain-join",
		map[string]any{"agent_id": f.agentID}, &resp); err != nil {
		log.Warn("domainjoin.fetch", slog.String("error", err.Error()))
		return false
	}
	if !resp.Join || resp.Domain == "" {
		return false
	}
	if cur, joined := currentDomain(); joined && strings.EqualFold(cur, resp.Domain) {
		domainJoined = true // already a member of the target domain
		return false
	}
	if f.dryRun {
		log.Info("domainjoin.skip",
			slog.String("reason", "--dry-run"), slog.String("domain", resp.Domain))
		return false
	}
	if err := joinDomain(resp.Domain, resp.OU, resp.User, resp.Password); err != nil {
		log.Error("domainjoin.fail",
			slog.String("domain", resp.Domain), slog.String("error", err.Error()))
		return false // retry on the next poll
	}
	domainJoined = true
	// LOG ONLY THE FACT — credentials never appear in any log line.
	log.Info("domainjoin.ok",
		slog.String("domain", resp.Domain), slog.String("ou", resp.OU),
		slog.String("note", "rebooting to complete join"))
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	_, _ = runner.Run(ctx, "shutdown", []string{"/r", "/t", "15", "/c", "AutoDeploy domain join"}, "")
	return true
}

// hardwareReported guards reportHardwareOnce so the WMI sweep runs at most
// once per agent process (specs rarely change; a service restart re-reports).
var hardwareReported bool

// reportHardwareOnce collects and posts the machine's hardware spec the
// first time it's called in this process. Needs the agent_id (the server
// keys hardware by it). All failures are logged and swallowed.
func reportHardwareOnce(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) {
	if hardwareReported || f.agentID == "" {
		return
	}
	hw := collectHardware()
	if hw == nil {
		hardwareReported = true // non-Windows / unsupported: don't retry
		return
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/hardware",
		map[string]any{"agent_id": f.agentID, "hardware": hw}, nil); err != nil {
		log.Warn("hardware.report", slog.String("error", err.Error()))
		return // leave the guard unset so the next poll retries
	}
	hardwareReported = true
	log.Info("hardware.reported")
}

// reportObservedIdentity posts the machine's current computer name and AD
// distinguished name so the server tracks manual renames and AD moves and
// syncs the binding. Runs every poll; all failures are logged and swallowed.
func reportObservedIdentity(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) {
	if f.agentID == "" {
		return
	}
	name, dn := collectObservedIdentity()
	if name == "" && dn == "" {
		return
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/identity", map[string]any{
		"agent_id":              f.agentID,
		"computer_name":         name,
		"ad_distinguished_name": dn,
	}, nil); err != nil {
		log.Warn("identity.report", slog.String("error", err.Error()))
	}
}

// mediaCleaned guards cleanupMediaOnce so the partition removal is attempted
// at most once per process. "absent" (already gone) and success both latch
// it; a transient error leaves it unset so a later poll retries.
var mediaCleaned bool

// cleanupMediaOnce removes the ADBOOT boot-media partition (which carries
// autounattend.xml with the admin password in plaintext) and extends C:
// into the freed space, the first time it succeeds. Best-effort.
func cleanupMediaOnce(log *slog.Logger) {
	if mediaCleaned {
		return
	}
	status := cleanupMedia()
	switch {
	case status == "": // non-Windows stub
		mediaCleaned = true
	case strings.HasPrefix(status, "error"):
		log.Warn("media.cleanup", slog.String("status", status)) // retry next poll
	default:
		mediaCleaned = true
		log.Info("media.cleanup", slog.String("status", status))
	}
}

// pollLoopResult is the return value of a pollLoop work function. It
// carries an optional suggested interval (from the server) and a flag
// indicating whether the caller should exit (e.g. self-update launched).
type pollLoopResult struct {
	suggestedInterval time.Duration
	exit              bool
}

// pollLoop is the common ticker loop shared by runSelfLoop and
// runCheckInLoop. It runs work immediately, then every interval, adding
// ±20 % random jitter to avoid thundering-herd polling. If work returns
// exit==true the loop returns. The server can change the cadence by
// returning a non-zero suggestedInterval.
func pollLoop(ctx context.Context, log *slog.Logger, interval time.Duration, work func(ctx context.Context) pollLoopResult) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	// applyInterval resets the ticker when the server advertises a different
	// cadence, so an operator changing the check-in interval in the portal
	// takes effect on the next poll without reinstalling the agent.
	applyInterval := func(tick *time.Ticker, suggested time.Duration) {
		if suggested > 0 && suggested != interval {
			log.Info("checkin.interval",
				slog.Duration("old", interval), slog.Duration("new", suggested))
			interval = suggested
			tick.Reset(interval)
		}
	}

	// First immediate poll.
	res := work(ctx)
	if res.suggestedInterval > 0 {
		interval = res.suggestedInterval
	}
	if res.exit {
		return
	}

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			res := work(ctx)
			applyInterval(tick, res.suggestedInterval)
			if res.exit {
				return
			}
			// Add ±20% random jitter to avoid thundering-herd polling.
			jitter := time.Duration(rand.Int64N(int64(interval) * 2 / 5))
			tick.Reset(interval - interval/5 + jitter)
		}
	}
}

// runSelfLoop polls runSelfOnce immediately and then every interval until
// ctx is cancelled (the service Stop handler cancels it). This is the
// resident heartbeat: the agent only needs the server URL + its agent_id.
func runSelfLoop(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, shipper *logging.Shipper, interval time.Duration) {
	// When running under the SCM, self-update must restart the SERVICE
	// (not relaunch a detached console process), or the upgraded agent
	// would run outside the service. Empty in console mode.
	svc := ""
	if isWindowsService() {
		svc = serviceName
	}
	pollLoop(ctx, log, interval, func(ctx context.Context) pollLoopResult {
		suggested := runSelfOnce(ctx, log, c, f)
		shipLogs(log, shipper, f.server, f.insecureTLS)
		if !f.noSelfUpdate && maybeSelfUpdate(ctx, log, c, f, svc) {
			log.Info("selfupdate.exiting", slog.String("note", "updater launched; exiting so the swap can complete"))
			shipLogs(log, shipper, f.server, f.insecureTLS)
			return pollLoopResult{exit: true}
		}
		return pollLoopResult{suggestedInterval: suggested}
	})
}

// packageFile is one downloadable file in a software package.
type packageFile struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
}

// softwareItem mirrors the server's AgentSoftwareItem, shared by the
// /software (legacy, by image-id) and /self (by agent-id) responses.
type softwareItem struct {
	PackageID  int64         `json:"package_id"`
	Name       string        `json:"name"`
	OrderValue int64         `json:"order_value"`
	PayloadURL string        `json:"payload_url"`
	Files      []packageFile `json:"files,omitempty"`
	// Bundles are zip archives extracted into the package work dir before
	// steps run, so their contents resolve by bare filename.
	Bundles        []packageFile          `json:"bundles,omitempty"`
	DetectionRules []swspec.DetectionRule `json:"detection_rules"`
	InstallSteps   []swspec.InstallStep   `json:"install_steps"`
}

// pkgReport is one package's deploy-time outcome, posted back to the server.
type pkgReport struct {
	PackageID int64  `json:"package_id"`
	Detected  bool   `json:"detected"`
	Installed bool   `json:"installed"`
	Skipped   bool   `json:"skipped"`
	Failed    bool   `json:"failed"`
	Message   string `json:"message,omitempty"`
}

// installPackages evaluates detection, downloads files, and runs install
// steps for each package not already present. Shared by the legacy
// deploy-time flow and the resident /self loop. Returns per-package
// reports and whether any package failed.
func installPackages(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, items []softwareItem) ([]pkgReport, bool) {
	eval := &detect.Evaluator{Backend: detect.DefaultBackend()}
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	var packageReports []pkgReport
	failed := false

	for _, pkg := range items {
		// Detection first — skip already-installed.
		installed, err := eval.EvaluatePackage(ctx, pkg.DetectionRules)
		if err != nil {
			log.Warn("detect.error",
				slog.String("package", pkg.Name),
				slog.String("error", err.Error()))
		}
		if installed {
			log.Info("package.skip",
				slog.String("actor", f.uuid),
				slog.String("target", pkg.Name),
				slog.String("reason", "detection rules report already installed"))
			packageReports = append(packageReports, pkgReport{
				PackageID: pkg.PackageID, Detected: true, Skipped: true,
			})
			continue
		}
		if len(pkg.DetectionRules) == 0 {
			log.Warn("package.no_detection",
				slog.String("package", pkg.Name),
				slog.String("note", "no detection rules; package will install every time"))
		}

		// Multi-file packages: download every file the server advertises
		// into a per-package work directory, then resolve bare filenames in
		// install-step paths against it. Legacy single-file packages (no
		// Files list) fall back to the {payload} -> pkg-N.bin path.
		pkgDir := filepath.Join(f.workDir, fmt.Sprintf("pkg-%d", pkg.PackageID))
		filesDir := filepath.Join(pkgDir, "files")
		hasWorkdir := len(pkg.Files) > 0 || len(pkg.Bundles) > 0
		var legacyPayloadPath string
		if hasWorkdir {
			if err := os.MkdirAll(filesDir, 0o755); err != nil {
				log.Error("package.workdir",
					slog.String("package", pkg.Name),
					slog.String("error", err.Error()))
				continue
			}
			downloadOK := true
			for _, pf := range pkg.Files {
				url := pf.URL
				if len(url) > 0 && url[0] == '/' {
					url = f.server + url
				}
				fdst := filepath.Join(filesDir, pf.Name)
				out, err := os.Create(fdst)
				if err != nil {
					log.Error("package.download.create",
						slog.String("package", pkg.Name),
						slog.String("file", pf.Name),
						slog.String("error", err.Error()))
					downloadOK = false
					break
				}
				if err := c.Download(ctx, url, out); err != nil {
					_ = out.Close()
					log.Error("package.download",
						slog.String("package", pkg.Name),
						slog.String("file", pf.Name),
						slog.String("error", err.Error()))
					downloadOK = false
					break
				}
				_ = out.Close()
				log.Info("package.download.ok",
					slog.String("package", pkg.Name),
					slog.String("file", pf.Name),
					slog.String("path", fdst))
			}
			// Bundles: download each zip and extract it into the work dir so
			// its contents resolve by bare filename. The unzip has zip-slip
			// defence and creates directories as needed.
			for _, bz := range pkg.Bundles {
				url := bz.URL
				if len(url) > 0 && url[0] == '/' {
					url = f.server + url
				}
				zpath := filepath.Join(pkgDir, "bundle-"+bz.Name)
				out, err := os.Create(zpath)
				if err != nil {
					log.Error("package.bundle.create",
						slog.String("package", pkg.Name), slog.String("bundle", bz.Name),
						slog.String("error", err.Error()))
					downloadOK = false
					break
				}
				if err := c.Download(ctx, url, out); err != nil {
					_ = out.Close()
					log.Error("package.bundle.download",
						slog.String("package", pkg.Name), slog.String("bundle", bz.Name),
						slog.String("error", err.Error()))
					downloadOK = false
					break
				}
				_ = out.Close()
				if err := runner.Unzip(ctx, zpath, filesDir); err != nil {
					log.Error("package.bundle.extract",
						slog.String("package", pkg.Name), slog.String("bundle", bz.Name),
						slog.String("error", err.Error()))
					downloadOK = false
					break
				}
				log.Info("package.bundle.ok",
					slog.String("package", pkg.Name), slog.String("bundle", bz.Name),
					slog.String("dir", filesDir))
			}
			if !downloadOK {
				continue
			}
			// A single plain file (no bundles) keeps the legacy {payload}
			// convenience pointing at it.
			if len(pkg.Files) == 1 && len(pkg.Bundles) == 0 {
				legacyPayloadPath = filepath.Join(filesDir, pkg.Files[0].Name)
			}
		} else if pkg.PayloadURL != "" {
			legacyPayloadPath = filepath.Join(f.workDir, fmt.Sprintf("pkg-%d.bin", pkg.PackageID))
			url := pkg.PayloadURL
			if len(url) > 0 && url[0] == '/' {
				url = f.server + url
			}
			out, err := os.Create(legacyPayloadPath)
			if err != nil {
				log.Error("package.download.create",
					slog.String("package", pkg.Name),
					slog.String("error", err.Error()))
				continue
			}
			if err := c.Download(ctx, url, out); err != nil {
				_ = out.Close()
				log.Error("package.download",
					slog.String("package", pkg.Name),
					slog.String("error", err.Error()))
				continue
			}
			_ = out.Close()
			log.Info("package.download.ok",
				slog.String("package", pkg.Name),
				slog.String("path", legacyPayloadPath))
		}

		// Rewrite, in order: substitute the legacy {payload} token; resolve
		// bare filenames against everything in the work dir (uploaded files +
		// extracted bundle contents); then expand Windows %ENV% in path fields
		// (incl. copy/unzip destinations) so e.g. %ProgramData%\... lands in
		// the real location instead of a literal "%ProgramData%" folder.
		rewritten := rewriteSteps(pkg.InstallSteps, legacyPayloadPath)
		if hasWorkdir {
			// %pkgdir% -> the work dir (any field, any step type); then resolve
			// remaining bare filenames against everything in the work dir
			// (uploaded files + extracted bundle contents).
			rewritten = expandPkgDir(rewritten, filesDir)
			knownFiles := mapWorkdirFiles(filesDir)
			rewritten = resolveBareFilenames(rewritten, knownFiles)
		}
		rewritten = expandStepEnv(rewritten)

		log.Info("package.install.start",
			slog.String("actor", f.uuid),
			slog.String("target", pkg.Name),
			slog.Int("steps", len(rewritten)))
		// Run steps from the package work dir so an installer's relative args
		// (e.g. OfficeSetup.exe /configure NoTeams.xml) and bare filenames
		// resolve against the downloaded/extracted files, not the service's CWD.
		if hasWorkdir {
			runner.WorkDir = filesDir
		} else {
			runner.WorkDir = f.workDir
		}
		results := steps.Execute(ctx, rewritten, runner)
		ok := true
		for i, r := range results {
			lvl := slog.LevelInfo
			if r.Error != nil || r.Aborted {
				lvl = slog.LevelError
				ok = false
			}
			log.Log(ctx, lvl, "package.step",
				slog.String("package", pkg.Name),
				slog.Int("step", i+1),
				slog.String("type", r.Step.Type),
				slog.Int("exit_code", r.ExitCode),
				slog.Bool("aborted", r.Aborted),
				slog.Any("error", r.Error),
			)
		}
		if ok {
			log.Info("package.install.ok",
				slog.String("actor", f.uuid),
				slog.String("target", pkg.Name))
			postDetected, _ := eval.EvaluatePackage(ctx, pkg.DetectionRules)
			packageReports = append(packageReports, pkgReport{
				PackageID: pkg.PackageID, Installed: true, Detected: postDetected,
			})
		} else {
			log.Error("package.install.fail",
				slog.String("actor", f.uuid),
				slog.String("target", pkg.Name))
			packageReports = append(packageReports, pkgReport{
				PackageID: pkg.PackageID, Failed: true,
			})
			failed = true
		}
	}
	return packageReports, failed
}

// maybeEnableBitLocker fetches the assigned PIN (if any) and enables
// BitLocker on C:. If no PIN is configured the machine is left
// unencrypted (the design's "absence of a PIN means do not encrypt"
// rule). The recovery key is escrowed back to the server; only the FACT
// of encryption is logged.
func maybeEnableBitLocker(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, identityBody map[string]any) {
	type cfgResp struct {
		PINSet bool   `json:"pin_set"`
		PIN    string `json:"pin,omitempty"`
	}
	var cfg cfgResp
	if err := c.PostJSON(ctx, "/api/v1/agent/bitlocker/config",
		map[string]any{"identity": identityBody}, &cfg); err != nil {
		log.Warn("bitlocker.config.fetch", slog.String("error", err.Error()))
		return
	}
	if !cfg.PINSet {
		log.Info("bitlocker.skip",
			slog.String("actor", f.uuid),
			slog.String("reason", "no PIN configured for this machine"))
		return
	}
	if f.dryRun {
		log.Info("bitlocker.skip",
			slog.String("actor", f.uuid),
			slog.String("reason", "--dry-run"))
		return
	}
	d := &bitlocker.Driver{}
	key, err := d.Enable(ctx, cfg.PIN)
	if err != nil {
		if errors.Is(err, bitlocker.ErrUnsupported) {
			log.Warn("bitlocker.unsupported",
				slog.String("actor", f.uuid),
				slog.String("os", runtime.GOOS),
				slog.String("note", "agent built for non-Windows host; BitLocker is Windows-only"))
			return
		}
		log.Error("bitlocker.enable.fail",
			slog.String("actor", f.uuid),
			slog.String("error", err.Error()))
		return
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/bitlocker/escrow",
		map[string]any{
			"identity":     identityBody,
			"recovery_key": key,
			"note":         "deploy",
		}, nil); err != nil {
		log.Error("bitlocker.escrow.fail",
			slog.String("actor", f.uuid),
			slog.String("error", err.Error()))
		return
	}
	// LOG ONLY THE FACT — the recovery key never appears in any log line.
	log.Info("bitlocker.enabled",
		slog.String("actor", f.uuid),
		slog.String("target", "C:"),
		slog.String("note", "recovery key escrowed; value not logged"),
	)
}

// applyOEMBranding fetches the operator's branding from the server
// and writes the relevant fields to HKLM\SOFTWARE\Microsoft\Windows\
// CurrentVersion\OEMInformation. Windows reads those keys when
// rendering System Properties; setting them is what makes the
// deployed machine look like the operator's organisation rather
// than the OEM's. No-op on non-Windows hosts (the registry path
// doesn't exist and the agent is built for Windows targets anyway).
func applyOEMBranding(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) {
	if runtime.GOOS != "windows" {
		return
	}
	type brand struct {
		OrganisationName string `json:"organisation_name"`
		SupportURL       string `json:"support_url"`
		SupportPhone     string `json:"support_phone"`
		OEMManufacturer  string `json:"oem_manufacturer"`
	}
	var b brand
	if err := c.GetJSON(ctx, "/api/v1/branding", &b); err != nil {
		log.Info("brand.fetch.skip", slog.String("reason", err.Error()))
		return
	}
	manufacturer := b.OEMManufacturer
	if manufacturer == "" {
		manufacturer = b.OrganisationName
	}
	if manufacturer == "" && b.SupportURL == "" && b.SupportPhone == "" {
		// Nothing to write.
		return
	}
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	const root = `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\OEMInformation`
	// reg.exe is the simplest tool to set these values from a
	// userland process; Set-ItemProperty would work too but reg.exe
	// is shorter to compose. Each value is best-effort.
	writes := []struct{ name, value string }{
		{"Manufacturer", manufacturer},
		{"SupportURL", b.SupportURL},
		{"SupportPhone", b.SupportPhone},
	}
	for _, w := range writes {
		if w.value == "" {
			continue
		}
		code, err := runner.Run(ctx, "reg",
			[]string{"add", root, "/v", w.name, "/d", w.value, "/t", "REG_SZ", "/f"}, "")
		if err != nil || code != 0 {
			log.Warn("brand.oem.write.fail",
				slog.String("key", w.name),
				slog.Int("exit_code", code),
				slog.String("error", errString(err)))
			continue
		}
	}
	log.Info("brand.oem.applied",
		slog.String("actor", f.uuid),
		slog.String("target", root),
	)
}

// shipLogs flushes the buffered slog records to the server. Failures
// are reported on stdout (and will sit in the buffer for the next
// attempt) but otherwise silent so the agent's exit path stays
// non-fatal.
func shipLogs(log *slog.Logger, shipper *logging.Shipper, server string, insecureTLS bool) {
	if server == "" || shipper == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := shipper.Ship(ctx, server, insecureTLS)
	if err != nil {
		log.Warn("logs.ship.fail", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		log.Info("logs.ship.ok", slog.Int("events", n))
	}
}

// runCheckInLoop is the Phase 13 resident-mode loop. The agent
// periodically calls /api/v1/agent/checkin, claims any queued bulk
// jobs, executes them, and posts results. Loops forever.
func runCheckInLoop(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, identityBody map[string]any, shipper *logging.Shipper) {
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	type bulkJob struct {
		ID      int64  `json:"id"`
		Action  string `json:"action"`
		Payload string `json:"payload"`
	}
	type checkinResp struct {
		MachineID int64     `json:"machine_id"`
		Jobs      []bulkJob `json:"jobs"`
	}

	pollLoop(ctx, log, f.checkInInterval, func(ctx context.Context) pollLoopResult {
		var resp checkinResp
		if err := c.PostJSON(ctx, "/api/v1/agent/checkin",
			map[string]any{"identity": identityBody}, &resp); err != nil {
			log.Warn("checkin.fetch", slog.String("error", err.Error()))
		} else {
			for _, j := range resp.Jobs {
				status, result := executeBulkJob(ctx, log, c, f, runner, j.Action, j.Payload)
				_ = c.PostJSON(ctx,
					fmt.Sprintf("/api/v1/agent/jobs/%d/result", j.ID),
					map[string]any{"status": status, "result_json": result}, nil)
			}
		}
		// Best-effort log ship at the end of each tick so the
		// portal sees a near-live view of resident-mode activity.
		shipLogs(log, shipper, f.server, f.insecureTLS)
		// Check for a self-update last so the in-flight bulk-job
		// loop completes before we exit. maybeSelfUpdate returns
		// true when an update was successfully launched -- the
		// updater script is now running and will restart us, so
		// the agent process must exit promptly.
		if !f.noSelfUpdate && maybeSelfUpdate(ctx, log, c, f, "") {
			log.Info("selfupdate.exiting",
				slog.String("note", "updater script spawned; exiting so the swap can complete"))
			shipLogs(log, shipper, f.server, f.insecureTLS)
			return pollLoopResult{exit: true}
		}
		return pollLoopResult{}
	})
}

// maybeSelfUpdate asks the server whether a newer agent is
// available; if so, downloads + SHA-256 verifies + spawns the
// updater script. Returns true when the updater has been launched
// (so the caller should exit). All failure paths return false so a
// transient network glitch doesn't break the check-in loop.
func maybeSelfUpdate(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, serviceName string) bool {
	var info selfupdate.UpdateInfo
	body := map[string]string{
		"os":              runtime.GOOS,
		"arch":            runtime.GOARCH,
		"current_version": Version,
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/update-info", body, &info); err != nil {
		log.Warn("selfupdate.checkfail", slog.String("error", err.Error()))
		return false
	}
	if !info.UpdateAvailable || info.URL == "" {
		return false
	}
	log.Info("selfupdate.available",
		slog.String("from", Version),
		slog.String("to", info.Current),
		slog.String("url", info.URL),
		slog.Int64("size_bytes", info.Size),
	)
	dst, err := selfupdate.SiblingPath(".new")
	if err != nil {
		log.Warn("selfupdate.path", slog.String("error", err.Error()))
		return false
	}
	if err := selfupdate.Download(ctx, info.URL, dst, info.SHA256, f.insecureTLS); err != nil {
		log.Warn("selfupdate.download", slog.String("error", err.Error()))
		return false
	}
	log.Info("selfupdate.downloaded",
		slog.String("path", dst),
		slog.String("sha256", info.SHA256),
	)
	if err := selfupdate.Swap(dst, os.Args, serviceName); err != nil {
		log.Warn("selfupdate.swap", slog.String("error", err.Error()))
		return false
	}
	return true
}

// executeBulkJob dispatches on action and returns (status, result JSON).
// Result JSON is bounded so the server can store it.
func executeBulkJob(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, runner steps.Runner, action, payload string) (string, string) {
	log.Info("bulk.start", slog.String("action", action))
	switch action {
	case "script":
		// Payload: {"shell":"cmd"|"powershell","body":"..."}
		var p struct {
			Shell string `json:"shell"`
			Body  string `json:"body"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return "failed", `{"error":"invalid payload"}`
		}
		var code int
		var rerr error
		switch p.Shell {
		case "cmd":
			code, rerr = runner.Run(ctx, "cmd", []string{"/C", p.Body}, "")
		case "powershell":
			code, rerr = runner.Run(ctx, "powershell",
				[]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-"},
				p.Body)
		default:
			return "failed", `{"error":"unknown shell"}`
		}
		if rerr != nil || code != 0 {
			return "failed", fmt.Sprintf(`{"exit_code":%d,"error":%q}`, code, errString(rerr))
		}
		return "ok", fmt.Sprintf(`{"exit_code":%d}`, code)

	case "rename":
		// Payload: {"new_name":"LAB-02"}. Real Windows rename uses
		// Rename-Computer; we wrap in PowerShell. AD coordination is
		// done server-side via the Domain Integration Service when the
		// operator creates the bulk operation (Phase 10 + 13).
		var p struct {
			NewName string `json:"new_name"`
			Find    string `json:"rename_find"`
			Replace string `json:"rename_replace"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return "failed", `{"error":"invalid payload"}`
		}
		newName := p.NewName
		if p.Find != "" {
			// Regex find/replace against this machine's CURRENT hostname,
			// so one operation renames a whole fleet consistently
			// (LAB-A-01 -> LAB-B-01, LAB-A-02 -> LAB-B-02, ...).
			re, rerr := regexp.Compile(p.Find)
			if rerr != nil {
				return "failed", fmt.Sprintf(`{"error":"bad rename regex: %s"}`, errString(rerr))
			}
			host, _ := os.Hostname()
			newName = re.ReplaceAllString(host, p.Replace)
			if newName == "" || newName == host {
				return "ok", fmt.Sprintf(`{"skipped":true,"host":%q}`, host)
			}
		}
		if newName == "" {
			return "failed", `{"error":"no new name"}`
		}
		body := fmt.Sprintf(`Rename-Computer -NewName '%s' -Force -Restart`,
			ps1Escape(newName))
		code, err := runner.Run(ctx, "powershell",
			[]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", body},
			"")
		if err != nil || code != 0 {
			return "failed", fmt.Sprintf(`{"exit_code":%d,"error":%q}`, code, errString(err))
		}
		return "ok", fmt.Sprintf(`{"renamed_to":%q}`, newName)

	case "software_push":
		// Payload: {"package_id": N}. The agent fetches that package's
		// install items -- plus its transitive dependencies, deps first --
		// from the server and installs them like any other software set.
		var p struct {
			PackageID int64 `json:"package_id"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil || p.PackageID <= 0 {
			return "failed", `{"error":"software_push payload must be {\"package_id\":N}"}`
		}
		var resp struct {
			Items    []softwareItem `json:"items"`
			Warnings []string       `json:"warnings"`
		}
		if err := c.GetJSON(ctx,
			fmt.Sprintf("/api/v1/agent/package-items?package=%d", p.PackageID), &resp); err != nil {
			return "failed", fmt.Sprintf(`{"error":%q}`, err.Error())
		}
		for _, wn := range resp.Warnings {
			log.Warn("software_push.warning", slog.String("message", wn))
		}
		reports, failed := installPackages(ctx, log, c, f, resp.Items)
		out, _ := json.Marshal(reports)
		if failed {
			return "failed", string(out)
		}
		return "ok", string(out)

	case "reimage":
		// The server has already flagged this machine for re-image
		// (reimage_pending). All the agent does is reboot; on the next
		// network boot the boot client sees the flag and auto-deploys.
		// Report ok BEFORE rebooting so the job isn't left "running"
		// (the result POST happens after this returns, but the reboot is
		// scheduled with a short delay to let that POST complete).
		if f.dryRun {
			return "ok", `{"reimage":"dry-run; reboot skipped"}`
		}
		// Set a one-time UEFI next-boot to the network/PXE entry so the
		// machine lands in AutoDeploy regardless of its persistent boot
		// order. Best-effort: if no network entry is found we still reboot
		// and rely on the firmware boot order.
		nb := setNextBootNetwork()
		log.Info("reimage.nextboot", slog.String("status", nb))
		// 15s delay: enough for the caller to POST this job result before
		// the OS goes down.
		_, _ = runner.Run(ctx, "shutdown", []string{"/r", "/t", "15", "/c", "AutoDeploy re-image"}, "")
		return "ok", `{"reimage":"reboot scheduled","next_boot":"` + nb + `"}`
	}
	return "failed", `{"error":"unknown action"}`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func ps1Escape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}

// rewriteSteps replaces the literal token "{payload}" in source/MSI/APPX/EXE
// paths with the actual on-disk path of the downloaded package.
func rewriteSteps(in []swspec.InstallStep, payload string) []swspec.InstallStep {
	out := make([]swspec.InstallStep, len(in))
	copy(out, in)
	for i := range out {
		out[i].SourcePath = strings.ReplaceAll(out[i].SourcePath, "{payload}", payload)
		out[i].MSIPath = strings.ReplaceAll(out[i].MSIPath, "{payload}", payload)
		out[i].APPXPath = strings.ReplaceAll(out[i].APPXPath, "{payload}", payload)
		out[i].ExePath = strings.ReplaceAll(out[i].ExePath, "{payload}", payload)
	}
	return out
}

// pkgDirToken matches the %pkgdir% placeholder (case-insensitive), which
// expands to the package work directory. Unlike {payload} it works in EVERY
// path field and arg of EVERY step type, including copy/unzip destinations and
// script bodies -- e.g. copy a shortcut to "%pkgdir%\..." or pass
// "/configure %pkgdir%\NoTeams.xml".
var pkgDirToken = regexp.MustCompile(`(?i)%pkgdir%`)

// expandPkgDir replaces %pkgdir% with the absolute package work dir in every
// path field, arg and script body. Literal replacement (no $-expansion) so
// Windows backslash paths pass through untouched.
func expandPkgDir(in []swspec.InstallStep, dir string) []swspec.InstallStep {
	if dir == "" {
		return in
	}
	rep := func(s string) string { return pkgDirToken.ReplaceAllLiteralString(s, dir) }
	repAll := func(a []string) []string {
		if len(a) == 0 {
			return a
		}
		o := make([]string, len(a))
		for i, s := range a {
			o[i] = rep(s)
		}
		return o
	}
	out := make([]swspec.InstallStep, len(in))
	copy(out, in)
	for i := range out {
		out[i].SourcePath = rep(out[i].SourcePath)
		out[i].DestinationPath = rep(out[i].DestinationPath)
		out[i].MSIPath = rep(out[i].MSIPath)
		out[i].APPXPath = rep(out[i].APPXPath)
		out[i].ExePath = rep(out[i].ExePath)
		out[i].ScriptBody = rep(out[i].ScriptBody)
		out[i].MSIArgs = repAll(out[i].MSIArgs)
		out[i].ExeArgs = repAll(out[i].ExeArgs)
	}
	return out
}

// resolveBareFilenames substitutes bare filenames in install-step
// path fields with their absolute path on disk. A path is treated as
// "bare" when it doesn't contain any directory separator or drive
// letter and doesn't start with a Windows env-var (%X%). Absolute
// paths and env-var paths are left untouched so the operator's
// explicit C:\... / %ProgramFiles%\... are not silently shadowed by
// a package file that happens to share a basename. The destination
// path on copy/unzip is NOT remapped -- that's where files land on
// the target, not where they come from.
func resolveBareFilenames(in []swspec.InstallStep, files map[string]string) []swspec.InstallStep {
	out := make([]swspec.InstallStep, len(in))
	copy(out, in)
	for i := range out {
		out[i].SourcePath = resolveOne(out[i].SourcePath, files)
		out[i].MSIPath = resolveOne(out[i].MSIPath, files)
		out[i].APPXPath = resolveOne(out[i].APPXPath, files)
		out[i].ExePath = resolveOne(out[i].ExePath, files)
	}
	return out
}

// mapWorkdirFiles walks the package work dir and maps each file's BASENAME to
// its absolute path, so a step can reference an uploaded file or a file from an
// extracted bundle by bare name. On a basename collision the shallowest path
// wins (a top-level installer beats a same-named file buried in a subfolder).
func mapWorkdirFiles(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // best-effort; unreadable entries are skipped
		}
		base := info.Name()
		if existing, ok := out[base]; ok {
			// Keep the shallower path (fewer separators).
			if strings.Count(path, string(os.PathSeparator)) >= strings.Count(existing, string(os.PathSeparator)) {
				return nil
			}
		}
		out[base] = path
		return nil
	})
	return out
}

// expandStepEnv expands Windows environment variables (%VAR%) in every install-
// step path field, including copy/unzip DESTINATIONS. Go's own file ops don't
// expand %VAR%, so without this a destination like %ProgramData%\... would be
// taken literally and created in the wrong place. No-op off Windows.
// envExpand is the function expandStepEnv applies; a package var so tests can
// substitute a deterministic expander (the real one is platform-specific).
var envExpand = expandEnv

func expandStepEnv(in []swspec.InstallStep) []swspec.InstallStep {
	out := make([]swspec.InstallStep, len(in))
	copy(out, in)
	for i := range out {
		out[i].SourcePath = envExpand(out[i].SourcePath)
		out[i].DestinationPath = envExpand(out[i].DestinationPath)
		out[i].MSIPath = envExpand(out[i].MSIPath)
		out[i].APPXPath = envExpand(out[i].APPXPath)
		out[i].ExePath = envExpand(out[i].ExePath)
		out[i].MSIArgs = expandEnvAll(out[i].MSIArgs)
		out[i].ExeArgs = expandEnvAll(out[i].ExeArgs)
	}
	return out
}

func expandEnvAll(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = envExpand(s)
	}
	return out
}

func resolveOne(p string, files map[string]string) string {
	if p == "" {
		return p
	}
	// Skip already-absolute paths (POSIX or Windows) and anything
	// that uses an env-var or contains a separator. The remaining
	// case is a single filename like "setup.exe" -- if we have a
	// matching upload, swap it for the absolute path; otherwise
	// leave the operator's value alone so a typo surfaces as a
	// real "file not found" at run time, not a silent skip.
	if strings.ContainsAny(p, `/\`) || strings.HasPrefix(p, "%") {
		return p
	}
	if len(p) >= 2 && p[1] == ':' { // C:foo style
		return p
	}
	if abs, ok := files[p]; ok {
		return abs
	}
	return p
}

// readSystemUUID dispatches to the platform-specific implementation
// (see uuid_windows.go and uuid_other.go). Returning "" is acceptable
// at the boundary -- the caller can fall back to the --uuid flag for
// integration testing -- but the supported deployment target
// (Windows) MUST resolve a real UUID or the server cannot identify
// the machine in inventory, BitLocker, or bulk-job lookups.
func readSystemUUID() string { return readSystemUUIDPlatform() }

func defaultWorkDir() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\work`
	}
	return "/var/lib/autodeploy/work"
}
