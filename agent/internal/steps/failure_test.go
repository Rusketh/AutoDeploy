package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

func TestLineTailKeepsLastLines(t *testing.T) {
	tl := &lineTail{max: 3}
	tl.Write([]byte("one\ntw"))
	tl.Write([]byte("o\n\n  three  \nfour\nfi"))
	got := tl.Lines()
	want := []string{"three", "four", "fi"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("Lines = %q, want %q", got, want)
	}
	if (&lineTail{max: 3}).Lines() != nil {
		t.Error("empty tail should be nil")
	}
}

// TestOSRunnerStderrIsInfoAndTailed: stderr lines are logged at INFO under
// the unchanged steps.exec.stderr action, and the last lines are kept for
// the failure event.
func TestOSRunnerStderrIsInfoAndTailed(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	var buf bytes.Buffer
	r := &OSRunner{Log: slog.New(slog.NewJSONHandler(&buf, nil))}
	code, err := r.Run(context.Background(), "sh", []string{"-c", "echo first >&2; echo 'MSI (s) error 1603' >&2; exit 3"}, "")
	if err != nil || code != 3 {
		t.Fatalf("Run = %d, %v", code, err)
	}
	if got := r.LastStderr(); len(got) != 2 || got[1] != "MSI (s) error 1603" {
		t.Fatalf("LastStderr = %q", got)
	}
	sawStderr := false
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		if rec["msg"] == "steps.exec.stderr" {
			sawStderr = true
			if rec["level"] != "INFO" {
				t.Errorf("stderr logged at %v, want INFO", rec["level"])
			}
		}
	}
	if !sawStderr {
		t.Fatalf("no steps.exec.stderr event in:\n%s", buf.String())
	}
	// A later run with no stderr clears the tail.
	if _, err := r.Run(context.Background(), "sh", []string{"-c", "true"}, ""); err != nil {
		t.Fatal(err)
	}
	if r.LastStderr() != nil {
		t.Errorf("tail not reset: %q", r.LastStderr())
	}
}

func TestFailureReasonAndExitMeaning(t *testing.T) {
	cases := []struct {
		res  Result
		want string
	}{
		{Result{Step: swspec.InstallStep{Type: "msi"}, ExitCode: 1603, Aborted: true},
			"exit code 1603 (fatal error during installation)"},
		{Result{Step: swspec.InstallStep{Type: "msi", SuccessCodes: []int{0}}, ExitCode: 3010, Aborted: true},
			"exit code 3010 (success, restart required), not one of the step's success codes [0]"},
		{Result{Step: swspec.InstallStep{Type: "exe"}, ExitCode: 7, Aborted: true}, "exit code 7"},
		{Result{Step: swspec.InstallStep{Type: "copy"}, ExitCode: -1, Error: errors.New("open src: no such file")},
			"open src: no such file"},
		{Result{Step: swspec.InstallStep{Type: "msi"}, ExitCode: 0}, ""},
		{Result{Step: swspec.InstallStep{Type: "msi"}, Skipped: true}, ""},
	}
	for _, c := range cases {
		if got := FailureReason(c.res); got != c.want {
			t.Errorf("FailureReason(%+v) = %q, want %q", c.res, got, c.want)
		}
	}
	for code, want := range map[int]string{
		0: "success", 1603: "fatal error during installation", 1618: "another installation is already in progress",
		3010: "success, restart required", 1641: "success, restart initiated", 42: "",
	} {
		if got := ExitMeaning(code); got != want {
			t.Errorf("ExitMeaning(%d) = %q, want %q", code, got, want)
		}
	}
}
