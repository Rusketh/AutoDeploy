package portal

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// denyOpen makes openDownload fail with a permission error for the named
// file (tests usually run as root, which can read anything, so a real
// chmod can't reproduce it).
func denyOpen(t *testing.T, name string) {
	t.Helper()
	orig := openDownload
	openDownload = func(path string) (*os.File, error) {
		if filepath.Base(path) == name {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrPermission}
		}
		return orig(path)
	}
	t.Cleanup(func() { openDownload = orig })
}

func downloadsRepos(t *testing.T) (Repos, string) {
	t.Helper()
	data := t.TempDir()
	dir := filepath.Join(data, "downloads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"autodeploy-agent-windows-amd64.exe", "autodeploy-boot-linux-amd64"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("binary:"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return Repos{DataDir: data, Branding: branding.New(db)}, dir
}

func serveDownload(r Repos, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/portal/downloads/file/"+name, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	downloadsServe(r)(rec, req)
	return rec
}

func TestDownloadsServeReadable(t *testing.T) {
	r, _ := downloadsRepos(t)
	rec := serveDownload(r, "autodeploy-agent-windows-amd64.exe")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `attachment; filename="autodeploy-agent-windows-amd64.exe"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if got := rec.Body.String(); got != "binary:autodeploy-agent-windows-amd64.exe" {
		t.Errorf("body = %q", got)
	}
}

// A file the server can't read must explain itself (and how to fix it)
// rather than send a bare 403 the browser reports as "Failed - Forbidden".
func TestDownloadsServeUnreadableExplains(t *testing.T) {
	r, dir := downloadsRepos(t)
	denyOpen(t, "autodeploy-agent-windows-amd64.exe")
	rec := serveDownload(r, "autodeploy-agent-windows-amd64.exe")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Errorf("error page must not be sent as an attachment (Content-Disposition = %q)", cd)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "can&#39;t read this file") && !strings.Contains(body, "can't read this file") {
		t.Errorf("missing explanation in body:\n%s", body)
	}
	if runtime.GOOS != "windows" && !strings.Contains(body, "sudo chown") {
		t.Errorf("missing fix command in body")
	}
	if !strings.Contains(body, filepath.Join(dir, "autodeploy-agent-windows-amd64.exe")) {
		t.Errorf("fix command should name the file's full path")
	}
}

func TestDownloadsServeMissing(t *testing.T) {
	r, _ := downloadsRepos(t)
	if rec := serveDownload(r, "autodeploy-agent-windows-arm64.exe"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDownloadsListFlagsUnreadable(t *testing.T) {
	r, _ := downloadsRepos(t)
	denyOpen(t, "autodeploy-agent-windows-amd64.exe")
	req := httptest.NewRequest("GET", "/portal/downloads", nil)
	rec := httptest.NewRecorder()
	downloadsList(r)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Count(body, "Not readable") != 1 {
		t.Errorf("want exactly one Not readable badge, body:\n%s", body)
	}
	if !strings.Contains(body, "can&#39;t read 1 of these files") && !strings.Contains(body, "can't read 1 of these files") {
		t.Errorf("missing unreadable banner")
	}
	if strings.Contains(body, `.exe" download>`) || strings.Contains(body, "\" download>") {
		t.Errorf("download links must not carry the download attribute")
	}
}

func TestDownloadsListNoBannerWhenReadable(t *testing.T) {
	r, _ := downloadsRepos(t)
	req := httptest.NewRequest("GET", "/portal/downloads", nil)
	rec := httptest.NewRecorder()
	downloadsList(r)(rec, req)
	if strings.Contains(rec.Body.String(), "Not readable") {
		t.Errorf("readable files must not be flagged")
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/var/lib/autodeploy/downloads/a.exe": "/var/lib/autodeploy/downloads/a.exe",
		"/srv/my downloads/a.exe":             "'/srv/my downloads/a.exe'",
		"it's":                                `'it'"'"'s'`,
		"":                                    "''",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
