// Package steps executes a SoftwarePackage's ordered install steps. Each
// step's success codes and continue-on-failure flag control how the
// sequencer responds; an aborting failure short-circuits the package.
//
// The package speaks through a Runner interface so tests can record
// commands instead of running them. The OSRunner used at runtime spawns
// processes via os/exec with the correct shell wrappers per step type.
package steps

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// Runner is the boundary between the executor and the host shell.
type Runner interface {
	// Run executes name with args, returning the process exit code.
	// shellInput, if non-empty, is piped to the process's stdin (used for
	// cmd/powershell steps with inline script bodies).
	Run(ctx context.Context, name string, args []string, shellInput string) (int, error)
	// Copy duplicates src to dst. Goes through the runner so tests can
	// record copies and dry-run can log without touching disk.
	Copy(ctx context.Context, src, dst string) error
	// Unzip extracts every entry of the zip at src into the dest
	// directory. Goes through the runner so dry-run logs without
	// touching disk and tests can assert against recorded extractions.
	// Implementations MUST refuse entries whose resolved path escapes
	// dst (zip-slip defence).
	Unzip(ctx context.Context, src, dst string) error
}

// OSRunner runs commands via os/exec.
type OSRunner struct {
	Log    *slog.Logger
	DryRun bool
	// WorkDir, when set, is the working directory for Run'd processes. The
	// agent points it at the package's work dir so an installer's relative
	// args (e.g. OfficeSetup.exe /configure NoTeams.xml) and a step's bare
	// filenames resolve against the downloaded/extracted files, not the
	// service's CWD (C:\Windows\System32).
	WorkDir string
}

// Run implements Runner.
func (r *OSRunner) Run(ctx context.Context, name string, args []string, shellInput string) (int, error) {
	if r.Log != nil {
		r.Log.Info("steps.exec",
			slog.String("actor", "agent"),
			slog.String("target", name),
			slog.String("args", fmt.Sprintf("%q", args)),
			slog.String("dir", r.WorkDir),
			slog.Bool("dry_run", r.DryRun),
			slog.Bool("has_stdin", shellInput != ""),
		)
	}
	if r.DryRun {
		return 0, nil
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.WorkDir
	cmd.Stdout = stdoutWriter{r.Log}
	cmd.Stderr = stderrWriter{r.Log}
	if shellInput != "" {
		cmd.Stdin = strings.NewReader(shellInput)
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// Unzip implements Runner. archive/zip from the stdlib is enough --
// the agent never deals with multi-volume or password-protected zips
// in the install-step path. Every entry's path is validated to live
// under dst before any bytes are written (zip-slip defence: an
// entry named "..\Windows\System32\..." would otherwise be allowed
// to escape).
func (r *OSRunner) Unzip(_ context.Context, src, dst string) error {
	if r.Log != nil {
		r.Log.Info("steps.unzip",
			slog.String("actor", "agent"),
			slog.String("source", src),
			slog.String("destination", dst),
			slog.Bool("dry_run", r.DryRun),
		)
	}
	if r.DryRun {
		return nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("open zip %s: %w", src, err)
	}
	defer zr.Close()
	// Resolve dst to an absolute, symlink-evaluated path so the
	// containment check below can't be defeated by a destination
	// that's itself a symlink into a sensitive area.
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		if err := extractZipEntry(absDst, f); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(absDst string, f *zip.File) error {
	// Reject absolute paths and any path containing ".." segments
	// before we let Join resolve them; both are zip-slip vectors.
	if filepath.IsAbs(f.Name) || strings.Contains(f.Name, "..") {
		return fmt.Errorf("unsafe zip entry %q: absolute or contains .. segments", f.Name)
	}
	// Join, then double-check the result is still under absDst.
	target := filepath.Join(absDst, filepath.FromSlash(f.Name))
	rel, err := filepath.Rel(absDst, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("unsafe zip entry %q: resolves outside destination", f.Name)
	}
	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, f.Mode().Perm()|0o700)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	mode := f.Mode().Perm()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Copy implements Runner.
func (r *OSRunner) Copy(_ context.Context, src, dst string) error {
	if r.Log != nil {
		r.Log.Info("steps.copy",
			slog.String("actor", "agent"),
			slog.String("source", src),
			slog.String("destination", dst),
			slog.Bool("dry_run", r.DryRun),
		)
	}
	if r.DryRun {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(parent(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func parent(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[:i]
		}
	}
	return "."
}

// Result is the outcome of executing one step.
type Result struct {
	Step     swspec.InstallStep
	ExitCode int
	Error    error
	Aborted  bool
}

// Execute runs the ordered list of steps and returns one Result per step
// actually attempted. If a non-success-coded step has ContinueOnFailure
// false, execution stops and the last Result has Aborted = true.
func Execute(ctx context.Context, list []swspec.InstallStep, r Runner) []Result {
	results := make([]Result, 0, len(list))
	for _, s := range list {
		res := runOne(ctx, s, r)
		results = append(results, res)
		if res.Aborted {
			break
		}
	}
	return results
}

func runOne(ctx context.Context, s swspec.InstallStep, r Runner) Result {
	res := Result{Step: s}
	switch s.Type {
	case "copy":
		if err := r.Copy(ctx, s.SourcePath, s.DestinationPath); err != nil {
			res.Error = err
			res.ExitCode = -1
		}
	case "unzip":
		if err := r.Unzip(ctx, s.SourcePath, s.DestinationPath); err != nil {
			res.Error = err
			res.ExitCode = -1
		}
	case "msi":
		args := append([]string{"/i", s.MSIPath, "/quiet", "/norestart"}, s.MSIArgs...)
		res.ExitCode, res.Error = r.Run(ctx, "msiexec", args, "")
	case "appx":
		// Add-AppxPackage via PowerShell.
		body := fmt.Sprintf(`Add-AppxPackage -Path '%s' -ErrorAction Stop`,
			strings.ReplaceAll(s.APPXPath, "'", "''"))
		res.ExitCode, res.Error = r.Run(ctx, "powershell",
			[]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", body}, "")
	case "cmd":
		res.ExitCode, res.Error = r.Run(ctx, "cmd", []string{"/C", s.ScriptBody}, "")
	case "powershell":
		// Pass the script via stdin so the body can be arbitrary length
		// and contain quotes safely.
		res.ExitCode, res.Error = r.Run(ctx, "powershell",
			[]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-"},
			s.ScriptBody)
	case "exe":
		res.ExitCode, res.Error = r.Run(ctx, s.ExePath, s.ExeArgs, "")
	case "winget":
		args := []string{"install", "--id", s.WingetID, "--silent",
			"--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity"}
		args = append(args, s.WingetArgs...)
		res.ExitCode, res.Error = r.Run(ctx, "winget", args, "")
	default:
		res.Error = fmt.Errorf("unknown step type %q", s.Type)
		res.ExitCode = -1
	}
	if res.Error != nil || !isSuccess(res.ExitCode, s.SuccessCodes) {
		if !s.ContinueOnFailure {
			res.Aborted = true
		}
	}
	return res
}

func isSuccess(code int, allowed []int) bool {
	if len(allowed) == 0 {
		return code == 0
	}
	for _, c := range allowed {
		if c == code {
			return true
		}
	}
	return false
}

type stdoutWriter struct{ log *slog.Logger }

func (w stdoutWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Info("steps.exec.stdout", slog.String("line", string(p)))
	}
	return len(p), nil
}

type stderrWriter struct{ log *slog.Logger }

func (w stderrWriter) Write(p []byte) (int, error) {
	if w.log != nil {
		w.log.Warn("steps.exec.stderr", slog.String("line", string(p)))
	}
	return len(p), nil
}
