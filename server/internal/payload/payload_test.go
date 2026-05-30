package payload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newTestService(t *testing.T) (*httptest.Server, *Service, *model.ISORepo, *model.SoftwarePackageRepo, *model.ImageRepo) {
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
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	images := model.NewImageRepo(db)
	svc := &Service{Blobs: blobs, ISOs: isos, Drivers: drivers, Software: software}
	mux := http.NewServeMux()
	svc.Register(mux)
	mh := &ManifestHandler{Resolver: resolve.New(images, isos, unattend)}
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc, isos, software, images
}

// buildTinyISO constructs a one-file ISO9660 image in a temp file using the
// same library the server uses to read it. Returns the path on disk.
func buildTinyISO(t *testing.T, fileName, content string) string {
	t.Helper()
	writer, err := iso9660.NewWriter()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Cleanup()
	if err := writer.AddFile(strings.NewReader(content), fileName); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "tiny.iso")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteTo(f, "AUTODEPLOY_TEST"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestISOUploadExtractAndServe(t *testing.T) {
	ctx := context.Background()
	srv, _, isos, _, _ := newTestService(t)

	iso, err := isos.Create(ctx, model.ISO{Name: "Tiny", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}

	// Build a tiny ISO whose payload is sources/install.wim, mirroring real
	// Windows media (where PrepareBootMedia looks for the install image).
	srcISO := buildTinyISO(t, "sources/install.wim", "this-is-the-fake-wim")

	// PUT upload.
	body, err := os.Open(srcISO)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/v1/isos/"+itoa(iso.ID)+"/upload", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, b)
	}
	resp.Body.Close()

	// POST extract.
	resp, err = http.Post(srv.URL+"/api/v1/isos/"+itoa(iso.ID)+"/extract",
		"application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("extract status=%d body=%s", resp.StatusCode, b)
	}
	var ext map[string]any
	_ = json.Unmarshal(b, &ext)
	// extract now reports the prepared install image via "install_path"
	// (sources/install.wim for this small fake WIM -- under the split
	// threshold, so no .swm split).
	if ip, _ := ext["install_path"].(string); !strings.EqualFold(ip, "sources/install.wim") {
		t.Errorf("install_path = %v", ext["install_path"])
	}

	// GET the served install image — try case variants because the ISO9660
	// writer may uppercase path components.
	for _, name := range []string{"sources/install.wim", "SOURCES/INSTALL.WIM"} {
		resp, err = http.Get(srv.URL + "/payload/iso/" + itoa(iso.ID) + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode == http.StatusOK {
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if !bytes.Equal(got, []byte("this-is-the-fake-wim")) {
				t.Errorf("served content = %q", got)
			}
			return
		}
		resp.Body.Close()
	}
	t.Errorf("could not GET sources/install.wim under either casing")
}

// TestISOMediaIndex verifies the media-index endpoint lists the extracted
// tree so the Boot Client can mirror the whole media (boot-the-media
// deploy). Upload now auto-extracts, so no explicit extract call is made.
func TestISOMediaIndex(t *testing.T) {
	ctx := context.Background()
	srv, _, isos, _, _ := newTestService(t)

	iso, err := isos.Create(ctx, model.ISO{Name: "Media", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}
	srcISO := buildTinyISO(t, "sources/install.wim", "fake-wim-bytes")
	body, err := os.Open(srcISO)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/v1/isos/"+itoa(iso.ID)+"/upload", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status=%d body=%s", resp.StatusCode, b)
	}
	// Upload auto-extracts; assert the response says so.
	var up map[string]any
	_ = json.Unmarshal(b, &up)
	if ex, _ := up["extracted"].(bool); !ex {
		t.Fatalf("expected auto-extract on upload; got %s", b)
	}

	// Media index should list at least the install.wim file.
	resp, err = http.Get(srv.URL + "/payload/iso/" + itoa(iso.ID) + "/index.json")
	if err != nil {
		t.Fatal(err)
	}
	ib, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index status=%d body=%s", resp.StatusCode, ib)
	}
	var idx MediaIndex
	if err := json.Unmarshal(ib, &idx); err != nil {
		t.Fatalf("decode index: %v (%s)", err, ib)
	}
	found := false
	for _, f := range idx.Files {
		if strings.EqualFold(f.Path, "sources/install.wim") {
			found = true
			if f.Size == 0 {
				t.Errorf("install.wim listed with size 0")
			}
		}
	}
	if !found {
		t.Errorf("media index missing sources/install.wim: %+v", idx.Files)
	}
}

func TestSoftwareUploadAndRangeServe(t *testing.T) {
	ctx := context.Background()
	srv, _, _, software, _ := newTestService(t)

	pkg, err := software.Create(ctx, model.SoftwarePackage{Name: "demo-pkg"})
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("ABCDEFGH", 128) // 1024 bytes
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/v1/software/"+itoa(pkg.ID)+"/upload",
		strings.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Full GET.
	resp, err = http.Get(srv.URL + "/payload/software/" + itoa(pkg.ID))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != content {
		t.Errorf("full content mismatch")
	}

	// Range GET — bytes=10-19.
	req, _ = http.NewRequest(http.MethodGet,
		srv.URL+"/payload/software/"+itoa(pkg.ID), nil)
	req.Header.Set("Range", "bytes=10-19")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", resp.StatusCode)
	}
	got, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != content[10:20] {
		t.Errorf("range content = %q, want %q", got, content[10:20])
	}
}

func TestManifestIncludesPayloadURLs(t *testing.T) {
	ctx := context.Background()
	srv, _, isos, software, images := newTestService(t)

	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	// Simulate the result of an extract.
	iso.StoragePath = "iso/" + itoa(iso.ID) + "/files/sources/install.wim"
	if err := isos.Update(ctx, iso); err != nil {
		t.Fatal(err)
	}

	swA, _ := software.Create(ctx, model.SoftwarePackage{Name: "A"})

	isoID := iso.ID
	img, _ := images.Create(ctx, model.Image{
		Name: "deploy", ISOID: &isoID,
		SoftwareLinks: []model.ImageSoftwareLink{{PackageID: swA.ID, OrderValue: 100}},
	})

	resp, err := http.Get(srv.URL + "/api/v1/images/" + itoa(img.ID) + "/manifest")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", resp.StatusCode, b)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 2 {
		t.Fatalf("expected 2 items, got %+v", m.Items)
	}
	var sawWIM, sawSW bool
	for _, it := range m.Items {
		if it.Role == "iso-wim" && strings.HasSuffix(it.URL, "/sources/install.wim") {
			sawWIM = true
		}
		if it.Role == "software" && strings.HasSuffix(it.URL, "/payload/software/"+itoa(swA.ID)) {
			sawSW = true
		}
	}
	if !sawWIM {
		t.Error("manifest missing iso-wim item with /sources/install.wim URL")
	}
	if !sawSW {
		t.Error("manifest missing software item")
	}
}

func itoa(id model.ID) string {
	b, _ := json.Marshal(id)
	return strings.Trim(string(b), `"`)
}
