package payload

import (
	"context"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestManifestRewritesToSiteMirror(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	loadouts := model.NewSoftwareLoadoutRepo(db)
	images := model.NewImageRepo(db)
	mirrors := model.NewPayloadMirrorRepo(db)

	// One global mirror, one site-specific.
	_, _ = mirrors.Create(ctx, model.PayloadMirror{
		Name: "global", BaseURL: "https://global.example", Site: "", Priority: 100,
	})
	_, _ = mirrors.Create(ctx, model.PayloadMirror{
		Name: "eu", BaseURL: "https://eu.example", Site: "eu-west", Priority: 10,
	})

	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	iso.StoragePath = "iso/1/files/sources/install.wim"
	_ = isos.Update(ctx, iso)
	swA, _ := software.Create(ctx, model.SoftwarePackage{Name: "A"})
	isoID := iso.ID
	im, _ := images.Create(ctx, model.Image{
		Name: "img", ISOID: &isoID,
		SoftwareLinks: []model.ImageSoftwareLink{{PackageID: swA.ID, OrderValue: 100}},
	})

	h := &ManifestHandler{
		Resolver: resolve.New(images, isos, una).WithDrivers(drivers).WithLoadouts(loadouts),
		Mirrors:  mirrors,
	}

	// eu-west site → eu mirror.
	m, err := h.BuildForSite(ctx, im.ID, "https://primary.example", match.Identity{}, "eu-west")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range m.Items {
		if strings.HasPrefix(it.URL, "https://primary.example") {
			t.Errorf("eu site should not point at primary: %+v", it)
		}
		if !strings.HasPrefix(it.URL, "https://eu.example") {
			t.Errorf("eu site should point at eu mirror: %+v", it)
		}
	}

	// Unknown site → global mirror.
	m, _ = h.BuildForSite(ctx, im.ID, "https://primary.example", match.Identity{}, "atlantis")
	for _, it := range m.Items {
		if !strings.HasPrefix(it.URL, "https://global.example") {
			t.Errorf("unknown site should fall back to global mirror: %+v", it)
		}
	}

	// No mirrors at all → primary.
	_ = mirrors.Delete(ctx, 1)
	_ = mirrors.Delete(ctx, 2)
	m, _ = h.BuildForSite(ctx, im.ID, "https://primary.example", match.Identity{}, "eu-west")
	for _, it := range m.Items {
		if !strings.HasPrefix(it.URL, "https://primary.example") {
			t.Errorf("no mirrors should fall back to primary: %+v", it)
		}
	}
}

func TestUnhealthyMirrorSkipped(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	mirrors := model.NewPayloadMirrorRepo(db)

	_, _ = mirrors.Create(ctx, model.PayloadMirror{
		Name: "eu", BaseURL: "https://eu.example", Site: "eu-west", Priority: 10,
	})
	// Mark unhealthy.
	_ = mirrors.SetHealth(ctx, 1, false)

	_, ok, err := mirrors.PickFor(ctx, "eu-west")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("unhealthy mirror should not be picked")
	}
}
