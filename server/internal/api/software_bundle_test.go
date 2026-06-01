package api

import (
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestListPackageBundles enumerates the package bundle/ directory and builds
// download URLs the agent can fetch.
func TestListPackageBundles(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Blobs: blobs}

	// No bundle dir yet.
	if got := listPackageBundles(r, 7, "/payload/software/"); got != nil {
		t.Errorf("expected nil with no bundles, got %v", got)
	}

	// Seed a bundle zip under software/7/bundle/.
	if _, err := blobs.WriteStream("software/7/bundle/papercut.zip", strings.NewReader("PK\x03\x04zip")); err != nil {
		t.Fatal(err)
	}

	got := listPackageBundles(r, 7, "/payload/software/")
	if len(got) != 1 {
		t.Fatalf("expected 1 bundle, got %d (%v)", len(got), got)
	}
	if got[0].Name != "papercut.zip" {
		t.Errorf("name = %q", got[0].Name)
	}
	if got[0].URL != "/payload/software/7/bundle/papercut.zip" {
		t.Errorf("url = %q", got[0].URL)
	}
	if got[0].SizeBytes == 0 {
		t.Error("size should be non-zero")
	}
}
