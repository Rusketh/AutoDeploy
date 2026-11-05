package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/runas"
	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// Tool Kit jobs. A "toolkit" bulk job names a job the server has already
// marked running for this machine; the agent fetches the kit's definition
// (files, parameter-expanded steps, Run As mode, timeout, reboot policy) and,
// only when it runs as a specific account, that account's password -- each
// bound to this job and refused once it finishes. The steps run once, in
// order, with no detection rules; each step's exit code and output tail are
// reported back as the job result.

// toolkitJobDef mirrors the server's AgentToolkitJobResponse.
type toolkitJobDef struct {
	JobID          int64        `json:"job_id"`
	ToolkitID      int64        `json:"toolkit_id"`
	Name           string       `json:"name"`
	Revision       int          `json:"revision"`
	QueuedRevision int          `json:"queued_revision,omitempty"`
	RunAs          string       `json:"run_as"`
	RunAsUser      string       `json:"run_as_user,omitempty"`
	NeedsPassword  bool         `json:"needs_password"`
	TimeoutMinutes int          `json:"timeout_minutes"`
	RebootPolicy   string       `json:"reboot_policy"`
	Item           softwareItem `json:"item"`
}

// toolkitStepResult / toolkitResult are the job result_json (the server
// decodes them as model.ToolkitStepResult / model.ToolkitResult).
type toolkitStepResult struct {
	Step        int    `json:"step"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	AsSystem    bool   `json:"as_system,omitempty"`
	ExitCode    int    `json:"exit_code"`
	Skipped     bool   `json:"skipped,omitempty"`
	Error       string `json:"error,omitempty"`
	Output      string `json:"output,omitempty"`
}

type toolkitResult struct {
	ToolkitID int64               `json:"toolkit_id"`
	Revision  int                 `json:"revision"`
	RunAs     string              `json:"run_as"`
	RunAsUser string              `json:"run_as_user,omitempty"`
	Steps     []toolkitStepResult `json:"steps,omitempty"`
	TimedOut  bool                `json:"timed_out,omitempty"`
	Reboot    string              `json:"reboot,omitempty"`
	Error     string              `json:"error,omitempty"`
	DurationS int                 `json:"duration_s,omitempty"`
}

const (
	runAsSystem       = "system"
	runAsUser         = "user"
	runAsLoggedOnUser = "logged_on_user"

	rebootIfRequested = "if_requested"
	rebootAlways      = "always"

	// toolkitOutputTail bounds each step's captured output in the result.
	toolkitOutputTail = 4096
	// toolkitMaxStepsReported bounds the number of step results sent back.
	toolkitMaxStepsReported = 200
	// toolkitRebootDelay gives the agent time to post the result first.
	toolkitRebootDelay = 60
)

// runAsSession is what a Tool Kit needs from a logged-on account; satisfied
// by *runas.Session, faked in tests.
type runAsSession interface {
	steps.Launcher
	Expand(string) string
	GrantDir(string) error
	Close()
}

// openRunAs logs on the Run As account (mode "user") or attaches to the
// signed-in user ("logged_on_user"). It returns the session and the account
// name to report. A package var so tests can fake it.
var openRunAs = func(mode, account, password string) (runAsSession, string, error) {
	var s *runas.Session
	var err error
	switch mode {
	case runAsUser:
		s, err = runas.Logon(account, password)
	case runAsLoggedOnUser:
		s, err = runas.LoggedOnUser()
	default:
		return nil, "", fmt.Errorf("unknown run-as mode %q", mode)
	}
	if err != nil {
		return nil, "", err
	}
	return s, s.Account, nil
}

// rebootScheduler schedules the post-run restart; a package var for tests.
var rebootScheduler = func(ctx context.Context, r steps.Runner, name string) error {
	code, err := r.Run(ctx, "shutdown",
		[]string{"/r", "/t", fmt.Sprint(toolkitRebootDelay), "/c", "AutoDeploy tool kit: " + name}, "")
	if err == nil && code != 0 {
		err = fmt.Errorf("shutdown exited %d", code)
	}
	return err
}

// runToolkitJob runs one toolkit bulk job and returns (status, result JSON).
func runToolkitJob(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, jobID int64) (string, string) {
	started := time.Now()
	res := toolkitResult{}
	finish := func(status string) (string, string) {
		res.DurationS = int(time.Since(started).Seconds())
		if len(res.Steps) > toolkitMaxStepsReported {
			res.Steps = res.Steps[:toolkitMaxStepsReported]
		}
		b, err := json.Marshal(res)
		if err != nil {
			return "failed", `{"error":"couldn't encode the tool kit result"}`
		}
		return status, string(b)
	}
	fail := func(msg string) (string, string) {
		res.Error = msg
		log.Error("toolkit.fail", slog.Int64("job", jobID),
			slog.String("target", res.toolkitTarget()), slog.String("error", msg))
		return finish("failed")
	}
	if f.agentID == "" {
		return fail("this agent has no agent_id, so it can't fetch a tool kit job")
	}
	req := map[string]any{"agent_id": f.agentID, "job_id": jobID}

	var def toolkitJobDef
	if err := c.PostJSON(ctx, "/api/v1/agent/toolkit-job", req, &def); err != nil {
		return fail("couldn't fetch the tool kit: " + err.Error())
	}
	res.ToolkitID, res.Revision = def.ToolkitID, def.Revision
	res.RunAs, res.RunAsUser = def.RunAs, def.RunAsUser
	if res.RunAs == "" {
		res.RunAs = runAsSystem
	}
	log.Info("toolkit.start", slog.Int64("job", jobID), slog.String("target", def.Name),
		slog.Int("revision", def.Revision), slog.String("run_as", res.RunAs),
		slog.String("account", def.RunAsUser), slog.Int("steps", len(def.Item.InstallSteps)))

	runCtx := ctx
	if def.TimeoutMinutes > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(def.TimeoutMinutes)*time.Minute)
		defer cancel()
	}

	// Log the account on (or attach to the signed-in user) before anything
	// is downloaded, so a bad password fails fast.
	var sess runAsSession
	if res.RunAs == runAsUser || res.RunAs == runAsLoggedOnUser {
		if f.dryRun {
			log.Info("toolkit.runas.dry_run", slog.String("run_as", res.RunAs), slog.String("account", def.RunAsUser))
		} else {
			password := ""
			if def.NeedsPassword {
				var cred struct {
					User     string `json:"user"`
					Password string `json:"password"`
				}
				if err := c.PostJSON(ctx, "/api/v1/agent/job-credential", req, &cred); err != nil {
					return fail("couldn't get the Run As password: " + err.Error())
				}
				password = cred.Password
			}
			s, account, err := openRunAs(res.RunAs, def.RunAsUser, password)
			password = "" // used once; never kept or logged
			if err != nil {
				log.Error("toolkit.runas.fail", slog.String("run_as", res.RunAs),
					slog.String("account", def.RunAsUser), slog.String("error", err.Error()))
				if errors.Is(err, runas.ErrNoInteractiveUser) {
					return fail("nobody is signed in to this machine, so the tool kit can't run as the logged-on user")
				}
				return fail("couldn't run as " + runAsLabel(res.RunAs, def.RunAsUser) + ": " + err.Error())
			}
			defer s.Close()
			sess = s
			if account != "" {
				res.RunAsUser = account
			}
			log.Info("toolkit.runas.ok", slog.String("run_as", res.RunAs), slog.String("account", res.RunAsUser))
		}
	}

	// Download into a per-job work folder (removed afterwards).
	pkgDir := filepath.Join(f.workDir, fmt.Sprintf("toolkit-job-%d", jobID))
	filesDir := filepath.Join(pkgDir, "files")
	defer func() { _ = os.RemoveAll(pkgDir) }()
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return fail("couldn't create the work folder: " + err.Error())
	}
	sysRunner := &steps.OSRunner{Log: log, DryRun: f.dryRun}
	hasWorkdir, legacyPath, ok := fetchPackagePayload(runCtx, log, c, f, sysRunner, def.Item, pkgDir, filesDir,
		filepath.Join(pkgDir, "payload.bin"))
	if !ok {
		return fail("couldn't download the tool kit's files")
	}
	workDir := pkgDir
	if hasWorkdir {
		workDir = filesDir
	}
	if sess != nil {
		if err := sess.GrantDir(pkgDir); err != nil {
			return fail("couldn't give " + res.RunAsUser + " access to the work folder: " + err.Error())
		}
	}

	prepared := rewriteSteps(def.Item.InstallSteps, legacyPath)
	if hasWorkdir {
		prepared = expandPkgDir(prepared, filesDir)
		prepared = resolveBareFilenames(prepared, mapWorkdirFiles(filesDir))
	}
	appxDir := ""
	if hasWorkdir {
		appxDir = filesDir
	}

	facts := hostFacts(log)
	failed, rebootRequested := false, false
	for i, s := range prepared {
		asSystem := sess == nil || s.RunAsSystem
		one := []swspec.InstallStep{s}
		if asSystem {
			one = finalizeAppxSteps(expandStepEnv(one), appxDir)
		} else {
			// Environment variables as the Run As account sees them, and
			// no forced machine-wide provisioning: a per-user appx install
			// for the account is exactly what running as it is for.
			one = expandStepEnvWith(one, sess.Expand)
			if one[0].Type == "appx" && len(one[0].APPXDependencies) == 0 && appxDir != "" {
				one[0].APPXDependencies = discoverAppxDependencies(appxDir)
			}
		}
		tail := newOutputTail(toolkitOutputTail)
		runner := &steps.OSRunner{Log: log, DryRun: f.dryRun, WorkDir: workDir, Tee: tail}
		if !asSystem {
			runner.Launcher = sess
		}
		r := steps.Execute(runCtx, one, runner, facts)[0]
		sr := toolkitStepResult{
			Step: i + 1, Type: s.Type, Description: s.Description,
			AsSystem: sess != nil && asSystem,
			ExitCode: r.ExitCode, Skipped: r.Skipped, Output: tail.String(),
		}
		stepFailed := !r.Skipped && (r.Error != nil || r.Aborted)
		if stepFailed || (!r.Skipped && !stepSucceeded(r)) {
			sr.Error = steps.FailureReason(r)
		}
		res.Steps = append(res.Steps, sr)
		lvl := slog.LevelInfo
		if stepFailed {
			lvl = slog.LevelError
		}
		log.Log(ctx, lvl, "toolkit.step", slog.String("target", def.Name), slog.Int("step", i+1),
			slog.String("type", s.Type), slog.Int("exit_code", r.ExitCode),
			slog.Bool("skipped", r.Skipped), slog.Bool("as_system", sr.AsSystem),
			slog.String("error", sr.Error))
		if !r.Skipped && (r.ExitCode == 3010 || r.ExitCode == 1641) {
			rebootRequested = true
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
			failed = true
			res.Error = fmt.Sprintf("stopped after the %d-minute timeout", def.TimeoutMinutes)
			break
		}
		if stepFailed {
			failed = true
			if r.Aborted {
				break
			}
		}
	}

	// Restart, per the kit's policy (never after a timeout: the machine is
	// in an unknown state and an operator should look first).
	if !res.TimedOut && (def.RebootPolicy == rebootAlways || (def.RebootPolicy == rebootIfRequested && rebootRequested)) {
		switch {
		case f.dryRun:
			res.Reboot = "skipped (dry run)"
		default:
			if err := rebootScheduler(ctx, sysRunner, def.Name); err != nil {
				res.Reboot = "failed: " + err.Error()
			} else {
				res.Reboot = fmt.Sprintf("in %ds", toolkitRebootDelay)
			}
		}
		log.Info("toolkit.reboot", slog.String("target", def.Name), slog.String("reboot", res.Reboot))
	}

	if failed {
		if res.Error == "" {
			res.Error = "a step failed"
		}
		log.Error("toolkit.fail", slog.Int64("job", jobID), slog.String("target", def.Name), slog.String("error", res.Error))
		return finish("failed")
	}
	log.Info("toolkit.ok", slog.Int64("job", jobID), slog.String("target", def.Name))
	return finish("ok")
}

// stepSucceeded reports whether a non-skipped step exited with a success
// code (0 or one of its success_codes).
func stepSucceeded(r steps.Result) bool {
	if r.Error != nil {
		return false
	}
	if len(r.Step.SuccessCodes) == 0 {
		return r.ExitCode == 0
	}
	for _, c := range r.Step.SuccessCodes {
		if c == r.ExitCode {
			return true
		}
	}
	return false
}

func (r toolkitResult) toolkitTarget() string {
	if r.ToolkitID == 0 {
		return "toolkit"
	}
	return fmt.Sprintf("toolkit/%d", r.ToolkitID)
}

func runAsLabel(mode, user string) string {
	switch mode {
	case runAsLoggedOnUser:
		return "the logged-on user"
	case runAsUser:
		return user
	}
	return "SYSTEM"
}

// outputTail keeps the last max bytes written to it (a step's combined
// stdout+stderr), trimmed to whole UTF-8 characters.
type outputTail struct {
	max int
	buf []byte
	cut bool
}

func newOutputTail(max int) *outputTail { return &outputTail{max: max} }

func (t *outputTail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.max:]...)
		t.cut = true
	}
	return len(p), nil
}

func (t *outputTail) String() string {
	b := t.buf
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[1:]
	}
	s := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n ")
	if t.cut && s != "" {
		s = "…" + s
	}
	return s
}
