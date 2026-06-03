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
	// A server-minted agent_id is assigned at creation and is a distinct,
	// non-empty value (NOT the BIOS UUID).
	if a.AgentID == "" || a.AgentID == a.SystemUUID {
		t.Errorf("agent_id not minted distinctly: %+v", a)
	}
	// Second upsert with same UUID returns same record AND keeps the same
	// agent_id (never re-minted).
	b, err := repo.UpsertFromIdentity(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Errorf("expected same id on second upsert, got %d -> %d", a.ID, b.ID)
	}
	if b.AgentID != a.AgentID {
		t.Errorf("agent_id changed across upserts: %q -> %q", a.AgentID, b.AgentID)
	}
	// GetByAgentID resolves the same machine; empty never matches.
	got, err := repo.GetByAgentID(ctx, a.AgentID)
	if err != nil || got.ID != a.ID {
		t.Errorf("GetByAgentID(%q) = %+v, %v", a.AgentID, got, err)
	}
	if _, err := repo.GetByAgentID(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty agent_id should not match, got %v", err)
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
		TargetOU:         "OU=Lab,DC=corp,DC=example",
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

// TestUpsertFromIdentityPreservesMakeModel guards the inventory make/model
// display: the Boot Client reads SMBIOS make/model pre-boot, but the resident
// agent (and other callers) later report an identity carrying only the UUID.
// A UUID-only upsert must NOT wipe the previously captured SMBIOS fields.
func TestUpsertFromIdentityPreservesMakeModel(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))

	// Boot Client captures full SMBIOS identity pre-boot.
	full := match.Identity{
		SystemUUID:         "mk-uuid",
		SystemSerial:       "SN123",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
		BIOSVendor:         "Dell",
		BoardManufacturer:  "Dell",
		BoardProduct:       "0ABCD",
	}
	if _, err := repo.UpsertFromIdentity(ctx, full); err != nil {
		t.Fatal(err)
	}

	// Resident agent checks in with only the UUID populated.
	m, err := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "mk-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if m.SystemManufacturer != "Dell Inc." || m.SystemProduct != "Latitude 5520" {
		t.Errorf("UUID-only upsert wiped make/model: got %q / %q",
			m.SystemManufacturer, m.SystemProduct)
	}
	if m.SystemSerial != "SN123" || m.BoardProduct != "0ABCD" {
		t.Errorf("UUID-only upsert wiped serial/board: got %q / %q",
			m.SystemSerial, m.BoardProduct)
	}

	// A real SMBIOS report with changed (non-empty) values still updates.
	m, err = repo.UpsertFromIdentity(ctx, match.Identity{
		SystemUUID:         "mk-uuid",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5530",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.SystemProduct != "Latitude 5530" {
		t.Errorf("non-empty update did not apply: got %q", m.SystemProduct)
	}
}

func TestUpdateLatestInProgressNote(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "milestone-uuid"})
	depID, err := inv.RecordDeployment(ctx, m.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// While in_progress: the note updates, completed_at stays NULL, outcome
	// stays in_progress.
	if err := inv.UpdateLatestInProgressNote(ctx, m.ID, "reached specialize"); err != nil {
		t.Fatal(err)
	}
	hist, _ := inv.HistoryFor(ctx, m.ID)
	if len(hist) != 1 {
		t.Fatalf("want 1 history row, got %d", len(hist))
	}
	if hist[0].Outcome != "in_progress" || hist[0].CompletedAt != nil || hist[0].Notes != "reached specialize" {
		t.Errorf("after milestone: %+v", hist[0])
	}

	// Agent closes the row ok. A late milestone must NOT resurrect or alter
	// it (race guard).
	if err := inv.CompleteDeployment(ctx, depID, "ok", "done"); err != nil {
		t.Fatal(err)
	}
	if err := inv.UpdateLatestInProgressNote(ctx, m.ID, "late specialize callback"); err != nil {
		t.Fatal(err)
	}
	hist, _ = inv.HistoryFor(ctx, m.ID)
	if hist[0].Outcome != "ok" || hist[0].Notes != "done" || hist[0].CompletedAt == nil {
		t.Errorf("late milestone must not mutate a closed row: %+v", hist[0])
	}
}
