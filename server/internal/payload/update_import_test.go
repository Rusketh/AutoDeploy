package payload

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newImportTestEnv(t *testing.T) (*storage.BlobStore, *model.WindowsUpdateRepo, model.ID) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	updates := model.NewWindowsUpdateRepo(db, model.NewInventoryRepo(db))
	u, err := updates.Create(context.Background(), model.WindowsUpdate{
		KBNumber: "KB5034122", Title: "2024-01 Cumulative Update",
	})
	if err != nil {
		t.Fatal(err)
	}
	return blobs, updates, u.ID
}

// waitImport polls until the import for id finishes or the deadline hits.
func waitImport(t *testing.T, id model.ID) ImportStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := ImportStatusFor(id); s.Finished {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("import %d did not finish: %+v", id, ImportStatusFor(id))
	return ImportStatus{}
}

func TestStartUpdateImportAsync(t *testing.T) {
	blobs, updates, id := newImportTestEnv(t)
	payload := strings.Repeat("msu-bytes ", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)

	StartUpdateImportAsync(blobs, updates, id,
		func(ctx context.Context) ([]string, error) {
			return []string{srv.URL + "/d/msdownload/windows10.0-kb5034122-x64.msu"}, nil
		},
		func(links []string) string { return links[0] })

	s := waitImport(t, id)
	if s.Phase != ImportDone || s.Error != "" {
		t.Fatalf("status = %+v", s)
	}
	if s.Filename != "windows10.0-kb5034122-x64.msu" || s.Percent != 100 {
		t.Errorf("status = %+v", s)
	}

	// The row records the payload exactly like a manual upload.
	u, err := updates.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	wantRel := fmt.Sprintf("updates/%d/windows10.0-kb5034122-x64.msu", int64(id))
	if u.StoragePath != wantRel || u.PayloadFilename != "windows10.0-kb5034122-x64.msu" ||
		u.SizeBytes != int64(len(payload)) {
		t.Errorf("payload fields = %q %q %d", u.StoragePath, u.PayloadFilename, u.SizeBytes)
	}
	// And the bytes are on disk under the blob root.
	abs, err := blobs.Resolve(u.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(abs)
	if err != nil || len(b) != len(payload) {
		t.Errorf("blob on disk: %v len=%d", err, len(b))
	}
}

// A failed resolution leaves the row payloadless and allows a retry.
func TestStartUpdateImportAsyncFailureAndRetry(t *testing.T) {
	blobs, updates, id := newImportTestEnv(t)

	StartUpdateImportAsync(blobs, updates, id,
		func(ctx context.Context) ([]string, error) {
			return nil, errors.New("catalog unreachable")
		},
		func(links []string) string { return "" })

	s := waitImport(t, id)
	if s.Phase != ImportError || !strings.Contains(s.Error, "catalog unreachable") {
		t.Fatalf("status = %+v", s)
	}
	u, _ := updates.Get(context.Background(), id)
	if u.StoragePath != "" {
		t.Errorf("row should stay payloadless after failure, got %q", u.StoragePath)
	}

	// Retry succeeds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("retry-bytes"))
	}))
	t.Cleanup(srv.Close)
	StartUpdateImportAsync(blobs, updates, id,
		func(ctx context.Context) ([]string, error) {
			return []string{srv.URL + "/kb.msu"}, nil
		},
		func(links []string) string { return links[0] })
	if s := waitImport(t, id); s.Phase != ImportDone {
		t.Fatalf("retry status = %+v", s)
	}
}

func TestImportFilename(t *testing.T) {
	tests := []struct{ url, want string }{
		{"https://catalog.s.download.windowsupdate.com/d/secu/windows10.0-kb5034122-x64_abc.msu",
			"windows10.0-kb5034122-x64_abc.msu"},
		{"https://x/", "update.msu"},
		{"://bad url", "update.msu"},
		// Encoded traversal: base-stripping leaves a bare, safe filename.
		{"https://x/a%2F..%2Fevil.msu", "evil.msu"},
		{"https://x/..%2F..", "update.msu"},
		{`https://x/dir/evil\name.msu`, "update.msu"},
	}
	for _, tt := range tests {
		if got := importFilename(tt.url); got != tt.want {
			t.Errorf("importFilename(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
