package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestHandleCredProviderInfo_NoDLL_Empty(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Blobs: blobs}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agent/credprovider-info", nil)
	handleCredProviderInfo(r)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var info CredProviderInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.URL != "" || info.SHA256 != "" {
		t.Errorf("expected empty info with no DLL present: %+v", info)
	}
}

func TestHandleCredProviderInfo_Advertises(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	dlDir := blobs.CategoryRoot("downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "autodeploy-credprovider-windows-amd64.dll"
	if err := os.WriteFile(filepath.Join(dlDir, name), []byte("MZ fake dll"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, name+".version"), []byte("v0.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := computeSHA256(filepath.Join(dlDir, name))
	if err := os.WriteFile(filepath.Join(dlDir, name+".sha256"),
		[]byte(hash+"  "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := Repos{Blobs: blobs}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agent/credprovider-info", nil)
	handleCredProviderInfo(r)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var info CredProviderInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "v0.2.0" {
		t.Errorf("Version = %q want v0.2.0", info.Version)
	}
	if info.SHA256 != hash {
		t.Errorf("SHA256 = %q want %q", info.SHA256, hash)
	}
	if info.URL == "" || info.Size == 0 {
		t.Errorf("URL/Size unset: %+v", info)
	}
	if want := "/api/v1/agent/download/" + name; !strings.HasSuffix(info.URL, want) {
		t.Errorf("URL = %q, want suffix %q", info.URL, want)
	}
}

// TestHandleAgentDownload_AllowsCredProviderPrefix ensures the public download
// route serves the DLL (and its sidecars) but still rejects arbitrary names.
func TestHandleAgentDownload_AllowsCredProviderPrefix(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	dlDir := blobs.CategoryRoot("downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "autodeploy-credprovider-windows-amd64.dll"
	if err := os.WriteFile(filepath.Join(dlDir, name), []byte("MZdll"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Repos{Blobs: blobs}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agent/download/"+name, nil)
	req.SetPathValue("name", name)
	handleAgentDownload(r)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("download status %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "MZdll" {
		t.Errorf("body = %q want MZdll", rr.Body.String())
	}

	// A name without an allowed prefix is rejected (no arbitrary reads).
	rr2 := httptest.NewRecorder()
	bad := httptest.NewRequest("GET", "/api/v1/agent/download/evil", nil)
	bad.SetPathValue("name", "evil")
	handleAgentDownload(r)(rr2, bad)
	if rr2.Code != 404 {
		t.Errorf("bad-name status = %d want 404", rr2.Code)
	}
}
