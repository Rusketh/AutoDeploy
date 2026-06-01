package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// TestMapWorkdirFiles maps basenames to absolute paths and prefers the
// shallowest path on a collision (a top-level installer beats a same-named
// file inside an extracted-bundle subfolder).
func TestMapWorkdirFiles(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "support")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "setup.msi"))
	write(filepath.Join(sub, "config.xml"))
	write(filepath.Join(sub, "setup.msi")) // deeper duplicate -- must NOT win

	m := mapWorkdirFiles(dir)
	if got := m["setup.msi"]; got != filepath.Join(dir, "setup.msi") {
		t.Errorf("setup.msi mapped to %q, want the top-level copy", got)
	}
	if got := m["config.xml"]; got != filepath.Join(sub, "config.xml") {
		t.Errorf("config.xml mapped to %q", got)
	}
}

// TestExpandStepEnv confirms every path field (incl. copy/unzip destinations
// and arg slices) is run through the env expander.
func TestExpandStepEnv(t *testing.T) {
	orig := envExpand
	t.Cleanup(func() { envExpand = orig })
	// Deterministic stub: replace the marker with a fixed root.
	envExpand = func(s string) string {
		return strings.ReplaceAll(s, "%ROOT%", `C:\Root`)
	}

	in := []swspec.InstallStep{{
		Type:            "unzip",
		SourcePath:      "bundle.zip",
		DestinationPath: `%ROOT%\Start Menu\Programs`,
	}, {
		Type:    "msi",
		MSIPath: `%ROOT%\app.msi`,
		MSIArgs: []string{`LOG=%ROOT%\log.txt`},
	}, {
		Type:    "exe",
		ExePath: `%ROOT%\setup.exe`,
		ExeArgs: []string{`/dir=%ROOT%\x`},
	}}
	out := expandStepEnv(in)

	if out[0].DestinationPath != `C:\Root\Start Menu\Programs` {
		t.Errorf("unzip destination not expanded: %q", out[0].DestinationPath)
	}
	if out[1].MSIPath != `C:\Root\app.msi` || out[1].MSIArgs[0] != `LOG=C:\Root\log.txt` {
		t.Errorf("msi fields not expanded: %+v", out[1])
	}
	if out[2].ExePath != `C:\Root\setup.exe` || out[2].ExeArgs[0] != `/dir=C:\Root\x` {
		t.Errorf("exe fields not expanded: %+v", out[2])
	}
	// Input must be untouched (expandStepEnv copies).
	if in[0].DestinationPath != `%ROOT%\Start Menu\Programs` {
		t.Error("expandStepEnv mutated its input")
	}
}
