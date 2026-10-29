package imaging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
)

// TestOSRunnerStderrTail: Exec logs stderr at INFO (action name unchanged)
// and keeps the last lines for the failure screen, reset per Exec.
func TestOSRunnerStderrTail(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	var buf bytes.Buffer
	r := &OSRunner{Log: slog.New(slog.NewJSONHandler(&buf, nil))}
	err := r.Exec(context.Background(), "sh", "-c", "echo one >&2; printf 'two\\nthree' >&2; exit 4")
	if err == nil {
		t.Fatal("expected exit error")
	}
	if got := strings.Join(r.LastStderr(), "|"); got != "one|two|three" {
		t.Fatalf("LastStderr = %q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] == "imaging.exec.stderr" && rec["level"] != "INFO" {
			t.Errorf("stderr logged at %v, want INFO", rec["level"])
		}
	}
	if err := r.Exec(context.Background(), "sh", "-c", "true"); err != nil {
		t.Fatal(err)
	}
	if r.LastStderr() != nil {
		t.Errorf("tail not reset: %q", r.LastStderr())
	}
}
