package wingetsrc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

func TestParseSearch(t *testing.T) {
	results, err := parseSearch(readTestdata(t, "manifestSearch.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	if results[0].PackageIdentifier != "7zip.7zip" || results[0].Name != "7-Zip" {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	// Latest version chosen from the (unsorted) list.
	if results[0].LatestVersion != "24.08" {
		t.Errorf("latest version = %q, want 24.08", results[0].LatestVersion)
	}
}

func TestParseManifestLatestAndInheritance(t *testing.T) {
	// exe manifest sets InstallerType/Scope at the version level; the
	// installer should inherit them.
	m, err := parseManifest(readTestdata(t, "manifest_exe_deps.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "1.2.3" {
		t.Fatalf("version = %q", m.Version)
	}
	if len(m.Installers) != 1 {
		t.Fatalf("want 1 installer, got %d", len(m.Installers))
	}
	inst := m.Installers[0]
	if inst.InstallerType != "inno" || inst.Scope != "machine" {
		t.Errorf("inheritance failed: type=%q scope=%q", inst.InstallerType, inst.Scope)
	}
	if len(m.Dependencies) != 1 || m.Dependencies[0] != "Microsoft.VCRedist.2015+.x64" {
		t.Errorf("dependencies = %v", m.Dependencies)
	}
}

func TestPickInstaller(t *testing.T) {
	m, err := parseManifest(readTestdata(t, "manifest_msi.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	// Default prefers x64 + machine.
	got := PickInstaller(m.Installers, "", "")
	if got == nil || got.Architecture != "x64" {
		t.Fatalf("default pick = %+v", got)
	}
	// Explicit x86 override.
	got = PickInstaller(m.Installers, "x86", "machine")
	if got == nil || got.Architecture != "x86" {
		t.Fatalf("x86 pick = %+v", got)
	}
	// A requested arch with no match falls back rather than returning nil.
	got = PickInstaller(m.Installers, "arm64", "machine")
	if got == nil {
		t.Fatal("arm64 pick returned nil; expected a fallback")
	}
	if PickInstaller(nil, "", "") != nil {
		t.Error("empty installer list should pick nil")
	}
}

func TestBuildPackageMSI(t *testing.T) {
	m, _ := parseManifest(readTestdata(t, "manifest_msi.json"), "")
	chosen := PickInstaller(m.Installers, "x64", "machine")
	bp, err := BuildPackage(m, chosen, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Files) != 1 || !strings.HasSuffix(bp.Files[0].Filename, ".msi") {
		t.Fatalf("files = %+v", bp.Files)
	}
	if len(bp.Steps) != 1 || bp.Steps[0].Type != "msi" {
		t.Fatalf("steps = %+v", bp.Steps)
	}
	if bp.Steps[0].MSIPath != bp.Files[0].Filename {
		t.Errorf("msi step path %q != file %q", bp.Steps[0].MSIPath, bp.Files[0].Filename)
	}
	// winget + msi detection (product code present).
	if len(bp.Detection) != 2 {
		t.Fatalf("detection = %+v", bp.Detection)
	}
	if bp.Detection[0].Type != "winget" || bp.Detection[0].WingetID != "7zip.7zip" {
		t.Errorf("winget detection = %+v", bp.Detection[0])
	}
	if bp.Detection[1].Type != "msi" || bp.Detection[1].MSIProductCode == "" {
		t.Errorf("msi detection = %+v", bp.Detection[1])
	}
	// SuccessCodes include reboot-required.
	if len(bp.Steps[0].SuccessCodes) == 0 || bp.Steps[0].SuccessCodes[1] != 3010 {
		t.Errorf("success codes = %v", bp.Steps[0].SuccessCodes)
	}
}

func TestBuildPackageExeWithPrereqOrdering(t *testing.T) {
	m, _ := parseManifest(readTestdata(t, "manifest_exe_deps.json"), "")
	chosen := PickInstaller(m.Installers, "x64", "machine")

	dep, _ := parseManifest(readTestdata(t, "manifest_vcredist.json"), "")
	depInst := PickInstaller(dep.Installers, "x64", "machine")

	bp, err := BuildPackage(m, chosen, []DepInstaller{{Manifest: dep, Installer: depInst}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Two files: prereq then app.
	if len(bp.Files) != 2 {
		t.Fatalf("files = %+v", bp.Files)
	}
	if len(bp.Steps) != 2 {
		t.Fatalf("steps = %+v", bp.Steps)
	}
	// Prereq step comes first and is an exe (burn → exe with /quiet).
	if bp.Steps[0].Type != "exe" || !strings.Contains(bp.Steps[0].Description, "Prerequisite") {
		t.Errorf("first step should be the prereq: %+v", bp.Steps[0])
	}
	if len(bp.Steps[0].ExeArgs) == 0 || bp.Steps[0].ExeArgs[0] != "/quiet" {
		t.Errorf("burn prereq args = %v (want /quiet default)", bp.Steps[0].ExeArgs)
	}
	// Main app: inno → exe with /VERYSILENT default.
	if bp.Steps[1].Type != "exe" || bp.Steps[1].ExeArgs[0] != "/VERYSILENT" {
		t.Errorf("inno main step = %+v", bp.Steps[1])
	}
	// AppsAndFeatures product code drives the msi detection rule.
	if len(bp.Detection) != 2 || bp.Detection[1].Type != "msi" {
		t.Errorf("detection = %+v", bp.Detection)
	}
}

func TestBuildPackageMSIX(t *testing.T) {
	m, _ := parseManifest(readTestdata(t, "manifest_msix.json"), "")
	chosen := PickInstaller(m.Installers, "x64", "machine")
	bp, err := BuildPackage(m, chosen, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(bp.Steps) != 1 || bp.Steps[0].Type != "appx" {
		t.Fatalf("steps = %+v", bp.Steps)
	}
	if bp.Steps[0].APPXPath != bp.Files[0].Filename {
		t.Errorf("appx path %q != file %q", bp.Steps[0].APPXPath, bp.Files[0].Filename)
	}
	// No product code → winget-only detection.
	if len(bp.Detection) != 1 || bp.Detection[0].Type != "winget" {
		t.Errorf("detection = %+v", bp.Detection)
	}
}

func TestInstallerFilenameFallback(t *testing.T) {
	m := &Manifest{PackageIdentifier: "Vendor.App", Version: "1.0"}
	inst := &Installer{InstallerType: "exe"}
	// A URL with no usable base name synthesizes one.
	name := installerFilename("https://example.com/download?id=42", m, inst)
	if !strings.HasSuffix(name, ".exe") || !strings.Contains(name, "Vendor-App") {
		t.Errorf("synthesized filename = %q", name)
	}
	// A clean URL keeps its base name.
	name = installerFilename("https://example.com/path/setup.msi", m, &Installer{InstallerType: "msi"})
	if name != "setup.msi" {
		t.Errorf("filename = %q, want setup.msi", name)
	}
}

func TestClientSearchAndManifestOverHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/manifestSearch":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(readTestdata(t, "manifestSearch.json"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/packageManifests/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(readTestdata(t, "manifest_msi.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	results, err := c.Search(context.Background(), "7zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("search results = %d", len(results))
	}
	m, err := c.Manifest(context.Background(), "7zip.7zip", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.PackageIdentifier != "7zip.7zip" || len(m.Installers) != 2 {
		t.Errorf("manifest = %+v", m)
	}
}

func TestSearchNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	results, err := c.Search(context.Background(), "nothing")
	if err != nil {
		t.Fatalf("204 should not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no results, got %d", len(results))
	}
}
