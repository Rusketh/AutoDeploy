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
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/detect"
	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/logging"
	"github.com/rusketh/autodeploy/agent/internal/selfupdate"
	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
	"github.com/rusketh/autodeploy/agent/internal/winenv"
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

	// Manual install: the operator gave --server but nothing provisioned an
	// agent_id (no registry config, no flag). Enroll by SMBIOS identity to
	// obtain this machine's server-minted id and persist it the way
	// SetupComplete.cmd does at PXE deploy, so a manually added machine
	// inventories (hardware, name, AD path) and polls like any other from
	// its very first run.
	if f.server != "" && f.agentID == "" {
		ensureEnrolled(context.Background(), log, &f)
	}

	// New model: identify by the server-minted agent_id and poll
	// /api/v1/agent/self for desired state. This is how the deployed agent
	// runs -- as a Windows service, polling forever; the SCM Stop handler
	// cancels the loop. An explicit --image-id skips this in favor of the
	// deploy-time path below (deploy that image now), which converges to
	// the same self loop afterwards.
	if f.server != "" && f.agentID != "" && f.imageID == 0 {
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
		AgentID      string `json:"agent_id,omitempty"`
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
	// The report response also carries the machine's agent_id (servers
	// that predate /agent/enroll included): adopt it if enrollment didn't.
	if f.agentID == "" && openResp.AgentID != "" {
		adoptAgentID(log, &f, openResp.AgentID)
	}
	// Inventory this machine the same way the resident loop does —
	// hardware spec (SMBIOS/WMI) once, plus the observed computer name
	// and AD path. Without these a manually added machine sat in the
	// inventory as a bare UUID. Both no-op if no agent_id was acquired.
	reportHardwareOnce(ctx, log, c, f)
	reportObservedIdentity(ctx, log, c, f)

	packageReports, failed := installPackages(ctx, log, c, f, resp.Items, nil)

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

	// Resident mode. With an agent_id (enrolled above, or adopted from the
	// report response) run the full self loop — the same mode a PXE-
	// provisioned service runs: hardware/identity reporting, bulk jobs,
	// Windows Update jobs. The legacy check-in loop remains only as a
	// fallback against servers too old to mint one.
	if f.checkInInterval > 0 {
		if f.agentID != "" {
			runSelfLoop(ctx, log, c, f, shipper, f.checkInInterval)
		} else {
			runCheckInLoop(ctx, log, c, f, identityBody, shipper)
		}
	}
}

// ensureEnrolled acquires this machine's server-minted agent_id by SMBIOS
// identity and persists it (registry) so later runs — and the installed
// service — start provisioned. The full identity (make/model, base board,
// BIOS — whatever WMI could read) is sent, so even a machine that never
// PXE-boots enrolls with the facts driver filters match on. Best-effort:
// on failure (e.g. a server that predates /agent/enroll) the deploy
// path's report response is the fallback source.
func ensureEnrolled(ctx context.Context, log *slog.Logger, f *agentFlags) {
	c := httpc.New(f.server, f.uuid, f.insecureTLS)
	var resp struct {
		MachineID int64  `json:"machine_id"`
		AgentID   string `json:"agent_id"`
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/enroll",
		map[string]any{"identity": smbiosIdentityBody(f.uuid)}, &resp); err != nil {
		log.Warn("enroll.fetch", slog.String("error", err.Error()))
		return
	}
	if resp.AgentID == "" {
		log.Warn("enroll.empty", slog.String("note", "server returned no agent_id"))
		return
	}
	adoptAgentID(log, f, resp.AgentID)
}

// adoptAgentID stores a newly learned agent_id in memory and persists it
// to the registry. Persistence is best-effort: a non-elevated run still
// works for this process and re-learns the same id next time.
func adoptAgentID(log *slog.Logger, f *agentFlags, agentID string) {
	f.agentID = agentID
	if err := saveConfig(f.server, agentID); err != nil {
		log.Warn("enroll.persist", slog.String("error", err.Error()),
			slog.String("note", "agent_id active for this run only; run elevated to persist"))
		return
	}
	log.Info("enroll.ok", slog.String("agent_id", agentID))
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
	// SetupLock is true when this machine's image locks the logon screen with
	// the branded setup screen until the initial software rollout finishes.
	SetupLock bool `json:"setup_lock"`
	// UpdateJobs are pending Windows Update deployment jobs.
	UpdateJobs []updateJob `json:"update_jobs,omitempty"`
	// Sequence is the resolved event sequence to execute for an open
	// deployment, starting at SeqCursor (only the not-yet-run steps). Empty
	// means nothing left to run. The server owns the cursor, so a mid-sequence
	// reboot resumes here transparently.
	Sequence  []seqStep `json:"sequence,omitempty"`
	SeqCursor int       `json:"seq_cursor,omitempty"`
}

// seqStep mirrors the server's resolve.ResolvedStep. Exactly one of
// BuiltinAction / Task is set, selected by Kind ("builtin" | "task").
type seqStep struct {
	Kind              string   `json:"kind"`
	BuiltinAction     string   `json:"builtin_action,omitempty"`
	Task              *seqTask `json:"task,omitempty"`
	ContinueOnFailure bool     `json:"continue_on_failure,omitempty"`
}

// seqTask mirrors the server's resolve.ResolvedTask: a script/task payload plus
// its applicability filter.
type seqTask struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Shell        string `json:"shell"`
	Body         string `json:"body"`
	FilterType   string `json:"filter_type"`
	FilterValue  string `json:"filter_value"`
	SuccessCodes []int  `json:"success_codes,omitempty"`
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
	// As soon as the agent is alive and the boot client left an open
	// deployment, tell the server Windows is installed and the agent is
	// running, so the portal advances off the Setup phases immediately --
	// even if a domain-join reboot follows before software installs.
	// Setup-lock screen: while the initial deployment is open and the image
	// opted in, keep the lock marker armed and the branding fresh so the
	// credential provider shows a branded, live screen. SetupComplete.cmd armed
	// the marker at deploy time; re-arming here is belt-and-suspenders.
	if resp.SetupLock && resp.DeploymentID != 0 {
		_ = armLockMarker()
		refreshLockBranding(ctx, log, c)
	}
	if resp.DeploymentID != 0 {
		reportDeployProgress(ctx, log, c, f, resp.DeploymentID, "agent-online", 0, 0, "Agent running; Windows installed")
	}
	// Reclaim the boot-media partition (ADBOOT) once. SECURITY: it holds
	// autounattend.xml with the admin password in plaintext, so this must
	// happen on the deployed machine. Also extends C: into the freed space.
	cleanupMediaOnce(log)

	identityBody := map[string]any{"system_uuid": f.uuid}
	// setupLockReboot is set when we close a setup-lock deployment; we reboot at
	// the END of this cycle to restore the logon screen.
	setupLockReboot := false
	var packageReports []pkgReport
	var pkgFailed bool

	if resp.DeploymentID != 0 {
		// Active deployment: execute the resolved event sequence from the
		// server-owned cursor. A reboot step (or a domain-join reboot) returns
		// here and the next poll resumes at the following step. A failed step
		// with continue_on_failure marks the deployment failed but proceeds; a
		// failed step without it pauses the sequence (the cursor is not
		// advanced) so the next poll retries — transient failures (e.g. a
		// domain controller not yet reachable) recover on their own.
		sr := executeSequence(ctx, log, c, f, resp)
		if sr.rebooting || sr.paused {
			return 0
		}
		packageReports = sr.packageReports
		pkgFailed = sr.failed
	} else if len(resp.Software) > 0 {
		// Resident mode (the deployment is already closed): keep installing the
		// effective software set idempotently — detection skips already-
		// installed packages — so newly added software converges without an
		// operator-created job, exactly as before sequences existed.
		onProgress := func(done, total int, name string) {
			notes := "Installing software"
			if name != "" {
				notes = "Installing " + name
			}
			reportDeployProgress(ctx, log, c, f, resp.DeploymentID, "installing", done, total, notes)
		}
		packageReports, pkgFailed = installPackages(ctx, log, c, f, resp.Software, onProgress)
	}

	// If the server told us about an open deployment (opened by the boot
	// client), report the final outcome once the whole sequence has run so the
	// dashboard row transitions from "in_progress" to "ok" or "failed". This
	// fires at most once per deployment: after reporting, the server's next
	// /self response won't include the deployment_id anymore.
	if resp.DeploymentID != 0 {
		outcome := "ok"
		if pkgFailed {
			outcome = "failed"
		}
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
			// Initial rollout finished: drop the setup-lock marker and remember
			// to reboot at the end of this cycle. A reboot (not just clearing
			// the marker) is REQUIRED to restore the logon: LogonUI evaluates
			// the credential provider only at load, so once the lock screen is
			// up, clearing the marker alone won't make it re-enumerate -- the
			// screen would linger until the machine restarts.
			if resp.SetupLock {
				writeLockStatus("done", "Setup complete — restarting…", 1, 1)
				clearLockMarker()
				setupLockReboot = true
			}
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
	// Process Windows Update deployment jobs.
	var updateRes updateBatchResult
	if len(resp.UpdateJobs) > 0 {
		updateRes = processUpdateJobs(ctx, log, c, f, resp.UpdateJobs)
	}
	// KB scan: every 10th poll, plus immediately after a successful install
	// so compliance reflects the new state without waiting for the cycle.
	kbScanCounter++
	if kbScanCounter%10 == 1 || updateRes.AnyInstalled {
		reportKBScan(ctx, log, c, f)
	}
	// Setup-lock: the deployment is closed and all post-install work is done, so
	// reboot to bring the machine back to a clean Windows logon for first use.
	// The marker was already cleared above.
	if setupLockReboot {
		scheduleSetupLockReboot(ctx, log, f.dryRun, "AutoDeploy setup complete")
	} else if updateRes.RebootNeeded {
		// An installed update demands a reboot and its operator opted in via
		// reboot_after. The setup-lock reboot above covers this case when
		// both trigger in one cycle.
		scheduleUpdateReboot(ctx, log, f.dryRun)
	}
	if resp.PollIntervalSeconds > 0 {
		return time.Duration(resp.PollIntervalSeconds) * time.Second
	}
	return 0
}

// reportDeployProgress posts a best-effort live-progress update for an open
// deployment so the portal's bar advances through the agent-run phases
// (agent-online, then installing done/total) without closing the row -- the
// terminal ok/failed still goes via /api/v1/agent/report. A no-op when there is
// no open deployment (depID == 0); failures are logged, never fatal.
func reportDeployProgress(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, depID int64, phase string, done, total int, notes string) {
	if depID == 0 {
		return
	}
	// Mirror the live progress onto the local setup-lock screen when the lock
	// is armed, so the credential provider's bar + activity stay in lockstep
	// with the portal. No-op (a cheap Stat) when no lock is active.
	if lockMarkerPresent() {
		writeLockStatus(phase, notes, done, total)
	}
	var ignore struct{}
	if err := c.PostJSON(ctx, "/api/v1/agent/deploy-progress", map[string]any{
		"identity":      map[string]any{"system_uuid": f.uuid},
		"deployment_id": depID,
		"phase":         phase,
		"done":          done,
		"total":         total,
		"notes":         notes,
	}, &ignore); err != nil {
		log.Warn("deploy.progress", slog.String("error", err.Error()))
	}
}

// scheduleSetupLockReboot restarts the machine so LogonUI reloads and, finding
// no lock marker, shows the normal Windows logon. Used both when the initial
// rollout finishes and when a technician unlocks: in either case the displayed
// lock screen can only be dismissed by re-evaluating the credential provider,
// which happens at boot. Best-effort; a no-op under --dry-run.
func scheduleSetupLockReboot(ctx context.Context, log *slog.Logger, dryRun bool, reason string) {
	if dryRun {
		log.Info("setuplock.reboot.skip", slog.String("reason", "--dry-run"))
		return
	}
	runner := &steps.OSRunner{Log: log, DryRun: dryRun}
	if _, err := runner.Run(ctx, "shutdown", []string{"/r", "/t", "10", "/c", reason}, ""); err != nil {
		log.Warn("setuplock.reboot", slog.String("error", err.Error()))
		return
	}
	log.Info("setuplock.reboot", slog.String("note", reason))
}

// seqResult is the outcome of executing (part of) the resolved event sequence
// in one poll.
type seqResult struct {
	rebooting      bool        // a step scheduled a reboot; resume next poll
	paused         bool        // a step failed (no continue_on_failure); retry next poll
	failed         bool        // at least one continue_on_failure step failed
	packageReports []pkgReport // reports from any software step, for the deploy close
}

// executeSequence runs the resolved event sequence for an open deployment,
// starting at resp.SeqCursor. After each completed step it advances the
// server-owned cursor via /api/v1/agent/sequence-progress, so a reboot resumes
// at the following step. Steps run in order; a failure without
// continue_on_failure pauses the sequence (the cursor is not advanced) so the
// next poll retries — which is what lets transient failures (a domain
// controller not yet reachable) recover on their own.
func executeSequence(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, resp selfResponse) seqResult {
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	facts := hostFacts(log)
	var res seqResult

	advance := func(absIdx int, status string) {
		postSeqProgress(ctx, log, c, f, resp.DeploymentID, absIdx, status)
	}

	for i, step := range resp.Sequence {
		absIdx := resp.SeqCursor + i
		log.Info("sequence.step",
			slog.Int("index", absIdx),
			slog.String("kind", step.Kind),
			slog.String("builtin", step.BuiltinAction))

		switch step.Kind {
		case "builtin":
			switch step.BuiltinAction {
			case "software":
				onProgress := func(done, total int, name string) {
					notes := "Installing software"
					if name != "" {
						notes = "Installing " + name
					}
					reportDeployProgress(ctx, log, c, f, resp.DeploymentID, "installing", done, total, notes)
				}
				reports, failed := installPackages(ctx, log, c, f, resp.Software, onProgress)
				res.packageReports = append(res.packageReports, reports...)
				if failed {
					res.failed = true
					if !step.ContinueOnFailure {
						res.paused = true
						return res
					}
					advance(absIdx, "failed")
					continue
				}
				advance(absIdx, "ok")

			case "domainjoin":
				switch domainJoinStep(ctx, log, c, f) {
				case djReboot:
					advance(absIdx, "ok")
					res.rebooting = true
					return res
				case djFailed:
					if step.ContinueOnFailure {
						res.failed = true
						advance(absIdx, "failed")
						continue
					}
					res.paused = true
					return res
				default: // djContinue
					advance(absIdx, "ok")
				}

			case "reboot":
				// Advance BEFORE rebooting so the next poll resumes past this
				// step; the reboot has a short delay to let the POST land.
				advance(absIdx, "ok")
				scheduleSequenceReboot(ctx, log, f.dryRun, "AutoDeploy sequence reboot")
				res.rebooting = true
				return res

			case "gpupdate":
				code, err := runner.Run(ctx, "gpupdate", []string{"/force"}, "")
				if err != nil || code != 0 {
					log.Warn("sequence.gpupdate.fail", slog.Int("exit_code", code), slog.Any("error", err))
					res.failed = true
					if !step.ContinueOnFailure {
						res.paused = true
						return res
					}
					advance(absIdx, "failed")
					continue
				}
				advance(absIdx, "ok")

			default:
				log.Warn("sequence.builtin.unknown", slog.String("action", step.BuiltinAction))
				advance(absIdx, "skipped")
			}

		case "task":
			if step.Task == nil {
				advance(absIdx, "skipped")
				continue
			}
			if !taskFilterApplies(ctx, log, runner, step.Task, facts) {
				log.Info("sequence.task.skip",
					slog.String("task", step.Task.Name),
					slog.String("filter", step.Task.FilterType))
				advance(absIdx, "skipped")
				continue
			}
			if f.dryRun {
				log.Info("sequence.task.dryrun", slog.String("task", step.Task.Name))
				advance(absIdx, "ok")
				continue
			}
			code, err := runner.RunScript(ctx, step.Task.Shell, step.Task.Body)
			if err != nil || !isSuccessCode(code, step.Task.SuccessCodes) {
				log.Error("sequence.task.fail",
					slog.String("task", step.Task.Name),
					slog.Int("exit_code", code), slog.Any("error", err))
				res.failed = true
				if !step.ContinueOnFailure {
					res.paused = true
					return res
				}
				advance(absIdx, "failed")
				continue
			}
			log.Info("sequence.task.ok", slog.String("task", step.Task.Name), slog.Int("exit_code", code))
			advance(absIdx, "ok")

		default:
			log.Warn("sequence.kind.unknown", slog.String("kind", step.Kind))
			advance(absIdx, "skipped")
		}
	}
	return res
}

// postSeqProgress advances the deployment's sequence cursor past a finished
// step. Best-effort — a failed post just means the step re-runs next poll.
func postSeqProgress(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, depID int64, absIdx int, status string) {
	if depID == 0 {
		return
	}
	var ignore struct{}
	if err := c.PostJSON(ctx, "/api/v1/agent/sequence-progress", map[string]any{
		"deployment_id":   depID,
		"completed_index": absIdx,
		"status":          status,
	}, &ignore); err != nil {
		log.Warn("sequence.progress", slog.String("error", err.Error()))
	}
}

// scheduleSequenceReboot restarts the machine to continue the sequence after
// boot. Best-effort; a no-op under --dry-run.
func scheduleSequenceReboot(ctx context.Context, log *slog.Logger, dryRun bool, reason string) {
	if dryRun {
		log.Info("sequence.reboot.skip", slog.String("reason", "--dry-run"))
		return
	}
	runner := &steps.OSRunner{Log: log, DryRun: dryRun}
	if _, err := runner.Run(ctx, "shutdown", []string{"/r", "/t", "15", "/c", reason}, ""); err != nil {
		log.Warn("sequence.reboot", slog.String("error", err.Error()))
		return
	}
	log.Info("sequence.reboot", slog.String("note", reason))
}

// isSuccessCode reports whether code is in the task's success set (default {0}).
func isSuccessCode(code int, success []int) bool {
	if len(success) == 0 {
		return code == 0
	}
	for _, s := range success {
		if s == code {
			return true
		}
	}
	return false
}

// taskFilterApplies evaluates a task's applicability filter on this host:
//   - ""     always applies
//   - "os"   OS-caption substring (case-insensitive)
//   - "wmic" a WMI (WQL) query that must return at least one instance
//   - "ps1"  a PowerShell snippet that must exit 0
// A filter that errors is treated as NOT applicable (the step is skipped) so a
// broken predicate never silently runs a step on the wrong machine.
func taskFilterApplies(ctx context.Context, log *slog.Logger, runner steps.Runner, t *seqTask, facts steps.HostFacts) bool {
	switch t.FilterType {
	case "", "none":
		return true
	case "os":
		return strings.Contains(strings.ToLower(facts.OSCaption), strings.ToLower(strings.TrimSpace(t.FilterValue)))
	case "ps1":
		code, err := runner.RunScript(ctx, "powershell", t.FilterValue)
		return err == nil && code == 0
	case "wmic":
		// Evaluate the WQL query via CIM (the modern replacement for wmic.exe):
		// exit 0 when it returns at least one instance.
		body := "$q = @'\n" + t.FilterValue + "\n'@\n" +
			"if (@(Get-CimInstance -Query $q -ErrorAction Stop).Count -gt 0) { exit 0 } else { exit 1 }"
		code, err := runner.RunScript(ctx, "powershell", body)
		return err == nil && code == 0
	default:
		log.Warn("sequence.task.filter.unknown", slog.String("filter", t.FilterType))
		return true
	}
}

// domainJoined guards domainJoinStep so a successful join (which schedules a
// reboot) isn't re-attempted later in the same process.
var domainJoined bool

// djStatus is the outcome of a domain-join sequence step.
type djStatus int

const (
	djContinue djStatus = iota // nothing to do / already a member — advance
	djReboot                   // join succeeded; a reboot is scheduled — resume next poll
	djFailed                   // join attempt failed — should be retried
)

// domainJoinStep asks the server whether this machine's image is configured for
// agent-driven AD join and, if so and the machine isn't already in that domain,
// joins it and schedules a reboot. Credentials are fetched per-call and NEVER
// logged. Returns djReboot when a reboot was scheduled, djFailed when the join
// attempt failed (caller decides whether to retry), and djContinue otherwise
// (not configured, already a member, or dry-run).
func domainJoinStep(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) djStatus {
	if domainJoined || f.agentID == "" {
		log.Info("domainjoin.skip",
			slog.Bool("already_joined", domainJoined),
			slog.Bool("no_agent_id", f.agentID == ""))
		return djContinue
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
		return djFailed
	}
	log.Info("domainjoin.response",
		slog.Bool("join", resp.Join),
		slog.String("domain", resp.Domain),
		slog.Bool("has_user", resp.User != ""),
		slog.Bool("has_password", resp.Password != ""))
	if !resp.Join || resp.Domain == "" {
		return djContinue
	}
	if cur, joined := currentDomain(); joined && domainMatches(cur, resp.Domain) {
		log.Info("domainjoin.already_member",
			slog.String("current_domain", cur))
		domainJoined = true // already a member of the target domain
		return djContinue
	}
	if f.dryRun {
		log.Info("domainjoin.skip",
			slog.String("reason", "--dry-run"), slog.String("domain", resp.Domain))
		return djContinue
	}
	if err := joinDomain(resp.Domain, resp.OU, resp.User, resp.Password); err != nil {
		log.Error("domainjoin.fail",
			slog.String("domain", resp.Domain), slog.String("error", err.Error()))
		return djFailed // retry on the next poll
	}
	domainJoined = true
	// LOG ONLY THE FACT — credentials never appear in any log line.
	log.Info("domainjoin.ok",
		slog.String("domain", resp.Domain), slog.String("ou", resp.OU),
		slog.String("note", "rebooting to complete join"))
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	_, _ = runner.Run(ctx, "shutdown", []string{"/r", "/t", "15", "/c", "AutoDeploy domain join"}, "")
	return djReboot
}

// domainMatches reports whether the machine's CURRENT domain (as read from
// Win32_ComputerSystem.Domain -- typically the DNS FQDN, e.g.
// "corp.example.com") refers to the same domain the operator configured for
// the image, which may be given as the NetBIOS short name ("CORP") or the
// FQDN. A bare case-insensitive equality misses the FQDN-vs-NetBIOS case, so
// a machine that joined successfully is never recognised as a member: the
// agent re-fetches credentials and re-attempts the join on every poll, and
// (because a join schedules a reboot) the box can get stuck rebooting instead
// of checking in. Matching the leading DNS label against the short name fixes
// that without loosening the comparison to unrelated domains.
func domainMatches(current, target string) bool {
	current = strings.TrimSpace(current)
	target = strings.TrimSpace(target)
	if current == "" || target == "" {
		return false
	}
	if strings.EqualFold(current, target) {
		return true
	}
	// Two distinct FQDNs (both dotted) are genuinely different domains --
	// never fuzzy-match them, or "corp.example.com" would wrongly equal
	// "corp.contoso.com". The NetBIOS/FQDN reconciliation only applies when
	// exactly one side is a bare short name (NetBIOS names carry no dots).
	if strings.Contains(current, ".") && strings.Contains(target, ".") {
		return false
	}
	firstLabel := func(s string) string {
		if i := strings.IndexByte(s, '.'); i >= 0 {
			return s[:i]
		}
		return s
	}
	return strings.EqualFold(firstLabel(current), firstLabel(target))
}

// hardwareReported guards reportHardwareOnce so the WMI sweep runs at most
// once per agent process (specs rarely change; a service restart re-reports).
var hardwareReported bool

// reportHardwareOnce collects and posts the machine's hardware spec the
// first time it's called in this process. Needs the agent_id (the server
// keys hardware by it). The SMBIOS identity rides along so a manually-
// installed machine — whose only voice is this agent, never the PXE boot
// client — still gets its make/model and base-board facts into inventory
// for driver-filter building. All failures are logged and swallowed.
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
		map[string]any{
			"agent_id": f.agentID,
			"hardware": hw,
			"identity": smbiosIdentityBody(f.uuid),
		}, nil); err != nil {
		log.Warn("hardware.report", slog.String("error", err.Error()))
		return // leave the guard unset so the next poll retries
	}
	hardwareReported = true
	log.Info("hardware.reported")
}

// smbiosIdentityCached holds the one-per-process result of the WMI SMBIOS
// sweep; the query costs a PowerShell start, so callers share it.
var smbiosIdentityCached map[string]any
var smbiosIdentityFetched bool

// smbiosIdentityBody returns the agent's best-known SMBIOS identity for
// server upserts: whatever WMI could read (nil fields simply absent, and
// nothing at all off Windows) plus the UUID the agent already trusts —
// which always wins over anything the sweep returned.
func smbiosIdentityBody(uuid string) map[string]any {
	if !smbiosIdentityFetched {
		smbiosIdentityCached = collectSMBIOSIdentity()
		smbiosIdentityFetched = true
	}
	id := map[string]any{"system_uuid": uuid}
	for k, v := range smbiosIdentityCached {
		if k != "system_uuid" {
			id[k] = v
		}
	}
	return id
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
	// Service technician-unlock PIN checks for the setup-lock screen for the
	// lifetime of the resident loop. Cheap and harmless when no lock is active.
	// A valid PIN clears the marker and reboots to a clean logon (the displayed
	// lock screen can't be dismissed in place -- LogonUI re-evaluates only at
	// boot).
	go watchLockPINRequests(ctx, log, serverPINValidator(c, f), func() {
		clearLockMarker()
		scheduleSetupLockReboot(ctx, log, f.dryRun, "AutoDeploy technician unlock")
	})
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

// osFacts memoises the host's OS caption/version so the (PowerShell) query
// behind collectOSFacts runs at most once per agent process -- the OS doesn't
// change under a running agent. Guarded by a mutex because installs from the
// deploy flow and the resident loop could in principle overlap.
var (
	osFactsMu     sync.Mutex
	osFactsCached steps.HostFacts
)

// hostFacts returns the target's OS facts, querying once and caching the
// result. A failed query (empty caption) is not cached, so a transient
// PowerShell hiccup is retried on the next install run rather than disabling
// OS filtering for the life of the process.
func hostFacts(log *slog.Logger) steps.HostFacts {
	osFactsMu.Lock()
	defer osFactsMu.Unlock()
	if osFactsCached.OSCaption != "" {
		return osFactsCached
	}
	caption, version := collectOSFacts()
	facts := steps.HostFacts{OSCaption: caption, OSVersion: version}
	if caption != "" {
		osFactsCached = facts
		if log != nil {
			log.Info("install.host_os",
				slog.String("actor", "agent"),
				slog.String("os_caption", caption),
				slog.String("os_version", version))
		}
	}
	return facts
}

// installPackages evaluates detection, downloads files, and runs install
// steps for each package not already present. Shared by the legacy
// deploy-time flow and the resident /self loop. Returns per-package
// reports and whether any package failed.
// installPackages installs each item, returning a per-package report and an
// overall failed flag. progress, if non-nil, is called once before each package
// (done = packages finished so far, total = len(items), name = the upcoming
// package) and once more at the end with done == total, so a caller can drive a
// live "installing (done/total)" progress indicator. It is best-effort: a
// progress callback never affects the install outcome.
func installPackages(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, items []softwareItem, progress func(done, total int, name string)) ([]pkgReport, bool) {
	eval := &detect.Evaluator{Backend: detect.DefaultBackend(), Log: log}
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	// Gather the host's OS facts once (an OS query isn't free) so per-step
	// FilterOS can be evaluated without re-querying for every package.
	facts := hostFacts(log)
	var packageReports []pkgReport
	failed := false

	for i, pkg := range items {
		if progress != nil {
			progress(i, len(items), pkg.Name)
		}
		rep, didFail := installOnePackage(ctx, log, c, f, eval, runner, pkg, facts)
		if rep != nil {
			packageReports = append(packageReports, *rep)
		}
		if didFail {
			failed = true
		}
	}
	if progress != nil && len(items) > 0 {
		progress(len(items), len(items), "")
	}
	return packageReports, failed
}

// installOnePackage evaluates detection for pkg, downloads its payload, runs
// its install steps and returns the report to post back -- or nil when the
// package was neither installed nor skipped (e.g. a download error) -- plus
// whether it failed. The downloaded payload (the per-package work dir and any
// legacy single-file pkg-N.bin) is removed on the way out: once the steps have
// run, leaving it on disk just wastes space on the target.
func installOnePackage(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, eval *detect.Evaluator, runner *steps.OSRunner, pkg softwareItem, facts steps.HostFacts) (*pkgReport, bool) {
	pkgDir := filepath.Join(f.workDir, fmt.Sprintf("pkg-%d", pkg.PackageID))
	filesDir := filepath.Join(pkgDir, "files")
	legacyBinPath := filepath.Join(f.workDir, fmt.Sprintf("pkg-%d.bin", pkg.PackageID))
	defer func() {
		_ = os.RemoveAll(pkgDir)
		_ = os.Remove(legacyBinPath)
	}()

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
		return &pkgReport{PackageID: pkg.PackageID, Detected: true, Skipped: true}, false
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
	hasWorkdir := len(pkg.Files) > 0 || len(pkg.Bundles) > 0
	var legacyPayloadPath string
	if hasWorkdir {
		if err := os.MkdirAll(filesDir, 0o755); err != nil {
			log.Error("package.workdir",
				slog.String("package", pkg.Name),
				slog.String("error", err.Error()))
			return nil, false
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
			return nil, false
		}
		// A single plain file (no bundles) keeps the legacy {payload}
		// convenience pointing at it.
		if len(pkg.Files) == 1 && len(pkg.Bundles) == 0 {
			legacyPayloadPath = filepath.Join(filesDir, pkg.Files[0].Name)
		}
	} else if pkg.PayloadURL != "" {
		legacyPayloadPath = legacyBinPath
		url := pkg.PayloadURL
		if len(url) > 0 && url[0] == '/' {
			url = f.server + url
		}
		out, err := os.Create(legacyPayloadPath)
		if err != nil {
			log.Error("package.download.create",
				slog.String("package", pkg.Name),
				slog.String("error", err.Error()))
			return nil, false
		}
		if err := c.Download(ctx, url, out); err != nil {
			_ = out.Close()
			log.Error("package.download",
				slog.String("package", pkg.Name),
				slog.String("error", err.Error()))
			return nil, false
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
		slog.String("host_os", facts.OSCaption),
		slog.Int("steps", len(rewritten)))
	// Run steps from the package work dir so an installer's relative args
	// (e.g. OfficeSetup.exe /configure NoTeams.xml) and bare filenames
	// resolve against the downloaded/extracted files, not the service's CWD.
	if hasWorkdir {
		runner.WorkDir = filesDir
	} else {
		runner.WorkDir = f.workDir
	}
	results := steps.Execute(ctx, rewritten, runner, facts)
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
			// A step skipped because its FilterOS didn't match the host is
			// recorded so an operator can see WHY a step didn't run (e.g. the
			// Windows 10 zip step was passed over on a Windows 11 machine)
			// rather than wondering whether it silently failed.
			slog.Bool("skipped", r.Skipped),
			slog.String("filter_os", r.Step.FilterOS),
			slog.Any("error", r.Error),
		)
	}
	if ok {
		log.Info("package.install.ok",
			slog.String("actor", f.uuid),
			slog.String("target", pkg.Name))
		postDetected, _ := eval.EvaluatePackage(ctx, pkg.DetectionRules)
		return &pkgReport{PackageID: pkg.PackageID, Installed: true, Detected: postDetected}, false
	}
	log.Error("package.install.fail",
		slog.String("actor", f.uuid),
		slog.String("target", pkg.Name))
	return &pkgReport{PackageID: pkg.PackageID, Failed: true}, true
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
		case "cmd", "powershell":
			// Run from a script file (not inline) so multi-line bodies work
			// and PowerShell gets its ExecutionPolicy bypassed.
			code, rerr = runner.RunScript(ctx, p.Shell, p.Body)
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
		reports, failed := installPackages(ctx, log, c, f, resp.Items, nil)
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
// taken literally and created in the wrong place. winenv.Expand also resolves
// the synthetic Program Files variables a service's environment can lack (e.g.
// %ProgramFiles(x86)%), so install paths match detection.
// envExpand is the function expandStepEnv applies; a package var so tests can
// substitute a deterministic expander (the real one is platform-specific).
var envExpand = winenv.Expand

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
// the machine in inventory or bulk-job lookups.
func readSystemUUID() string { return readSystemUUIDPlatform() }

func defaultWorkDir() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\work`
	}
	return "/var/lib/autodeploy/work"
}
