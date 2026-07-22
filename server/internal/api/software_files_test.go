package api

import (
	"sort"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestListPackageFilesRecursesSubdirs confirms folder-structured payloads are
// advertised to the agent with their slash-relative path intact (so nested
// files like drivers/oem.inf survive the manifest and get a matching URL).
func TestListPackageFilesRecursesSubdirs(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Blobs: blobs}

	// No files dir yet.
	if got := listPackageFiles(r, 9, "/payload/software/"); got != nil {
		t.Errorf("expected nil with no files, got %v", got)
	}

	// Seed a flat file and a nested one under a sub-directory.
	if _, err := blobs.WriteStream("software/9/files/setup.exe", strings.NewReader("MZflat")); err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.WriteStream("software/9/files/drivers/oem.inf", strings.NewReader("[inf]")); err != nil {
		t.Fatal(err)
	}

	got := listPackageFiles(r, 9, "/payload/software/")
	if len(got) != 2 {
		t.Fatalf("expected 2 files, got %d (%v)", len(got), got)
	}
	// Order isn't guaranteed by the tree walk contract; sort by name.
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })

	if got[0].Name != "drivers/oem.inf" {
		t.Errorf("nested name = %q, want drivers/oem.inf", got[0].Name)
	}
	if got[0].URL != "/payload/software/9/files/drivers/oem.inf" {
		t.Errorf("nested url = %q", got[0].URL)
	}
	if got[1].Name != "setup.exe" {
		t.Errorf("flat name = %q, want setup.exe", got[1].Name)
	}
	if got[1].URL != "/payload/software/9/files/setup.exe" {
		t.Errorf("flat url = %q", got[1].URL)
	}
	for _, f := range got {
		if f.SizeBytes == 0 {
			t.Errorf("size should be non-zero for %s", f.Name)
		}
	}
}
