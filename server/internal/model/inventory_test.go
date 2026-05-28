package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestDeployTokenIssueAndValidate(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))
	m, err := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tok-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	// Issue.
	tok, err := repo.IssueDeployToken(ctx, m.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 32 {
		t.Errorf("token too short: %d chars", len(tok))
	}
	// Correct token passes.
	ok, err := repo.ValidateDeployToken(ctx, m.ID, tok)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("valid token rejected")
	}
	// Wrong token fails.
	ok, err = repo.ValidateDeployToken(ctx, m.ID, tok+"x")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("wrong token accepted")
	}
	// Empty token fails.
	ok, _ = repo.ValidateDeployToken(ctx, m.ID, "")
	if ok {
		t.Error("empty token accepted")
	}
	// Rotation: new token issued, old token invalidated.
	tok2, err := repo.IssueDeployToken(ctx, m.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if tok2 == tok {
		t.Error("rotation returned the same token")
	}
	ok, _ = repo.ValidateDeployToken(ctx, m.ID, tok)
	if ok {
		t.Error("old token still valid after rotation")
	}
	ok, _ = repo.ValidateDeployToken(ctx, m.ID, tok2)
	if !ok {
		t.Error("new token not valid after rotation")
	}
}

func TestDeployTokenExpiry(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))
	m, _ := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tok-exp"})
	tok, err := repo.IssueDeployToken(ctx, m.ID, -1*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ok, _ := repo.ValidateDeployToken(ctx, m.ID, tok)
	if ok {
		t.Error("expired token accepted")
	}
}

func TestUpsertFromIdentityCreatesThenUpdates(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))

	id := match.Identity{
		SystemUUID:         "uuid-aaaa",
		SystemSerial:       "SN-1",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
	}
	a, err := repo.UpsertFromIdentity(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.SystemUUID != "uuid-aaaa" {
		t.Errorf("create wrong: %+v", a)
	}
	// Second upsert with same UUID returns same record.
	b, err := repo.UpsertFromIdentity(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Errorf("expected same id on second upsert, got %d -> %d", a.ID, b.ID)
	}
	if _, err := repo.UpsertFromIdentity(ctx, match.Identity{}); !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error for empty UUID, got %v", err)
	}
}

func TestBindingAndHistory(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	img := NewImageRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	i, _ := img.Create(ctx, Image{Name: "base"})
	imgID := i.ID
	if err := inv.UpsertBinding(ctx, MachineBinding{
		MachineID: m.ID, ImageID: &imgID, MachineName: "LAB-01",
		TargetOU: "OU=Lab,DC=corp,DC=example",
		GroupMemberships: []string{"Lab-Computers", "All-Workstations"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := inv.GetBinding(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.MachineName != "LAB-01" || len(b.GroupMemberships) != 2 || b.ImageID == nil || *b.ImageID != imgID {
		t.Errorf("binding wrong: %+v", b)
	}

	// History.
	depID, err := inv.RecordDeployment(ctx, m.ID, &imgID)
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.CompleteDeployment(ctx, depID, "ok", "smoke test"); err != nil {
		t.Fatal(err)
	}
	hist, err := inv.HistoryFor(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Outcome != "ok" || hist[0].CompletedAt == nil {
		t.Errorf("history wrong: %+v", hist)
	}
}

func TestDetectedStateUpsert(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	sw := NewSoftwarePackageRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	pkg, _ := sw.Create(ctx, SoftwarePackage{Name: "acme"})

	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: m.ID, SoftwarePackageID: pkg.ID, Detected: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: m.ID, SoftwarePackageID: pkg.ID, Detected: false,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := inv.DetectedStateFor(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Detected {
		t.Errorf("expected single not-detected row, got %+v", got)
	}
}
