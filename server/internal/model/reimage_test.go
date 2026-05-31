package model

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestReimageFlagLifecycle(t *testing.T) {
	ctx := context.Background()
	inv := NewInventoryRepo(openTestDB(t))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "ri-1"})

	// Not flagged initially.
	if p, _, _ := inv.ReimagePending(ctx, "ri-1"); p {
		t.Error("should not be flagged initially")
	}
	// Flag with explicit image.
	if err := inv.SetReimagePending(ctx, m.ID, 7); err != nil {
		t.Fatal(err)
	}
	p, img, _ := inv.ReimagePending(ctx, "ri-1")
	if !p || img != 7 {
		t.Errorf("expected flagged with image 7, got pending=%v img=%d", p, img)
	}
	// Clear (as the boot client's "staging" report does).
	if err := inv.ClearReimagePending(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := inv.ReimagePending(ctx, "ri-1"); p {
		t.Error("flag should be cleared")
	}
	// Unknown UUID is simply not-pending, not an error.
	if p, _, err := inv.ReimagePending(ctx, "nope"); err != nil || p {
		t.Errorf("unknown uuid: pending=%v err=%v", p, err)
	}
}

func TestBulkReimageFlagsTargets(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	bulk := NewBulkRepo(db, inv)
	a, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "rb-a"})
	b, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "rb-b"})

	_, jobs, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:         BulkActionReimage,
		Payload:        "{}",
		Target:         BulkTarget{MachineIDs: []ID{a.ID, b.ID}},
		ReimageImageID: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 reimage jobs, got %d", len(jobs))
	}
	for _, uuid := range []string{"rb-a", "rb-b"} {
		p, img, _ := inv.ReimagePending(ctx, uuid)
		if !p || img != 5 {
			t.Errorf("%s should be flagged with image 5, got pending=%v img=%d", uuid, p, img)
		}
	}
}
