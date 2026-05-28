package resolve

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestLoadoutAdditiveInheritance(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	sw := model.NewSoftwarePackageRepo(db)
	ld := model.NewSoftwareLoadoutRepo(db)
	img := model.NewImageRepo(db)
	iso := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	r := New(img, iso, una).WithLoadouts(ld)

	a, _ := sw.Create(ctx, model.SoftwarePackage{Name: "A"})
	b, _ := sw.Create(ctx, model.SoftwarePackage{Name: "B"})
	c, _ := sw.Create(ctx, model.SoftwarePackage{Name: "C"})

	parent, _ := ld.Create(ctx, model.SoftwareLoadout{
		Name: "parent",
		Packages: []model.SoftwareLoadoutPackage{
			{PackageID: a.ID, OrderValue: 100},
			{PackageID: b.ID, OrderValue: 200},
		},
	})
	parentID := parent.ID
	child, _ := ld.Create(ctx, model.SoftwareLoadout{
		Name:     "child",
		ParentID: &parentID,
		Packages: []model.SoftwareLoadoutPackage{
			// Override A's order, opt out of B, add C.
			{PackageID: a.ID, OrderValue: 50},
			{PackageID: b.ID, OptOut: true},
			{PackageID: c.ID, OrderValue: 300},
		},
	})

	childID := child.ID
	im, _ := img.Create(ctx, model.Image{Name: "img", LoadoutID: &childID})
	got, err := r.Resolve(ctx, im.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Expected: A (order 50, child override), C (order 300). B is opted out.
	if len(got.Software) != 2 {
		t.Fatalf("got %d packages, want 2: %+v", len(got.Software), got.Software)
	}
	if got.Software[0].PackageID != a.ID || got.Software[0].OrderValue != 50 {
		t.Errorf("first = %+v; want A @ 50", got.Software[0])
	}
	if got.Software[1].PackageID != c.ID {
		t.Errorf("second = %+v; want C", got.Software[1])
	}
}

func TestLoadoutDirectLinksTakePrecedence(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	sw := model.NewSoftwarePackageRepo(db)
	ld := model.NewSoftwareLoadoutRepo(db)
	img := model.NewImageRepo(db)
	iso := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	r := New(img, iso, una).WithLoadouts(ld)

	a, _ := sw.Create(ctx, model.SoftwarePackage{Name: "A"})
	b, _ := sw.Create(ctx, model.SoftwarePackage{Name: "B"})

	lo, _ := ld.Create(ctx, model.SoftwareLoadout{
		Name: "lo",
		Packages: []model.SoftwareLoadoutPackage{
			{PackageID: a.ID, OrderValue: 999}, // loadout would order A late
			{PackageID: b.ID, OrderValue: 100},
		},
	})

	loID := lo.ID
	im, _ := img.Create(ctx, model.Image{
		Name:      "img",
		LoadoutID: &loID,
		SoftwareLinks: []model.ImageSoftwareLink{
			{PackageID: a.ID, OrderValue: 1}, // direct link should win
		},
	})

	got, _ := r.Resolve(ctx, im.ID)
	if len(got.Software) != 2 {
		t.Fatalf("got %+v", got.Software)
	}
	// A came from the direct link with order=1 (precedence).
	if got.Software[0].PackageID != a.ID || got.Software[0].OrderValue != 1 {
		t.Errorf("direct link should override loadout ordering: %+v", got.Software[0])
	}
	if got.Software[1].PackageID != b.ID {
		t.Errorf("expected B from loadout, got %+v", got.Software[1])
	}
}

func TestLoadoutCycleDetection(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	ld := model.NewSoftwareLoadoutRepo(db)

	a, _ := ld.Create(ctx, model.SoftwareLoadout{Name: "a"})
	aID := a.ID
	b, _ := ld.Create(ctx, model.SoftwareLoadout{Name: "b", ParentID: &aID})

	// Try to make a the child of b: cycle.
	bID := b.ID
	a.ParentID = &bID
	err := ld.Update(ctx, a)
	if err == nil {
		t.Error("expected cycle error")
	}
}
