package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// testIdentity builds a minimal SMBIOS identity for first-contact tests.
func testIdentity(uuid, serial, product string) match.Identity {
	return match.Identity{SystemUUID: uuid, SystemSerial: serial, SystemProduct: product}
}

// setupImportInventory returns an inventory repo wired to an imported-asset
// repo, plus the asset repo, sharing one in-memory DB.
func setupImportInventory(t *testing.T) (*InventoryRepo, *ImportedAssetRepo) {
	t.Helper()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	assets := NewImportedAssetRepo(db)
	inv.SetAssetImporter(assets)
	return inv, assets
}

func TestFirstContactAppliesImportedAsset(t *testing.T) {
	ctx := context.Background()
	inv, assets := setupImportInventory(t)
	if _, _, err := assets.Upsert(ctx, []ImportedAsset{
		{Serial: "SER1", Model: "Latitude 5520", Name: "SALES-01", OU: "OU=Sales,DC=corp"},
	}); err != nil {
		t.Fatal(err)
	}

	m, err := inv.UpsertFromIdentity(ctx, testIdentity("uuid-1", "ser1", "latitude 5520"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.FirstContact {
		t.Fatal("expected FirstContact on new machine")
	}

	b, err := inv.GetBinding(ctx, m.ID)
	if err != nil {
		t.Fatalf("binding should have been created: %v", err)
	}
	if b.MachineName != "SALES-01" || b.TargetOU != "OU=Sales,DC=corp" {
		t.Errorf("imported name/OU not applied: %+v", b)
	}

	// The asset is now marked matched and no longer matchable.
	if _, err := assets.MatchByIdentity(ctx, "SER1", "Latitude 5520"); !errors.Is(err, ErrNotFound) {
		t.Errorf("asset should be marked matched, got %v", err)
	}
}

func TestFirstContactFillOnlyDoesNotClobber(t *testing.T) {
	ctx := context.Background()
	inv, assets := setupImportInventory(t)

	// Machine already exists with an operator-set name/OU.
	m, _ := inv.UpsertFromIdentity(ctx, testIdentity("uuid-2", "SER2", "OptiPlex"))
	if err := inv.UpsertBinding(ctx, MachineBinding{
		MachineID: m.ID, MachineName: "KEEP-ME", TargetOU: "OU=Keep",
	}); err != nil {
		t.Fatal(err)
	}
	// Import a fill-only asset (Overwrite=false) that matches — but the machine
	// is not first-contact anymore, so nothing changes. This asserts the
	// fill-only intent by re-running the apply helper directly.
	if _, _, err := assets.Upsert(ctx, []ImportedAsset{
		{Serial: "SER2", Model: "OptiPlex", Name: "IMPORT-NAME", OU: "OU=Import", Overwrite: false},
	}); err != nil {
		t.Fatal(err)
	}
	inv.tryApplyImportedAsset(ctx, m, testIdentity("uuid-2", "SER2", "OptiPlex"))

	b, _ := inv.GetBinding(ctx, m.ID)
	if b.MachineName != "KEEP-ME" || b.TargetOU != "OU=Keep" {
		t.Errorf("fill-only mode clobbered operator values: %+v", b)
	}
}

func TestFirstContactOverwriteReplaces(t *testing.T) {
	ctx := context.Background()
	inv, assets := setupImportInventory(t)

	m, _ := inv.UpsertFromIdentity(ctx, testIdentity("uuid-3", "SER3", "ProBook"))
	_ = inv.UpsertBinding(ctx, MachineBinding{MachineID: m.ID, MachineName: "OLD", TargetOU: "OU=Old"})

	if _, _, err := assets.Upsert(ctx, []ImportedAsset{
		{Serial: "SER3", Model: "ProBook", Name: "NEW", OU: "OU=New", Overwrite: true},
	}); err != nil {
		t.Fatal(err)
	}
	inv.tryApplyImportedAsset(ctx, m, testIdentity("uuid-3", "SER3", "ProBook"))

	b, _ := inv.GetBinding(ctx, m.ID)
	if b.MachineName != "NEW" || b.TargetOU != "OU=New" {
		t.Errorf("overwrite mode did not replace: %+v", b)
	}
}

func TestFirstContactNoImportRowIsNoOp(t *testing.T) {
	ctx := context.Background()
	inv, _ := setupImportInventory(t)

	m, err := inv.UpsertFromIdentity(ctx, testIdentity("uuid-4", "SER4", "ModelUnknown"))
	if err != nil {
		t.Fatal(err)
	}
	// No import row -> no binding created.
	if _, err := inv.GetBinding(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected no binding, got %v", err)
	}
}

func TestSerialArrivesLaterTriggersMatch(t *testing.T) {
	ctx := context.Background()
	inv, assets := setupImportInventory(t)
	if _, _, err := assets.Upsert(ctx, []ImportedAsset{
		{Serial: "LATE1", Model: "ModelL", Name: "LATE-PC", OU: "OU=Late"},
	}); err != nil {
		t.Fatal(err)
	}

	// Agent enrolls UUID-first: no serial/model yet -> no match, no binding.
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "uuid-late"})
	if _, err := inv.GetBinding(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected no binding before serial known, got %v", err)
	}

	// Boot Client's SMBIOS report arrives with serial+model -> match fires.
	if _, err := inv.UpsertFromIdentity(ctx, testIdentity("uuid-late", "LATE1", "ModelL")); err != nil {
		t.Fatal(err)
	}
	b, err := inv.GetBinding(ctx, m.ID)
	if err != nil {
		t.Fatalf("binding should exist after serial arrives: %v", err)
	}
	if b.MachineName != "LATE-PC" || b.TargetOU != "OU=Late" {
		t.Errorf("late-serial apply wrong: %+v", b)
	}
}
