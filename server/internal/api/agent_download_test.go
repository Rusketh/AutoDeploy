package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestAgentDownloadIsPublicAndScoped(t *testing.T) {
	// A downloads dir with a stand-in agent binary.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "autodeploy-agent-windows-amd64.exe"), []byte("MZbinary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.NewBlobStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// NewBlobStore roots categories under dir; place the binary where
	// downloadsDirFor (Blobs.CategoryRoot("downloads")) will look.
	cat := blobs.CategoryRoot("downloads")
	_ = os.MkdirAll(cat, 0o755)
	if err := os.WriteFile(filepath.Join(cat, "autodeploy-agent-windows-amd64.exe"), []byte("MZbinary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	RegisterVersion(mux, Repos{Blobs: blobs})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Public GET (no session cookie) returns the binary bytes.
	resp, err := http.Get(srv.URL + "/api/v1/agent/download/autodeploy-agent-windows-amd64.exe")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent download status = %d, want 200", resp.StatusCode)
	}

	// Non-agent / traversal names are rejected (can't read arbitrary files).
	for _, bad := range []string{
		"secret.txt",
		"..%2f..%2fetc%2fpasswd",
		"autodeploy-agent-..%2fsecret.txt",
	} {
		r, err := http.Get(srv.URL + "/api/v1/agent/download/" + bad)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode == http.StatusOK {
			t.Errorf("name %q should not be served, got 200", bad)
		}
	}
}
