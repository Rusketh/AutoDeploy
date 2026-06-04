package detect

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// A file detection rule must match when the path contains spaces (and parens),
// both as a literal path and via an env var that expands to a spaced
// directory -- e.g. %ProgramFiles(x86)%\HUE Intuition\Hue Camera Manager.exe.
func TestEvaluateFileWithSpacesInPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Program Files (x86)", "HUE Intuition")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fpath := filepath.Join(dir, "Hue Camera Manager.exe")
	if err := os.WriteFile(fpath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Evaluator{Backend: PortableBackend{}}

	ok, err := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: fpath},
	})
	if err != nil || !ok {
		t.Errorf("literal spaced path: ok=%v err=%v (path=%q)", ok, err, fpath)
	}

	// $VAR form expands on every host (the portable expander handles it),
	// so the spaced directory resolves and the file is found.
	t.Setenv("AD_SPACE_DIR", filepath.Join(root, "Program Files (x86)"))
	ok, err = e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `$AD_SPACE_DIR/HUE Intuition/Hue Camera Manager.exe`},
	})
	if err != nil || !ok {
		t.Errorf("env-var spaced path: ok=%v err=%v", ok, err)
	}
}

// When a file rule does not match, the evaluator logs the RESOLVED path it
// checked, so an operator can tell a wrong expansion from a genuinely-absent
// file. The resolved path (spaces and all) must appear verbatim.
func TestEvaluateFileLogsResolvedPathWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	e := &Evaluator{
		Backend: PortableBackend{},
		Log:     slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	t.Setenv("AD_ABSENT_DIR", "/no/such dir (x86)")
	ok, _ := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `$AD_ABSENT_DIR/Microsoft OneDrive/OneDrive.exe`},
	})
	if ok {
		t.Fatal("expected not detected for a missing file")
	}
	out := buf.String()
	if !strings.Contains(out, "detect.file.not_detected") ||
		!strings.Contains(out, `/no/such dir (x86)/Microsoft OneDrive/OneDrive.exe`) {
		t.Errorf("expected log carrying the resolved path, got: %s", out)
	}
}
