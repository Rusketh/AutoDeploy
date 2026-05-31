package model

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func openBulk(t *testing.T) (*InventoryRepo, *BulkRepo) {
	t.Helper()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	return inv, NewBulkRepo(db, inv)
}

func TestBulkPreviewAndCreate(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)

	// Set up three machines with different bindings.
	for _, m := range []struct {
		uuid, name, ou string
		groups         []string
	}{
		{"u1", "LAB-01", "OU=Lab,DC=corp,DC=example", []string{"Lab-Computers"}},
		{"u2", "LAB-02", "OU=Lab,DC=corp,DC=example", []string{"Lab-Computers"}},
		{"u3", "FIN-01", "OU=Finance,DC=corp,DC=example", []string{"Finance-Computers"}},
	} {
		rec, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: m.uuid})
		_ = inv.UpsertBinding(ctx, MachineBinding{
			MachineID: rec.ID, MachineName: m.name, TargetOU: m.ou,
			GroupMemberships: m.groups,
		})
	}

	// Preview by OU.
	hits, err := bulk.PreviewTargets(ctx, BulkTarget{OU: "OU=Lab,DC=corp,DC=example"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Errorf("expected 2 lab machines, got %d", len(hits))
	}

	// Preview by name regex.
	hits, _ = bulk.PreviewTargets(ctx, BulkTarget{NameRegex: `^FIN-`})
	if len(hits) != 1 || hits[0].SystemUUID != "u3" {
		t.Errorf("expected FIN-01 only, got %+v", hits)
	}

	// Explicit machine-id selection (the new "selection basket" path)
	// targets exactly those machines, regardless of name/OU/group.
	fin, _ := inv.GetByUUID(ctx, "u3")
	lab1, _ := inv.GetByUUID(ctx, "u1")
	sel, _ := bulk.PreviewTargets(ctx, BulkTarget{MachineIDs: []ID{fin.ID, lab1.ID}})
	if len(sel) != 2 {
		t.Errorf("expected 2 explicitly-selected machines, got %d", len(sel))
	}
	got := map[ID]bool{}
	for _, m := range sel {
		got[m.ID] = true
	}
	if !got[fin.ID] || !got[lab1.ID] {
		t.Errorf("selection should contain exactly the chosen machines, got %+v", sel)
	}

	// Create script operation targeting lab machines.
	_, jobs, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:  BulkActionScript,
		Payload: `{"shell":"cmd","body":"echo hello"}`,
		Target:  BulkTarget{OU: "OU=Lab,DC=corp,DC=example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs queued, got %d", len(jobs))
	}

	// Lab-01 claims its jobs.
	rec1, _ := inv.GetByUUID(ctx, "u1")
	claimed, _ := bulk.ClaimJobsFor(ctx, rec1.ID, 10)
	if len(claimed) != 1 {
		t.Errorf("expected 1 claimed job for lab-01, got %d", len(claimed))
	}
	if claimed[0].Status != "running" {
		t.Errorf("expected status=running, got %q", claimed[0].Status)
	}
	// Second claim returns nothing.
	again, _ := bulk.ClaimJobsFor(ctx, rec1.ID, 10)
	if len(again) != 0 {
		t.Errorf("second claim should be empty, got %d", len(again))
	}

	// Complete.
	if err := bulk.CompleteJob(ctx, claimed[0].ID, "ok", `{"exit_code":0}`); err != nil {
		t.Fatal(err)
	}
}

func TestBulkRejectsInvalidAction(t *testing.T) {
	_, bulk := openBulk(t)
	_, _, err := bulk.CreateOperation(context.Background(), BulkOperation{
		Action:  "bogus",
		Payload: "{}",
	})
	if err == nil {
		t.Error("expected error for invalid action")
	}
}
