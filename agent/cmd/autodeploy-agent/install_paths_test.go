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
	}, {
		Type:             "appx",
		APPXPath:         `%ROOT%\app.msixbundle`,
		APPXDependencies: []string{`%ROOT%\VCLibs.appx`},
		APPXLicense:      `%ROOT%\App_License1.xml`,
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
	if out[3].APPXPath != `C:\Root\app.msixbundle` ||
		out[3].APPXDependencies[0] != `C:\Root\VCLibs.appx` ||
		out[3].APPXLicense != `C:\Root\App_License1.xml` {
		t.Errorf("appx fields not expanded: %+v", out[3])
	}
	// Input must be untouched (expandStepEnv copies).
	if in[0].DestinationPath != `%ROOT%\Start Menu\Programs` {
		t.Error("expandStepEnv mutated its input")
	}
}

// Under the Local System account, a per-user Add-AppxPackage is rejected
// (0x80073CF9), so finalizeAppxSteps forces machine-wide provisioning on every
// appx step -- and only appx steps.
func TestFinalizeAppxStepsProvisionsUnderSystem(t *testing.T) {
	orig := runningAsSystem
	t.Cleanup(func() { runningAsSystem = orig })
	runningAsSystem = func() bool { return true }

	in := []swspec.InstallStep{
		{Type: "appx", APPXPath: "App.msixbundle"},
		{Type: "exe", ExePath: "setup.exe"},
	}
	out := finalizeAppxSteps(in, "")
	if !out[0].APPXProvision {
		t.Error("appx step should be provisioned machine-wide under SYSTEM")
	}
	if out[1].APPXProvision {
		t.Error("non-appx step must be left alone")
	}
	// Copy semantics: the input slice is not mutated.
	if in[0].APPXProvision {
		t.Error("finalizeAppxSteps mutated its input")
	}
}

// Off the Local System account, provisioning is not forced: a plain appx step
// stays a per-user Add-AppxPackage unless the operator opted into provisioning.
func TestFinalizeAppxStepsKeepsPerUserOffSystem(t *testing.T) {
	orig := runningAsSystem
	t.Cleanup(func() { runningAsSystem = orig })
	runningAsSystem = func() bool { return false }

	out := finalizeAppxSteps([]swspec.InstallStep{{Type: "appx", APPXPath: "App.msix"}}, "")
	if out[0].APPXProvision {
		t.Error("appx step should stay per-user when the agent isn't SYSTEM")
	}
}

// A bundle whose step names no dependencies picks up the .appx/.msix packages in
// a sibling Dependencies/ folder (the layout `winget download` writes), sorted
// for a deterministic order and ignoring non-package files. An explicit
// dependency list is left untouched.
func TestFinalizeAppxStepsDiscoversDependenciesFolder(t *testing.T) {
	orig := runningAsSystem
	t.Cleanup(func() { runningAsSystem = orig })
	runningAsSystem = func() bool { return false }

	dir := t.TempDir()
	depDir := filepath.Join(dir, "Dependencies")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"VCLibs.appx", "UIXaml.msix", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(depDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := finalizeAppxSteps([]swspec.InstallStep{{Type: "appx", APPXPath: "App.msixbundle"}}, dir)
	want := []string{filepath.Join(depDir, "UIXaml.msix"), filepath.Join(depDir, "VCLibs.appx")}
	if len(out[0].APPXDependencies) != len(want) {
		t.Fatalf("dependencies = %v, want %v", out[0].APPXDependencies, want)
	}
	for i := range want {
		if out[0].APPXDependencies[i] != want[i] {
			t.Errorf("dependency %d = %q, want %q", i, out[0].APPXDependencies[i], want[i])
		}
	}

	// An operator-supplied list is authoritative -- discovery must not override it.
	explicit := finalizeAppxSteps([]swspec.InstallStep{
		{Type: "appx", APPXPath: "App.msixbundle", APPXDependencies: []string{"chosen.appx"}},
	}, dir)
	if len(explicit[0].APPXDependencies) != 1 || explicit[0].APPXDependencies[0] != "chosen.appx" {
		t.Errorf("explicit dependencies overridden: %v", explicit[0].APPXDependencies)
	}
}

// No Dependencies/ folder is the common case for a single-file package and must
// yield no dependencies rather than an error.
func TestDiscoverAppxDependenciesNoFolder(t *testing.T) {
	if got := discoverAppxDependencies(t.TempDir()); got != nil {
		t.Errorf("expected nil for a package with no Dependencies/ folder, got %v", got)
	}
}
