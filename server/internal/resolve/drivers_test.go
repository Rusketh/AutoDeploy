package resolve

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestResolveForMachineMatchesDriversGlobally(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	unas := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	r := New(imgs, isos, unas).WithDrivers(drivers)

	// Two driver packages with different filters.
	dellPkg, _ := drivers.Create(ctx, model.DriverPackage{
		Name: "Dell-Latitude-5520",
		Filters: []model.DriverFilter{
			{FilterJSON: `{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520"}`},
		},
	})
	hpPkg, _ := drivers.Create(ctx, model.DriverPackage{
		Name: "HP-EliteBook",
		Filters: []model.DriverFilter{
			{FilterJSON: `{"system_manufacturer":"HP"}`},
		},
	})

	img, _ := imgs.Create(ctx, model.Image{Name: "lab"})

	dellID := match.Identity{
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
	}
	got, err := r.ResolveForMachine(ctx, img.ID, dellID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Drivers) != 1 || got.Drivers[0].ID != dellPkg.ID {
		t.Errorf("expected Dell-Latitude-5520 match only, got %+v", got.Drivers)
	}

	hpID := match.Identity{SystemManufacturer: "HP"}
	got, err = r.ResolveForMachine(ctx, img.ID, hpID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Drivers) != 1 || got.Drivers[0].ID != hpPkg.ID {
		t.Errorf("expected HP-EliteBook match only, got %+v", got.Drivers)
	}

	// A machine that matches neither.
	unknown := match.Identity{SystemManufacturer: "Some Other Co"}
	got, err = r.ResolveForMachine(ctx, img.ID, unknown)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Drivers) != 0 {
		t.Errorf("expected no drivers, got %+v", got.Drivers)
	}
	if len(got.Diagnostics) == 0 {
		t.Error("expected diagnostic for no-match")
	}
}

func TestResolveWithoutIdentitySkipsDrivers(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	isos := model.NewISORepo(db)
	unas := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	_, _ = drivers.Create(ctx, model.DriverPackage{
		Name: "p",
		Filters: []model.DriverFilter{
			{FilterJSON: `{"system_manufacturer":"Dell Inc."}`},
		},
	})
	img, _ := imgs.Create(ctx, model.Image{Name: "x"})

	// Plain Resolve must not touch drivers at all.
	r := New(imgs, isos, unas).WithDrivers(drivers)
	got, err := r.Resolve(ctx, img.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Drivers) != 0 {
		t.Errorf("plain Resolve must not match drivers, got %+v", got.Drivers)
	}
}
