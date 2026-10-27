package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/steps"
	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

func logOne(t *testing.T, fn func(log *slog.Logger)) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	fn(slog.New(slog.NewJSONHandler(&buf, nil)))
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return rec
}

// TestPackageFailAttrsCarryReason: package.install.fail names the failing
// step (index + type), its exit code, a plain error, the msiexec meaning
// and the stderr tail -- not just the package name.
func TestPackageFailAttrsCarryReason(t *testing.T) {
	results := []steps.Result{
		{Step: swspec.InstallStep{Type: "copy"}},
		{Step: swspec.InstallStep{Type: "msi"}, ExitCode: 1603, Aborted: true},
	}
	rec := logOne(t, func(log *slog.Logger) {
		log.Error("package.install.fail", packageFailAttrs("7-Zip", results, []string{"MSI error", "rolled back"})...)
	})
	want := map[string]any{
		"target":       "7-Zip",
		"step":         float64(2),
		"steps":        float64(2),
		"step_type":    "msi",
		"exit_code":    float64(1603),
		"error":        "exit code 1603 (fatal error during installation)",
		"exit_meaning": "fatal error during installation",
		"stderr_tail":  "MSI error\nrolled back",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("%s = %#v, want %#v", k, rec[k], v)
		}
	}
}

// A step that errored before the last one ran must not borrow the last
// step's stderr; a copy step has no exit meaning.
func TestPackageFailAttrsEarlierStep(t *testing.T) {
	results := []steps.Result{
		{Step: swspec.InstallStep{Type: "copy", ContinueOnFailure: true}, ExitCode: -1, Error: errors.New("open a.dat: not found")},
		{Step: swspec.InstallStep{Type: "exe"}},
	}
	rec := logOne(t, func(log *slog.Logger) {
		log.Error("package.install.fail", packageFailAttrs("App", results, []string{"unrelated"})...)
	})
	if rec["step"] != float64(1) || rec["error"] != "open a.dat: not found" || rec["step_type"] != "copy" {
		t.Errorf("unexpected attrs: %v", rec)
	}
	if _, ok := rec["stderr_tail"]; ok {
		t.Errorf("stderr_tail should be omitted for a non-final step: %v", rec)
	}
	if _, ok := rec["exit_meaning"]; ok {
		t.Errorf("copy step has no exit meaning: %v", rec)
	}
}

func TestWithDeployment(t *testing.T) {
	rec := logOne(t, func(log *slog.Logger) { withDeployment(log, 12).Info("x") })
	if rec["deployment_id"] != float64(12) {
		t.Errorf("deployment_id missing: %v", rec)
	}
	rec = logOne(t, func(log *slog.Logger) { withDeployment(log, 0).Info("x") })
	if _, ok := rec["deployment_id"]; ok {
		t.Errorf("deployment_id 0 should not be attached: %v", rec)
	}
}
