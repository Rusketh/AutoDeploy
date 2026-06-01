package model

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// TestUpdateObservedIdentity stores the agent-reported name + DN and surfaces
// them on the record read back by Get/List.
func TestUpdateObservedIdentity(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))
	m, err := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "obs-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.UpdateObservedIdentity(ctx, m.AgentID, "LAB-7K2Q",
		"CN=LAB-7K2Q,OU=Lab,DC=corp,DC=example")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReportedName != "LAB-7K2Q" {
		t.Errorf("returned record name = %q", got.ReportedName)
	}
	// Persisted: Get and List both read the columns.
	back, err := repo.Get(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.ReportedName != "LAB-7K2Q" || back.ADDistinguishedName == "" {
		t.Errorf("Get lost observed identity: %+v", back)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ReportedName != "LAB-7K2Q" {
		t.Errorf("List lost observed name: %+v", list)
	}
	// Unknown agent_id is rejected, not silently created.
	if _, err := repo.UpdateObservedIdentity(ctx, "nope", "X", ""); err == nil {
		t.Error("expected error for unknown agent_id")
	}
}

// TestSyncBindingFromObserved covers the rename/move sync rules, including the
// guard that never clobbers a %placeholder% template.
func TestSyncBindingFromObserved(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))
	m, err := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "sync-uuid"})
	if err != nil {
		t.Fatal(err)
	}

	// No binding yet: a report creates one with the observed name + OU.
	if err := repo.SyncBindingFromObserved(ctx, m.ID, "LAB-01", "OU=Lab,DC=corp,DC=example"); err != nil {
		t.Fatal(err)
	}
	b, err := repo.GetBinding(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.MachineName != "LAB-01" || b.TargetOU != "OU=Lab,DC=corp,DC=example" {
		t.Fatalf("create from observed wrong: %+v", b)
	}

	// Manual rename + AD move: both fields follow reality.
	if err := repo.SyncBindingFromObserved(ctx, m.ID, "LAB-02", "OU=Staging,DC=corp,DC=example"); err != nil {
		t.Fatal(err)
	}
	b, _ = repo.GetBinding(ctx, m.ID)
	if b.MachineName != "LAB-02" || b.TargetOU != "OU=Staging,DC=corp,DC=example" {
		t.Errorf("rename/move not applied: %+v", b)
	}

	// Empty observed values are ignored (don't wipe the binding).
	if err := repo.SyncBindingFromObserved(ctx, m.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ = repo.GetBinding(ctx, m.ID)
	if b.MachineName != "LAB-02" || b.TargetOU != "OU=Staging,DC=corp,DC=example" {
		t.Errorf("empty report wiped binding: %+v", b)
	}

	// A %placeholder% template name is never clobbered by an observed name,
	// but the OU still syncs.
	b.MachineName = "PC-%serial(4)%"
	if err := repo.UpsertBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := repo.SyncBindingFromObserved(ctx, m.ID, "PC-3456", "OU=Pinned,DC=corp,DC=example"); err != nil {
		t.Fatal(err)
	}
	b, _ = repo.GetBinding(ctx, m.ID)
	if b.MachineName != "PC-%serial(4)%" {
		t.Errorf("template name was clobbered: %q", b.MachineName)
	}
	if b.TargetOU != "OU=Pinned,DC=corp,DC=example" {
		t.Errorf("OU should still sync alongside a template name: %+v", b)
	}
}
