package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobStoreWriteAndOpen(t *testing.T) {
	dir := t.TempDir()
	b, err := NewBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := b.WriteStream("iso/1/install.wim", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("wrote %d, want 5", n)
	}

	f, err := b.Open("iso/1/install.wim")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("read = %q", got)
	}

	// Atomic: temp file should not linger.
	parent := filepath.Join(dir, "iso", "1")
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}

func TestBlobStoreResolveStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	b, _ := NewBlobStore(root)
	cases := []string{
		"../etc/passwd",
		"iso/../../etc/passwd",
		"/etc/passwd",
		"./../../foo",
		"a/b/c",
	}
	for _, p := range cases {
		abs, err := b.Resolve(p)
		if err != nil {
			// Refusal is acceptable.
			continue
		}
		rel, err := filepath.Rel(b.Root(), abs)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("Resolve(%q)=%q escapes root %q (rel=%q)", p, abs, b.Root(), rel)
		}
	}
}

func TestCategoryRoot_DefaultsToBaseSubdir(t *testing.T) {
	root := t.TempDir()
	b, _ := NewBlobStore(root)
	want := filepath.Join(root, "iso")
	if got := b.CategoryRoot("iso"); got != want {
		t.Errorf("CategoryRoot default: got %q, want %q", got, want)
	}
}

func TestSetCategoryRoot_OverridesResolve(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	b, _ := NewBlobStore(root)
	if err := b.SetCategoryRoot("iso", other); err != nil {
		t.Fatal(err)
	}
	// Drivers should still resolve under the base root.
	if got := b.CategoryRoot("drivers"); got != filepath.Join(root, "drivers") {
		t.Errorf("CategoryRoot(drivers): unexpected override leak, got %q", got)
	}
	abs, err := b.Resolve("iso/12/source.iso")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(other, "12", "source.iso")
	if abs != want {
		t.Errorf("Resolve under override: got %q, want %q", abs, want)
	}
}

func TestSetCategoryRoot_RefusesRelative(t *testing.T) {
	root := t.TempDir()
	b, _ := NewBlobStore(root)
	if err := b.SetCategoryRoot("iso", "relative/path"); err == nil {
		t.Error("expected error for non-absolute path")
	}
}

func TestSetCategoryRoot_EmptyClearsOverride(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	b, _ := NewBlobStore(root)
	_ = b.SetCategoryRoot("iso", other)
	if err := b.SetCategoryRoot("iso", ""); err != nil {
		t.Fatal(err)
	}
	if got := b.CategoryRoot("iso"); got != filepath.Join(root, "iso") {
		t.Errorf("clearing override should restore default, got %q", got)
	}
}

func TestResolve_PathTraversalStaysInsideKnownRoots_WithOverride(t *testing.T) {
	// Caller-supplied relative paths get normalised by filepath.Clean;
	// a path like "iso/../../etc/passwd" doesn't escape the data dir,
	// it just resolves to a different (non-override) category. Verify
	// every probe lands either inside the base root or inside one of
	// the configured override roots.
	root := t.TempDir()
	override := t.TempDir()
	b, _ := NewBlobStore(root)
	_ = b.SetCategoryRoot("iso", override)
	cases := []string{
		"iso/12/source.iso",
		"drivers/3/payload.bin",
		"iso/../../etc/passwd", // normalises away iso prefix
		"../etc/passwd",
		"/etc/passwd",
		"./../../foo",
	}
	for _, p := range cases {
		abs, err := b.Resolve(p)
		if err != nil {
			continue // refusal is acceptable
		}
		baseRel, _ := filepath.Rel(root, abs)
		overRel, _ := filepath.Rel(override, abs)
		insideBase := !strings.HasPrefix(baseRel, "..")
		insideOverride := !strings.HasPrefix(overRel, "..")
		if !insideBase && !insideOverride {
			t.Errorf("Resolve(%q)=%q escapes all known roots", p, abs)
		}
	}
}
