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
