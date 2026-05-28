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
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/bitlocker"
	"github.com/rusketh/autodeploy/agent/internal/detect"
	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/logging"
	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

type agentFlags struct {
	server      string
	imageID     int64
	uuid        string
	insecureTLS bool
	workDir     string
	dryRun      bool
}

func main() {
	var f agentFlags
	flag.StringVar(&f.server, "server", "", "AutoDeploy server base URL")
	flag.Int64Var(&f.imageID, "image-id", 0, "Image id this machine was deployed from")
	flag.StringVar(&f.uuid, "uuid", "", "Machine SMBIOS UUID (override; defaults to read from firmware)")
	flag.BoolVar(&f.insecureTLS, "insecure-tls", false, "Skip TLS verification (dev only)")
	flag.StringVar(&f.workDir, "work", defaultWorkDir(), "Scratch directory for downloaded payloads")
	flag.BoolVar(&f.dryRun, "dry-run", false, "Log steps without executing them")
	flag.Parse()

	log := logging.New(os.Stdout, "agent")

	if f.uuid == "" {
		f.uuid = readSystemUUID()
	}
	log.Info("agent.start",
		slog.String("actor", "system"),
		slog.String("target", "self"),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("server", f.server),
		slog.String("uuid", f.uuid),
		slog.Int64("image_id", f.imageID),
		slog.Bool("dry_run", f.dryRun),
	)

	if f.server == "" || f.imageID == 0 {
		log.Info("agent.idle",
			slog.String("reason", "server or image-id not configured; nothing to do"))
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
		Items []struct {
			PackageID      int64                  `json:"package_id"`
			Name           string                 `json:"name"`
			OrderValue     int64                  `json:"order_value"`
			PayloadURL     string                 `json:"payload_url"`
			DetectionRules []swspec.DetectionRule `json:"detection_rules"`
			InstallSteps   []swspec.InstallStep   `json:"install_steps"`
		} `json:"items"`
		Warnings []string `json:"warnings,omitempty"`
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

	eval := &detect.Evaluator{Backend: detect.DefaultBackend()}
	runner := &steps.OSRunner{Log: log, DryRun: f.dryRun}

	// Report opening so the server can record an in-progress deployment.
	type pkgReport struct {
		PackageID int64  `json:"package_id"`
		Detected  bool   `json:"detected"`
		Installed bool   `json:"installed"`
		Skipped   bool   `json:"skipped"`
		Failed    bool   `json:"failed"`
		Message   string `json:"message,omitempty"`
	}
	var openResp struct {
		MachineID    int64 `json:"machine_id"`
		DeploymentID int64 `json:"deployment_id"`
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

	var packageReports []pkgReport
	failed := false

	for _, pkg := range resp.Items {
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

		// Download the installer payload to work dir.
		dst := filepath.Join(f.workDir, fmt.Sprintf("pkg-%d.bin", pkg.PackageID))
		url := pkg.PayloadURL
		if len(url) > 0 && url[0] == '/' {
			url = f.server + url
		}
		out, err := os.Create(dst)
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
			slog.String("path", dst))

		// Rewrite step source/exe paths if they reference the magic
		// "{payload}" placeholder, so steps don't have to know the disk
		// path the agent picked.
		rewritten := rewriteSteps(pkg.InstallSteps, dst)

		log.Info("package.install.start",
			slog.String("actor", f.uuid),
			slog.String("target", pkg.Name),
			slog.Int("steps", len(rewritten)))
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

	// BitLocker (Phase 12): if the server has a PIN configured for this
	// machine, enable encryption and escrow the recovery key. Off-Windows
	// or on a host without TPM/PowerShell, the agent logs and skips.
	maybeEnableBitLocker(ctx, log, c, f, identityBody)

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
	_ = time.Now // placeholder for resident-mode timing in Phase 13
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

// rewriteSteps replaces the literal token "{payload}" in source/MSI/APPX/EXE
// paths with the actual on-disk path of the downloaded package.
func rewriteSteps(in []swspec.InstallStep, payload string) []swspec.InstallStep {
	out := make([]swspec.InstallStep, len(in))
	copy(out, in)
	for i := range out {
		out[i].SourcePath = replaceToken(out[i].SourcePath, payload)
		out[i].MSIPath = replaceToken(out[i].MSIPath, payload)
		out[i].APPXPath = replaceToken(out[i].APPXPath, payload)
		out[i].ExePath = replaceToken(out[i].ExePath, payload)
	}
	return out
}

func replaceToken(s, payload string) string {
	if s == "" {
		return s
	}
	// Use a simple substring replace; avoids importing strings just for one
	// call. Cheap for short strings.
	const token = "{payload}"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if i+len(token) <= len(s) && s[i:i+len(token)] == token {
			out = append(out, payload...)
			i += len(token)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// readSystemUUID returns "" on non-Linux dev hosts; the Windows agent
// reads it via the SMBIOS APIs and the override flag is provided for
// integration testing.
func readSystemUUID() string {
	b, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
	if err != nil {
		return ""
	}
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}

func defaultWorkDir() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\work`
	}
	return "/var/lib/autodeploy/work"
}

// parseDuration kept as a stub for Phase 13 resident-mode flags.
var _ = strconv.Itoa
