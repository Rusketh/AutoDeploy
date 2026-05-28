package detect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/swspec"
)

// fakeBackend lets tests script the answers.
type fakeBackend struct {
	files     map[string]bool
	versions  map[string]string
	registry  map[string]string // hive|key|value -> value
	products  map[string]bool
}

func (f *fakeBackend) FileExists(p string) (bool, error)     { return f.files[p], nil }
func (f *fakeBackend) FileVersion(p string) (string, error)  { return f.versions[p], nil }
func (f *fakeBackend) MSIProductInstalled(c string) (bool, error) {
	return f.products[c], nil
}
func (f *fakeBackend) RegistryValuePresent(h, k, v string) (bool, string, error) {
	key := h + "|" + k + "|" + v
	val, ok := f.registry[key]
	return ok, val, nil
}

func TestEvaluatePackageAllRulesMustMatch(t *testing.T) {
	fb := &fakeBackend{
		files:    map[string]bool{`C:\Program Files\Acme\acme.exe`: true},
		versions: map[string]string{`C:\Program Files\Acme\acme.exe`: "1.2.3"},
	}
	e := &Evaluator{Backend: fb}
	ok, err := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `C:\Program Files\Acme\acme.exe`, FileVersion: "1.2.3"},
	})
	if err != nil || !ok {
		t.Fatalf("expected detected, got ok=%v err=%v", ok, err)
	}
	// Wrong version -> not detected.
	ok, _ = e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `C:\Program Files\Acme\acme.exe`, FileVersion: "2.0.0"},
	})
	if ok {
		t.Error("expected not detected on version mismatch")
	}
}

func TestEvaluatePackageEmptyRulesIsNotDetected(t *testing.T) {
	e := &Evaluator{Backend: &fakeBackend{}}
	ok, _ := e.EvaluatePackage(context.Background(), nil)
	if ok {
		t.Error("no rules should report not detected so package is (re)installed")
	}
}

func TestRegistryEquals(t *testing.T) {
	fb := &fakeBackend{
		registry: map[string]string{"HKLM|Software\\Acme|Installed": "1"},
	}
	e := &Evaluator{Backend: fb}
	ok, _ := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "registry", RegistryHive: "HKLM", RegistryKey: "Software\\Acme",
			RegistryValue: "Installed", RegistryEquals: "1"},
	})
	if !ok {
		t.Error("expected detected when registry value matches")
	}
	ok, _ = e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "registry", RegistryHive: "HKLM", RegistryKey: "Software\\Acme",
			RegistryValue: "Installed", RegistryEquals: "0"},
	})
	if ok {
		t.Error("expected not detected when registry value mismatches")
	}
}

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	fb := &fakeBackend{files: map[string]bool{path: true}}
	e := &Evaluator{Backend: fb}
	ok, err := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: path, FileSHA256: helloSHA},
	})
	if err != nil || !ok {
		t.Fatalf("expected SHA match, got ok=%v err=%v", ok, err)
	}
}
