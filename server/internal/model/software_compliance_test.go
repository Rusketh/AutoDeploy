package model

import (
	"context"
	"testing"
)

// AllComplianceSummaries must count a machine as a target of a package that
// its image's loadout inherits from a PARENT loadout (the resolver installs
// it, so compliance must see it). A descendant opt-out must remove it again.
func TestComplianceCountsParentLoadoutPackages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pkgRepo := NewSoftwarePackageRepo(db)
	loRepo := NewSoftwareLoadoutRepo(db)
	imgRepo := NewImageRepo(db)

	base, _ := pkgRepo.Create(ctx, SoftwarePackage{Name: "base-app"})
	extra, _ := pkgRepo.Create(ctx, SoftwarePackage{Name: "extra-app"})
	optedOut, _ := pkgRepo.Create(ctx, SoftwarePackage{Name: "opted-out-app"})

	// Parent loadout carries base-app and opted-out-app; child adds extra-app
	// and opts out of opted-out-app.
	parent, err := loRepo.Create(ctx, SoftwareLoadout{
		Name: "parent",
		Packages: []SoftwareLoadoutPackage{
			{PackageID: base.ID},
			{PackageID: optedOut.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	parentID := parent.ID
	child, err := loRepo.Create(ctx, SoftwareLoadout{
		Name:     "child",
		ParentID: &parentID,
		Packages: []SoftwareLoadoutPackage{
			{PackageID: extra.ID},
			{PackageID: optedOut.ID, OptOut: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// An image using the CHILD loadout, and a machine bound to it.
	childLoadout := child.ID
	img, err := imgRepo.Create(ctx, Image{Name: "lab", LoadoutID: &childLoadout})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO machine_record (system_uuid) VALUES ('uuid-1')`); err != nil {
		t.Fatal(err)
	}
	var machID ID
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM machine_record WHERE system_uuid='uuid-1'`).Scan(&machID); err != nil {
		t.Fatal(err)
	}
	inv := NewInventoryRepo(db)
	if err := inv.UpsertBinding(ctx, MachineBinding{MachineID: machID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	// The machine reports base-app installed, extra-app missing.
	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: machID, SoftwarePackageID: base.ID, Detected: true}); err != nil {
		t.Fatal(err)
	}
	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: machID, SoftwarePackageID: extra.ID, Detected: false}); err != nil {
		t.Fatal(err)
	}

	sum, err := pkgRepo.AllComplianceSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// base-app is inherited from the PARENT loadout: 1 target, installed.
	if c := sum[base.ID]; c.TargetCount != 1 || c.InstalledCount != 1 {
		t.Errorf("base-app (inherited from parent): %+v, want Target=1 Installed=1", c)
	}
	// extra-app comes from the child loadout directly: 1 target, missing.
	if c := sum[extra.ID]; c.TargetCount != 1 || c.MissingCount != 1 {
		t.Errorf("extra-app (child loadout): %+v, want Target=1 Missing=1", c)
	}
	// opted-out-app: parent includes it, child opts out -> NOT a target.
	if c := sum[optedOut.ID]; c.TargetCount != 0 {
		t.Errorf("opted-out-app (child opt-out): %+v, want Target=0", c)
	}
}

// A package contributed by a grandparent loadout (parent-of-a-parent) must
// still count -- the resolver walks the whole ancestry, so compliance has to
// as well. This is the OneDrive case: package two loadout levels up.
func TestComplianceCountsGrandparentLoadoutPackages(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	pkgRepo := NewSoftwarePackageRepo(db)
	loRepo := NewSoftwareLoadoutRepo(db)
	imgRepo := NewImageRepo(db)

	onedrive, _ := pkgRepo.Create(ctx, SoftwarePackage{Name: "onedrive"})

	grand, err := loRepo.Create(ctx, SoftwareLoadout{
		Name:     "grandparent",
		Packages: []SoftwareLoadoutPackage{{PackageID: onedrive.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	grandID := grand.ID
	mid, err := loRepo.Create(ctx, SoftwareLoadout{Name: "middle", ParentID: &grandID})
	if err != nil {
		t.Fatal(err)
	}
	midID := mid.ID
	leaf, err := loRepo.Create(ctx, SoftwareLoadout{Name: "leaf", ParentID: &midID})
	if err != nil {
		t.Fatal(err)
	}

	leafID := leaf.ID
	img, err := imgRepo.Create(ctx, Image{Name: "lab", LoadoutID: &leafID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO machine_record (system_uuid) VALUES ('uuid-gp')`); err != nil {
		t.Fatal(err)
	}
	var machID ID
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM machine_record WHERE system_uuid='uuid-gp'`).Scan(&machID); err != nil {
		t.Fatal(err)
	}
	inv := NewInventoryRepo(db)
	if err := inv.UpsertBinding(ctx, MachineBinding{MachineID: machID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: machID, SoftwarePackageID: onedrive.ID, Detected: true}); err != nil {
		t.Fatal(err)
	}

	sum, err := pkgRepo.AllComplianceSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c := sum[onedrive.ID]; c.TargetCount != 1 || c.InstalledCount != 1 {
		t.Errorf("onedrive (grandparent loadout): %+v, want Target=1 Installed=1", c)
	}
}
