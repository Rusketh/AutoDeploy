package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

func openTestDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestISOCRUD(t *testing.T) {
	ctx := context.Background()
	repo := NewISORepo(openTestDB(t))

	a, err := repo.Create(ctx, ISO{Name: "Win11", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Name != "Win11" || a.OSType != "windows-11" {
		t.Errorf("Create result wrong: %+v", a)
	}

	got, err := repo.Get(ctx, a.ID)
	if err != nil || got.Name != "Win11" {
		t.Fatalf("Get: %+v err=%v", got, err)
	}

	a.Description = "Windows 11 ENT"
	if err := repo.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.Get(ctx, a.ID)
	if got.Description != "Windows 11 ENT" {
		t.Errorf("update didn't stick: %+v", got)
	}

	// Duplicate name -> conflict.
	if _, err := repo.Create(ctx, ISO{Name: "Win11", OSType: "windows-11"}); !errors.Is(err, ErrConflict) {
		t.Errorf("expected conflict, got %v", err)
	}

	// Missing os_type -> validation.
	if _, err := repo.Create(ctx, ISO{Name: "Bad"}); !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error, got %v", err)
	}

	// Delete works when no refs.
	if err := repo.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected not found after delete, got %v", err)
	}
}

func TestNameValidation(t *testing.T) {
	bad := []string{"", "   ", string([]byte{0xff}), "name/with/slashes"}
	for _, b := range bad {
		if err := validateName(b); !errors.Is(err, ErrValidation) {
			t.Errorf("validateName(%q) = %v, want ErrValidation", b, err)
		}
	}
	good := []string{"Win11", "Windows 11", "win11-ent.2026", "a"}
	for _, g := range good {
		if err := validateName(g); err != nil {
			t.Errorf("validateName(%q) = %v, want nil", g, err)
		}
	}
}

func TestUnattendValidatesJSON(t *testing.T) {
	repo := NewUnattendRepo(openTestDB(t))
	_, err := repo.Create(context.Background(), Unattend{
		Name:         "u1",
		SettingsJSON: "not json",
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error, got %v", err)
	}
}

func TestDriverPackageWithFilters(t *testing.T) {
	ctx := context.Background()
	repo := NewDriverPackageRepo(openTestDB(t))

	pkg, err := repo.Create(ctx, DriverPackage{
		Name: "Dell-Latitude-5520",
		Filters: []DriverFilter{
			{FilterJSON: `{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520"}`},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %+v", pkg.Filters)
	}

	// Update replaces filter set wholesale.
	pkg.Filters = []DriverFilter{
		{FilterJSON: `{"system_manufacturer":"Dell Inc."}`},
		{FilterJSON: `{"system_manufacturer":"Dell"}`},
	}
	if err := repo.Update(ctx, pkg); err != nil {
		t.Fatal(err)
	}
	got, _ := repo.Get(ctx, pkg.ID)
	if len(got.Filters) != 2 {
		t.Errorf("expected 2 filters after update, got %d", len(got.Filters))
	}

	// Invalid JSON rejected.
	if _, err := repo.Create(ctx, DriverPackage{
		Name: "bad",
		Filters: []DriverFilter{
			{FilterJSON: "{not json"},
		},
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error, got %v", err)
	}
}

func TestImageCycleDetection(t *testing.T) {
	ctx := context.Background()
	repo := NewImageRepo(openTestDB(t))

	a, err := repo.Create(ctx, Image{Name: "root"})
	if err != nil {
		t.Fatal(err)
	}
	bID := a.ID
	b, err := repo.Create(ctx, Image{Name: "child", ParentID: &bID})
	if err != nil {
		t.Fatal(err)
	}
	cParent := b.ID
	c, err := repo.Create(ctx, Image{Name: "grand", ParentID: &cParent})
	if err != nil {
		t.Fatal(err)
	}

	// Try to point the root at the grandchild. This would close a 3-node cycle.
	cID := c.ID
	a.ParentID = &cID
	if err := repo.Update(ctx, a); !errors.Is(err, ErrCycle) {
		t.Errorf("expected cycle, got %v", err)
	}

	// Self-parent is a cycle.
	a.ParentID = &a.ID
	if err := repo.Update(ctx, a); !errors.Is(err, ErrCycle) {
		t.Errorf("expected cycle on self-parent, got %v", err)
	}
}

func TestImageChildCountBlocksDelete(t *testing.T) {
	ctx := context.Background()
	repo := NewImageRepo(openTestDB(t))

	root, _ := repo.Create(ctx, Image{Name: "root"})
	rootID := root.ID
	_, _ = repo.Create(ctx, Image{Name: "child", ParentID: &rootID})

	if err := repo.Delete(ctx, root.ID); !errors.Is(err, ErrInUse) {
		t.Errorf("expected in-use, got %v", err)
	}
}

func TestRefCountBlocksISODelete(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	isoRepo := NewISORepo(db)
	imgRepo := NewImageRepo(db)

	iso, _ := isoRepo.Create(ctx, ISO{Name: "Win11", OSType: "windows-11"})
	isoID := iso.ID
	_, _ = imgRepo.Create(ctx, Image{Name: "img1", ISOID: &isoID})

	if err := isoRepo.Delete(ctx, iso.ID); !errors.Is(err, ErrInUse) {
		t.Errorf("expected ErrInUse, got %v", err)
	}

	n, _ := isoRepo.RefCount(ctx, iso.ID)
	if n != 1 {
		t.Errorf("RefCount = %d, want 1", n)
	}
}
