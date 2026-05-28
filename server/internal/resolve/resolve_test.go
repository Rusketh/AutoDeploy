package resolve

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func setup(t *testing.T) (*model.ISORepo, *model.UnattendRepo, *model.SoftwarePackageRepo, *model.ImageRepo, *Resolver) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	images := model.NewImageRepo(db)
	return isos, unattend, software, images, New(images, isos, unattend)
}

func TestNearestWinsISOAndUnattend(t *testing.T) {
	ctx := context.Background()
	isos, unas, _, images, r := setup(t)

	rootISO, _ := isos.Create(ctx, model.ISO{Name: "Win10", OSType: "windows-10"})
	childISO, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	rootUA, _ := unas.Create(ctx, model.Unattend{Name: "base-ua"})
	childUA, _ := unas.Create(ctx, model.Unattend{Name: "lab-ua"})

	rootISOID := rootISO.ID
	rootUAID := rootUA.ID
	root, _ := images.Create(ctx, model.Image{
		Name: "root", ISOID: &rootISOID, UnattendID: &rootUAID,
	})

	// Child overrides both. Nearest-wins must pick child's.
	rootImgID := root.ID
	childISOID := childISO.ID
	childUAID := childUA.ID
	child, _ := images.Create(ctx, model.Image{
		Name: "child", ParentID: &rootImgID,
		ISOID: &childISOID, UnattendID: &childUAID,
	})

	got, err := r.Resolve(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO == nil || got.ISO.Name != "Win11" {
		t.Errorf("ISO = %+v, want Win11", got.ISO)
	}
	if got.Unattend == nil || got.Unattend.Name != "lab-ua" {
		t.Errorf("Unattend = %+v, want lab-ua", got.Unattend)
	}
	if len(got.ChainNames) != 2 || got.ChainNames[0] != "child" || got.ChainNames[1] != "root" {
		t.Errorf("ChainNames = %v", got.ChainNames)
	}

	// A grandchild that links nothing should resolve to the child's ISO and unattend.
	childID := child.ID
	grand, _ := images.Create(ctx, model.Image{Name: "grand", ParentID: &childID})
	got, err = r.Resolve(ctx, grand.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO == nil || got.ISO.Name != "Win11" {
		t.Errorf("nearest-wins broken: got %+v", got.ISO)
	}
}

func TestResolveMissingISOReportsDiagnostic(t *testing.T) {
	ctx := context.Background()
	_, _, _, images, r := setup(t)
	img, _ := images.Create(ctx, model.Image{Name: "lonely"})
	got, err := r.Resolve(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ISO != nil || got.Unattend != nil {
		t.Errorf("expected nil ISO and unattend, got %+v", got)
	}
	if len(got.Diagnostics) != 2 {
		t.Errorf("expected 2 diagnostics, got %v", got.Diagnostics)
	}
}

func TestResolveSoftwareUnionDeduplicates(t *testing.T) {
	ctx := context.Background()
	_, _, software, images, r := setup(t)

	a, _ := software.Create(ctx, model.SoftwarePackage{Name: "A"})
	b, _ := software.Create(ctx, model.SoftwarePackage{Name: "B"})
	c, _ := software.Create(ctx, model.SoftwarePackage{Name: "C"})

	// root links A (order 100) and B (order 200).
	root, _ := images.Create(ctx, model.Image{
		Name: "root",
		SoftwareLinks: []model.ImageSoftwareLink{
			{PackageID: a.ID, OrderValue: 100},
			{PackageID: b.ID, OrderValue: 200},
		},
	})
	rootID := root.ID

	// child links A again (order 50 — should be ignored, root wins) and C.
	_, _ = images.Create(ctx, model.Image{
		Name:     "child",
		ParentID: &rootID,
		SoftwareLinks: []model.ImageSoftwareLink{
			{PackageID: a.ID, OrderValue: 50},
			{PackageID: c.ID, OrderValue: 300},
		},
	})

	// Resolve from child.
	childImg, _ := images.List(ctx)
	var childID model.ID
	for _, im := range childImg {
		if im.Name == "child" {
			childID = im.ID
		}
	}
	got, err := r.Resolve(ctx, childID)
	if err != nil {
		t.Fatal(err)
	}
	// We expect A, C from the child, then B from root, since the child is
	// walked first and dedupe is by first-seen.
	if len(got.Software) != 3 {
		t.Fatalf("expected 3 software entries, got %d: %+v", len(got.Software), got.Software)
	}
	// First two are from child (A, C). Order within an image is by stored
	// order; within the chain it's child-first.
	if got.Software[0].PackageID != a.ID {
		t.Errorf("expected first = A, got %+v", got.Software[0])
	}
	if got.Software[1].PackageID != c.ID {
		t.Errorf("expected second = C, got %+v", got.Software[1])
	}
	if got.Software[2].PackageID != b.ID {
		t.Errorf("expected third = B, got %+v", got.Software[2])
	}
}
