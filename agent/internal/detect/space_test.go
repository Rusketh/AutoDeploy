package detect

import (
	"context"
	"os"
	"path/filepath"
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
