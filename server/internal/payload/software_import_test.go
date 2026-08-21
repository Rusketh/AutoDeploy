package payload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func waitSoftwareImport(t *testing.T, id model.ID) SoftwareImportStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := SoftwareImportStatusFor(id)
		if s.Finished {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import did not finish in time")
	return SoftwareImportStatus{}
}

func TestStartSoftwareImportAsyncMultiFileSuccess(t *testing.T) {
	prereq := []byte("prerequisite installer bytes")
	app := []byte("main application installer bytes, a bit longer")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prereq.exe":
			_, _ = w.Write(prereq)
		case "/app.msi":
			_, _ = w.Write(app)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := softwareImportHTTPClient
	softwareImportHTTPClient = srv.Client()
	defer func() { softwareImportHTTPClient = old }()

	id := model.ID(42)
	StartSoftwareImportAsync(blobs, id, []SoftwareDownloadFile{
		{URL: srv.URL + "/prereq.exe", Filename: "prereq.exe", SHA256: sha256Hex(prereq)},
		{URL: srv.URL + "/app.msi", Filename: "app.msi", SHA256: sha256Hex(app)},
	})

	s := waitSoftwareImport(t, id)
	if s.Error != "" {
		t.Fatalf("import failed: %s", s.Error)
	}
	if s.Phase != SoftwareImportDone || s.Percent != 100 {
		t.Errorf("final status = %+v", s)
	}
	// Both files landed under software/<id>/files/.
	for name, want := range map[string][]byte{"prereq.exe": prereq, "app.msi": app} {
		sz, err := blobs.Size(softwarePackageFileRel(id, name))
		if err != nil {
			t.Fatalf("size %s: %v", name, err)
		}
		if sz != int64(len(want)) {
			t.Errorf("%s size = %d, want %d", name, sz, len(want))
		}
	}
}

func TestStartSoftwareImportAsyncChecksumMismatch(t *testing.T) {
	body := []byte("some installer bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := softwareImportHTTPClient
	softwareImportHTTPClient = srv.Client()
	defer func() { softwareImportHTTPClient = old }()

	id := model.ID(43)
	StartSoftwareImportAsync(blobs, id, []SoftwareDownloadFile{
		{URL: srv.URL + "/x.msi", Filename: "x.msi", SHA256: "deadbeef"},
	})

	s := waitSoftwareImport(t, id)
	if s.Error == "" {
		t.Fatal("expected a checksum-mismatch error")
	}
	if s.Phase != SoftwareImportError {
		t.Errorf("phase = %s, want error", s.Phase)
	}
	// The corrupt file must not be left behind.
	if _, err := blobs.Size(softwarePackageFileRel(id, "x.msi")); err == nil {
		t.Error("mismatched file should have been removed")
	}
}

func TestStartSoftwareImportAsyncDownloadError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old := softwareImportHTTPClient
	softwareImportHTTPClient = srv.Client()
	defer func() { softwareImportHTTPClient = old }()

	id := model.ID(44)
	StartSoftwareImportAsync(blobs, id, []SoftwareDownloadFile{
		{URL: srv.URL + "/missing.exe", Filename: "missing.exe"},
	})
	s := waitSoftwareImport(t, id)
	if s.Error == "" || s.Phase != SoftwareImportError {
		t.Errorf("expected download error, got %+v", s)
	}
	_ = context.Background()
}
