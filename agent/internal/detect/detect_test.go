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

// File detection runs winenv.Expand on the rule's file_path before
// touching the host. On Linux (dev / CI) os.ExpandEnv handles
// $VAR and ${VAR}; %VAR% (the Windows-style production syntax)
// passes through untouched -- by design, a rule that expects
// Windows env vars must not silently "detect" on a Linux dev host.
func TestEvaluateFileExpandsEnvVars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AD_DETECT_TEST_DIR", dir)
	path := filepath.Join(dir, "expanded.exe")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{files: map[string]bool{path: true}}
	e := &Evaluator{Backend: fb}
	// $VAR form -- the portable expander handles it on Linux.
	ok, err := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `$AD_DETECT_TEST_DIR/expanded.exe`},
	})
	if err != nil || !ok {
		t.Errorf("$VAR expansion: ok=%v err=%v (FilePath should have expanded to %q)",
			ok, err, path)
	}
	// ${VAR} form.
	ok, err = e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `${AD_DETECT_TEST_DIR}/expanded.exe`},
	})
	if err != nil || !ok {
		t.Errorf("${VAR} expansion: ok=%v err=%v", ok, err)
	}
}

// SHA-256 verification path also receives the expanded path, so a
// rule with a $VAR file_path + SHA-256 works the same as a literal
// absolute path.
func TestEvaluateFileExpandsEnvVarsForSHA256(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AD_DETECT_TEST_DIR", dir)
	path := filepath.Join(dir, "y.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	fb := &fakeBackend{files: map[string]bool{path: true}}
	e := &Evaluator{Backend: fb}
	ok, err := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `$AD_DETECT_TEST_DIR/y.bin`, FileSHA256: helloSHA},
	})
	if err != nil || !ok {
		t.Errorf("env var + SHA-256: ok=%v err=%v", ok, err)
	}
}

// %VAR% syntax (Windows-style) MUST NOT expand on Linux. A
// production rule written for Windows that gets evaluated on a
// non-Windows agent host should fall through to "not detected"
// rather than silently match a wrong file. This is the
// fail-closed default the package docs promise.
func TestEvaluateFilePercentVarDoesNotExpandOnPosix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AD_DETECT_TEST_DIR", dir)
	path := filepath.Join(dir, "z.exe")
	_ = os.WriteFile(path, []byte("x"), 0o644)
	fb := &fakeBackend{files: map[string]bool{path: true}}
	e := &Evaluator{Backend: fb}
	ok, _ := e.EvaluatePackage(context.Background(), []swspec.DetectionRule{
		{Type: "file", FilePath: `%AD_DETECT_TEST_DIR%/z.exe`},
	})
	if ok {
		t.Error("%VAR% should not expand on Linux; expected not detected")
	}
}
