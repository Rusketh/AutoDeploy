package payload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Like newTestService but with driver matching wired in.
func newTestServiceWithDrivers(t *testing.T) (*httptest.Server, *Service, *model.ISORepo, *model.DriverPackageRepo, *model.ImageRepo) {
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
	mh := &ManifestHandler{Resolver: resolve.New(images, isos, unattend).WithDrivers(drivers)}
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc, isos, drivers, images
}

func TestManifestIncludesMatchedDrivers(t *testing.T) {
	ctx := context.Background()
	srv, _, isos, drivers, images := newTestServiceWithDrivers(t)

	// One driver package for Dell hardware, with a payload uploaded.
	dellPkg, _ := drivers.Create(ctx, model.DriverPackage{
		Name: "Dell-Latitude-5520",
		Filters: []model.DriverFilter{
			{FilterJSON: `{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520"}`},
		},
	})
	// Mark it "uploaded" so the manifest URL is produced.
	dellPkg.StoragePath = "drivers/" + itoa(dellPkg.ID) + "/payload.bin"
	dellPkg.SizeBytes = 42
	if err := drivers.Update(ctx, dellPkg); err != nil {
		t.Fatal(err)
	}
	// A second package for HP, should NOT match a Dell machine.
	_, _ = drivers.Create(ctx, model.DriverPackage{
		Name: "HP",
		Filters: []model.DriverFilter{
			{FilterJSON: `{"system_manufacturer":"HP"}`},
		},
	})

	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	iso.StoragePath = "iso/" + itoa(iso.ID) + "/files/sources/install.wim"
	_ = isos.Update(ctx, iso)
	isoID := iso.ID
	img, _ := images.Create(ctx, model.Image{Name: "win11-base", ISOID: &isoID})

	identity := match.Identity{
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
		SystemUUID:         "uuid-test",
	}
	body, _ := json.Marshal(identity)
	resp, err := http.Post(
		srv.URL+"/api/v1/images/"+itoa(img.ID)+"/manifest",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	var sawDellDriver, sawHP bool
	for _, it := range m.Items {
		if it.Role != "driver" {
			continue
		}
		if strings.Contains(it.Name, "Dell") {
			sawDellDriver = true
		}
		if strings.Contains(it.Name, "HP") {
			sawHP = true
		}
	}
	if !sawDellDriver {
		t.Errorf("expected Dell driver in manifest, items=%+v", m.Items)
	}
	if sawHP {
		t.Errorf("HP driver should not match a Dell machine, items=%+v", m.Items)
	}
}
