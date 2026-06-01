package api

import (
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestSemverLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.1.1", true},
		{"v0.1.1", "v0.1.0", false},
		{"v0.1.0", "v0.1.0", false},
		{"v0.9.0", "v0.10.0", true},
		{"v0.10.0", "v1.0.0", true},
		{"v1.0.0", "v0.99.99", false},
		{"v0.1.0-rc.1", "v0.1.0", true},  // pre-release loses to release
		{"v0.1.0", "v0.1.0-rc.1", false},
		{"v0.1.0-alpha", "v0.1.0-beta", true},
		// Non-semver strings -- "dev" loses to anything that parses.
		{"dev", "v0.1.0", true},
		{"v0.1.0", "dev", false},
	}
	for _, c := range cases {
		if got := semverLess(c.a, c.b); got != c.want {
			t.Errorf("semverLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestIsOlderVersion_EmptyOrDevAlwaysOlder(t *testing.T) {
	if !isOlderVersion("", "v0.1.0") {
		t.Error("empty current should be older")
	}
	if !isOlderVersion("dev", "v0.1.0") {
		t.Error("dev should be older than tagged")
	}
	if isOlderVersion("v0.1.0", "v0.1.0") {
		t.Error("equal versions: not older")
	}
	if !isOlderVersion("v0.1.0", "v0.1.1") {
		t.Error("v0.1.0 < v0.1.1")
	}
	// Regression: a non-semver AVAILABLE build must never trigger an update,
	// or a "dev" agent self-updates to an equally-"dev"/unversioned binary on
	// every poll and the resident loop exits each time (check-ins stop; queued
	// jobs like re-image are never claimed).
	if isOlderVersion("dev", "dev") {
		t.Error("dev -> dev must NOT be an update (self-update loop)")
	}
	if isOlderVersion("dev", "") {
		t.Error("dev -> unversioned must NOT be an update")
	}
	if isOlderVersion("v0.1.0", "dev") {
		t.Error("released -> dev must NOT be an update")
	}
}

func TestHandleVersion_ReturnsBuildVersion(t *testing.T) {
	SetServerVersion("v9.9.9")
	defer SetServerVersion("dev")
	rr := httptest.NewRecorder()
	handleVersion()(rr, httptest.NewRequest("GET", "/api/v1/version", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	var info VersionInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "v9.9.9" {
		t.Errorf("got version %q want v9.9.9", info.Version)
	}
	if info.GoVersion == "" || info.GOOS == "" || info.GOARCH == "" {
		t.Errorf("missing runtime info: %+v", info)
	}
}

func TestHandleAgentUpdateInfo_NoBinary_NoUpdate(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Blobs: blobs}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agent/update-info", nil)
	handleAgentUpdateInfo(r)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var info AgentUpdateInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.UpdateAvailable {
		t.Error("UpdateAvailable should be false with no binary present")
	}
	if info.URL != "" || info.SHA256 != "" {
		t.Errorf("URL/SHA should be empty: %+v", info)
	}
}

func TestHandleAgentUpdateInfo_NewerBinary_Advertises(t *testing.T) {
	tmp := t.TempDir()
	blobs, err := storage.NewBlobStore(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// Seed a fake agent binary + .version + .sha256 in the
	// downloads category.
	dlDir := blobs.CategoryRoot("downloads")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := []byte("fake binary contents")
	name := "autodeploy-agent-windows-amd64.exe"
	if err := os.WriteFile(filepath.Join(dlDir, name), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, name+".version"), []byte("v0.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Realistic .sha256 line shape so the readSHA256Sidecar parser
	// confirms it accepts "<hex>  <filename>" pairs.
	hash := computeSHA256(filepath.Join(dlDir, name))
	if hash == "" {
		t.Fatal("hash empty")
	}
	if err := os.WriteFile(filepath.Join(dlDir, name+".sha256"),
		[]byte(hash+"  "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Defence in depth: verify the SHA-256 we expect.
	if _, err := hex.DecodeString(hash); err != nil {
		t.Fatalf("hash not hex: %v", err)
	}

	r := Repos{Blobs: blobs}
	body := `{"os":"windows","arch":"amd64","current_version":"v0.1.0"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agent/update-info",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	handleAgentUpdateInfo(r)(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var info AgentUpdateInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Current != "v0.2.0" {
		t.Errorf("Current = %q want v0.2.0", info.Current)
	}
	if !info.UpdateAvailable {
		t.Errorf("UpdateAvailable should be true; got %+v", info)
	}
	if info.SHA256 != hash {
		t.Errorf("SHA256 = %q want %q", info.SHA256, hash)
	}
	if info.URL == "" || info.Size == 0 {
		t.Errorf("URL/Size unset: %+v", info)
	}
}

func TestHandleAgentUpdateInfo_SameVersion_NoUpdate(t *testing.T) {
	tmp := t.TempDir()
	blobs, _ := storage.NewBlobStore(tmp)
	dlDir := blobs.CategoryRoot("downloads")
	_ = os.MkdirAll(dlDir, 0o755)
	name := "autodeploy-agent-windows-amd64.exe"
	_ = os.WriteFile(filepath.Join(dlDir, name), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dlDir, name+".version"), []byte("v0.2.0"), 0o644)
	r := Repos{Blobs: blobs}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agent/update-info",
		strings.NewReader(`{"current_version":"v0.2.0"}`))
	req.Header.Set("Content-Type", "application/json")
	handleAgentUpdateInfo(r)(rr, req)
	var info AgentUpdateInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.UpdateAvailable {
		t.Errorf("UpdateAvailable should be false for same version; got %+v", info)
	}
}


func TestLooksLikeSemver(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"v0.1.0", true},
		{"v10.20.300", true},
		{"v0.1.0-rc.1", true},
		{"v0.1.0+build.5", true},
		// Reject anything we'd be uncomfortable handing to a shell.
		{"0.1.0", false},          // missing v prefix
		{"v0.1", false},           // only two parts
		{"v0.1.0.0", false},       // four parts
		{"v0.1.x", false},         // non-numeric
		{"v0.1.0; rm -rf /", false},
		{"v0.1.0 && evil", false},
		{"", false},
		{"latest", false},
	}
	for _, c := range cases {
		if got := looksLikeSemver(c.in); got != c.want {
			t.Errorf("looksLikeSemver(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestHandleServerUpdate_RejectsBogusTag(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/server/update",
		strings.NewReader(`{"tag":"; rm -rf /"}`))
	req.Header.Set("Content-Type", "application/json")
	handleServerUpdate(Repos{})(rr, req)
	if rr.Code != 400 {
		t.Errorf("expected 400 for bogus tag, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestHandleServerUpdate_MissingHelperReturns503(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/server/update",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	handleServerUpdate(Repos{})(rr, req)
	// On a host without /usr/local/sbin/autodeploy-update installed
	// (the CI runners aren't pre-seeded with it), we expect 503.
	// If the runner happens to have one installed -- unlikely but
	// possible -- we accept 202 too.
	if rr.Code != 503 && rr.Code != 202 {
		t.Errorf("expected 503 or 202, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}
