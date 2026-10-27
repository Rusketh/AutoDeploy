package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// newExportTestServer is newTestServer plus a blob store, with one image
// linked to a deploy-ready ISO that carries the given editions.
func newExportTestServer(t *testing.T, editions []string, unattendJSON string) (*httptest.Server, Repos, model.ID) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isos, uas, images := model.NewISORepo(db), model.NewUnattendRepo(db), model.NewImageRepo(db)
	repos := Repos{
		ISOs: isos, Unattend: uas, Images: images,
		Drivers:  model.NewDriverPackageRepo(db),
		Software: model.NewSoftwarePackageRepo(db),
		Resolver: resolve.New(images, isos, uas),
		Users:    auth.New(db),
		Blobs:    blobs,
	}
	iso, err := isos.Create(ctx, model.ISO{Name: "Win11 Lab", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}
	iso.StoragePath = fmt.Sprintf("iso/%d/files/sources/install.wim", int64(iso.ID))
	iso.BootloaderPresent = true
	iso.Editions = editions
	if err := isos.Update(ctx, iso); err != nil {
		t.Fatal(err)
	}
	img := model.Image{Name: "Lab", ISOID: &iso.ID}
	if unattendJSON != "" {
		ua, err := uas.Create(ctx, model.Unattend{Name: "ua", SettingsJSON: unattendJSON})
		if err != nil {
			t.Fatal(err)
		}
		img.UnattendID = &ua.ID
	}
	created, err := images.Create(ctx, img)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos, created.ID
}

func fakeISO() []byte {
	b := make([]byte, 0x9000)
	copy(b[0x8001:], "CD001")
	copy(b, "OLDISO")
	return b
}

func TestExportDownload_NoISOIs404(t *testing.T) {
	srv, repos, id := newExportTestServer(t, nil, "")
	client := authedClient(t, srv, repos)
	resp, body := doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Build ISO") {
		t.Errorf("unfriendly 404 body: %s", body)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestExportDownload_RejectsTruncatedFile(t *testing.T) {
	srv, repos, id := newExportTestServer(t, nil, "")
	client := authedClient(t, srv, repos)
	path, _ := payload.ExportOutputPath(repos.Blobs, id)
	// An empty / non-ISO file at the final path (e.g. a crashed old build).
	if err := os.WriteFile(path, []byte("truncated"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, _ := doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a non-ISO file", resp.StatusCode)
	}
	// A .partial (a build in flight or interrupted) is never served either.
	_ = os.Remove(path)
	_ = os.WriteFile(path+".partial", fakeISO(), 0o644)
	resp, _ = doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when only a .partial exists", resp.StatusCode)
	}
}

// An ISO a previous server version exported straight to the final path must
// still download after the upgrade, and the status endpoint reports it ready.
func TestExportDownload_ServesExistingISO(t *testing.T) {
	srv, repos, id := newExportTestServer(t, nil, "")
	client := authedClient(t, srv, repos)
	path, _ := payload.ExportOutputPath(repos.Blobs, id)
	want := fakeISO()
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	resp, body := doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusOK || string(body) != string(want) {
		t.Fatalf("status = %d, %d bytes", resp.StatusCode, len(body))
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, fmt.Sprintf("autodeploy-image-%d.iso", int64(id))) {
		t.Errorf("Content-Disposition = %q", cd)
	}

	resp, body = doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/status", srv.URL, id), nil)
	var st payload.ExportStatus
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st.Phase != payload.ExportDone || st.SizeBytes != int64(len(want)) || st.BuiltAt == nil {
		t.Errorf("status for an on-disk ISO = %+v", st)
	}
}

func TestExportStart_EditionMismatchIs422(t *testing.T) {
	if !payload.ISOBuilderAvailable() {
		t.Skip("xorriso not installed (the start endpoint checks it first)")
	}
	srv, repos, id := newExportTestServer(t, []string{"Windows 11 Education"}, `{"edition":"Windows 11 Enterprise"}`)
	client := authedClient(t, srv, repos)
	resp, body := doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/v1/images/%d/export", srv.URL, id), nil)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d (%s), want 422", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Windows 11 Education") || !strings.Contains(string(body), `"Windows 11 Enterprise"`) {
		t.Errorf("message should name both editions: %s", body)
	}
	if st := payload.ExportStatusFor(id); st.Phase != payload.ExportIdle {
		t.Errorf("a rejected export must not start a job: %+v", st)
	}
}

// blockingRunner stands in for xorriso and blocks until cancelled.
type blockingRunner struct{ started chan struct{} }

func (b blockingRunner) Exec(ctx context.Context, _ string, _ ...string) error {
	close(b.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestExportDownload_409WhileBuilding(t *testing.T) {
	if !payload.ISOBuilderAvailable() {
		t.Skip("xorriso not installed (ExportImageISO checks it first)")
	}
	srv, repos, id := newExportTestServer(t, nil, "")
	client := authedClient(t, srv, repos)
	// A previous good ISO exists — it must still not be served mid-rebuild.
	path, _ := payload.ExportOutputPath(repos.Blobs, id)
	_ = os.WriteFile(path, fakeISO(), 0o644)
	res, _ := repos.Resolver.Resolve(context.Background(), id)
	media, _ := repos.Blobs.EnsureDir(fmt.Sprintf("iso/%d/files/BOOT", int64(res.ISO.ID)))
	_ = os.WriteFile(media+"/ETFSBOOT.COM", make([]byte, 4096), 0o644)

	run := blockingRunner{started: make(chan struct{})}
	deps := exportDeps(repos)
	deps.Runner = run
	if !payload.StartExportAsync(deps, payload.ISOExportOptions{ImageID: id, OutPath: path}) {
		t.Fatal("export did not start")
	}
	<-run.started
	resp, body := doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "still building") {
		t.Errorf("download while building: %d %s", resp.StatusCode, body)
	}
	resp, body = doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/v1/images/%d/export/cancel", srv.URL, id), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"cancelled":true`) {
		t.Errorf("cancel: %d %s", resp.StatusCode, body)
	}
	for i := 0; i < 500 && !payload.ExportStatusFor(id).Finished; i++ {
		<-time.After(10 * time.Millisecond)
	}
	// After the cancel the previous ISO downloads again, and the status keeps
	// the error while pointing at the surviving ISO.
	resp, _ = doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/download", srv.URL, id), nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("previous ISO not downloadable after cancel: %d", resp.StatusCode)
	}
	_, body = doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/images/%d/export/status", srv.URL, id), nil)
	var st payload.ExportStatus
	_ = json.Unmarshal(body, &st)
	if st.Phase != payload.ExportError || st.SizeBytes == 0 {
		t.Errorf("status after cancel = %+v", st)
	}
}

func TestExportCancel_NothingRunning(t *testing.T) {
	srv, repos, id := newExportTestServer(t, nil, "")
	client := authedClient(t, srv, repos)
	resp, body := doJSON(t, client, http.MethodPost, fmt.Sprintf("%s/api/v1/images/%d/export/cancel", srv.URL, id), nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"cancelled":false`) {
		t.Errorf("cancel with nothing running: %d %s", resp.StatusCode, body)
	}
}
