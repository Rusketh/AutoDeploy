package payload

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// compressibleDriverZip returns a zip whose UNCOMPRESSED contents are far
// larger than the zip itself — a stand-in for a real driver package, where the
// served zip is a fraction of the extracted tree. The reported uncompressed
// total lets a test assert the two byte counts never get conflated.
func compressibleDriverZip(t *testing.T) (zipBytes []byte, uncompressed int64) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	big := make([]byte, 4<<20) // 4 MiB of zeros -> compresses to almost nothing
	w, err := zw.Create("driver/big.sys")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(big); err != nil {
		t.Fatal(err)
	}
	inf, err := zw.Create("driver/test.inf")
	if err != nil {
		t.Fatal(err)
	}
	infBody := []byte("[Version]\nClass=Net\nProvider=Test\n")
	if _, err := inf.Write(infBody); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), int64(len(big) + len(infBody))
}

func newDriverEnv(t *testing.T) (*storage.BlobStore, *model.DriverPackageRepo, *model.ISORepo, *model.ImageRepo, *resolve.Resolver) {
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
	images := model.NewImageRepo(db)
	res := resolve.New(images, isos, unattend).WithDrivers(drivers)
	return blobs, drivers, isos, images, res
}

// TestExtractDriverDoesNotInflateSizeBytes locks in the fix for the deploy-
// stalling bug: extracting a driver package must leave SizeBytes equal to the
// served zip blob, NOT the (larger) uncompressed extract total. The manifest
// hands SizeBytes to the Boot Client as the download length; recording the
// uncompressed total made the client wait forever for bytes the server never
// serves, stalling the deploy.
func TestExtractDriverDoesNotInflateSizeBytes(t *testing.T) {
	ctx := context.Background()
	blobs, drivers, _, _, _ := newDriverEnv(t)

	pkg, err := drivers.Create(ctx, model.DriverPackage{Name: "pkg"})
	if err != nil {
		t.Fatal(err)
	}
	zipBytes, uncompressed := compressibleDriverZip(t)
	rel := "drivers/" + itoa(pkg.ID) + "/payload.bin"
	n, err := blobs.WriteStream(rel, bytes.NewReader(zipBytes))
	if err != nil {
		t.Fatal(err)
	}
	pkg.StoragePath = rel
	pkg.SizeBytes = n // what uploadDriver records: the served zip's size
	if err := drivers.Update(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	res, err := ExtractDriverPackage(ctx, drivers, blobs, pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Guard the test's own premise: the extract total must exceed the zip,
	// otherwise it can't distinguish the two values.
	if res.Bytes <= n {
		t.Fatalf("test premise broken: extracted %d not greater than zip %d", res.Bytes, n)
	}
	if res.Bytes != uncompressed {
		t.Errorf("extracted bytes = %d, want %d", res.Bytes, uncompressed)
	}
	reloaded, err := drivers.Get(ctx, pkg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.SizeBytes != n {
		t.Errorf("SizeBytes after extract = %d, want the served zip size %d (not the %d extract total)",
			reloaded.SizeBytes, n, res.Bytes)
	}
}

// TestBootCriticalDirsClassifiesINFs proves only storage/system/network INFs
// are flagged for WinPE; device drivers like Bluetooth and Display are
// excluded so they can't abort Win11 24H2 Setup from $WinPEDriver$.
func TestBootCriticalDirsClassifiesINFs(t *testing.T) {
	meta := DriverExtractResult{INFs: []INFEntry{
		{Path: "Storage/RST/iaStorAC.inf", Class: "SCSIAdapter"},
		{Path: "Net/e1d/e1d.inf", Class: "Net"},
		{Path: "Bluetooth/ibt/oem43.inf", Class: "Bluetooth"},
		// System by ClassGuid only (no friendly Class=).
		{Path: "Chipset/x/foo.inf", ClassGUID: "{4D36E97D-E325-11CE-BFC1-08002BE10318}"},
		{Path: "Display/gpu/nv.inf", Class: "Display"},
	}}
	got := bootCriticalDirs(meta)
	want := map[string]bool{"Storage/RST": true, "Net/e1d": true, "Chipset/x": true}
	if len(got) != len(want) {
		t.Fatalf("bootCriticalDirs = %v, want exactly the keys of %v", got, want)
	}
	for _, d := range got {
		if !want[d] {
			t.Errorf("unexpected boot-critical dir %q (Bluetooth/Display must be excluded): %v", d, got)
		}
	}
}

// TestBootCriticalDirsRootINF: a boot-critical INF sitting at the package root
// (no folder to isolate) means the whole package is one boot driver -> ".".
func TestBootCriticalDirsRootINF(t *testing.T) {
	meta := DriverExtractResult{INFs: []INFEntry{{Path: "iaStorAC.inf", Class: "SCSIAdapter"}}}
	got := bootCriticalDirs(meta)
	if len(got) != 1 || got[0] != "." {
		t.Errorf(`root boot-critical INF => %v, want ["."]`, got)
	}
}

// TestParseINFReadsClassGuid confirms the new ClassGuid fallback parses and
// drives classification when an INF omits the friendly Class= name.
func TestParseINFReadsClassGuid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "test.inf")
	if err := os.WriteFile(p, []byte("[Version]\nClassGuid={4d36e972-e325-11ce-bfc1-08002be10318}\nProvider=Acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := parseINF(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(e.ClassGUID, "{4d36e972-e325-11ce-bfc1-08002be10318}") {
		t.Errorf("ClassGUID = %q, want the Net GUID", e.ClassGUID)
	}
	if !isBootCriticalINF(e) {
		t.Error("a Net-by-GUID INF should be boot-critical")
	}
}

// TestManifestDriverSizeTracksBlob proves the manifest reports the actual
// served blob size, self-healing a row whose SizeBytes column is wrong (e.g.
// left inflated by the old extract bug) with no re-extract required.
func TestManifestDriverSizeTracksBlob(t *testing.T) {
	ctx := context.Background()
	blobs, drivers, isos, images, resolver := newDriverEnv(t)

	pkg, err := drivers.Create(ctx, model.DriverPackage{
		Name:    "Dell",
		Filters: []model.DriverFilter{{FilterJSON: `{"system_manufacturer":"Dell Inc."}`}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Write a real blob and DELIBERATELY record a wrong (inflated) SizeBytes,
	// mimicking a row corrupted by the old extract behaviour.
	rel := "drivers/" + itoa(pkg.ID) + "/payload.bin"
	blob := bytes.Repeat([]byte("x"), 4096)
	if _, err := blobs.WriteStream(rel, bytes.NewReader(blob)); err != nil {
		t.Fatal(err)
	}
	pkg.StoragePath = rel
	pkg.SizeBytes = 999_999_999 // wrong on purpose
	if err := drivers.Update(ctx, pkg); err != nil {
		t.Fatal(err)
	}

	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	iso.StoragePath = "iso/" + itoa(iso.ID) + "/files/sources/install.wim"
	_ = isos.Update(ctx, iso)
	isoID := iso.ID
	img, _ := images.Create(ctx, model.Image{Name: "win11", ISOID: &isoID})

	mh := &ManifestHandler{Resolver: resolver, Blobs: blobs}
	m, err := mh.BuildForSite(ctx, img.ID, "http://server", match.Identity{
		SystemManufacturer: "Dell Inc.",
		SystemUUID:         "uuid-1",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range m.Items {
		if it.Role != "driver" {
			continue
		}
		found = true
		if it.Size != int64(len(blob)) {
			t.Errorf("driver item Size = %d, want actual blob size %d (not the stale %d)",
				it.Size, len(blob), 999_999_999)
		}
	}
	if !found {
		t.Fatalf("expected a driver item in manifest, got %+v", m.Items)
	}
}
