package model

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestUpdateADJoinStatusTransitions(t *testing.T) {
	ctx := context.Background()
	inv := NewInventoryRepo(openTestDB(t))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "adj-1"})
	agentID := m.AgentID

	// First failure fires a one-shot notify.
	upd, err := inv.UpdateADJoinStatus(ctx, agentID, ADJoinFailed, "Add-Computer failed: access denied")
	if err != nil {
		t.Fatal(err)
	}
	if upd.NotifyEvent != ADJoinFailed {
		t.Fatalf("first join_failed NotifyEvent = %q, want %q", upd.NotifyEvent, ADJoinFailed)
	}
	if upd.Machine.ADJoinStatus != ADJoinFailed || upd.Machine.ADJoinDetail == "" {
		t.Errorf("status/detail not stored: %+v", upd.Machine)
	}

	// Same failure again is deduped (no repeat alert).
	upd, _ = inv.UpdateADJoinStatus(ctx, agentID, ADJoinFailed, "still failing")
	if upd.NotifyEvent != "" {
		t.Errorf("repeat join_failed should not re-notify, got %q", upd.NotifyEvent)
	}

	// Recovery to joined clears the dedup marker...
	upd, _ = inv.UpdateADJoinStatus(ctx, agentID, ADJoinJoined, "")
	if upd.NotifyEvent != "" {
		t.Errorf("joined should not notify, got %q", upd.NotifyEvent)
	}
	// ...so the next failure re-arms and alerts again.
	upd, _ = inv.UpdateADJoinStatus(ctx, agentID, ADJoinFailed, "failed again")
	if upd.NotifyEvent != ADJoinFailed {
		t.Errorf("failure after recovery should re-notify, got %q", upd.NotifyEvent)
	}

	// Read-back reflects the latest status.
	got, _ := inv.Get(ctx, m.ID)
	if got.ADJoinStatus != ADJoinFailed || got.ADJoinReportedAt == nil {
		t.Errorf("Get after update = %+v, want join_failed with reported_at set", got)
	}
}

func TestUpdateADJoinStatusTrustIndependent(t *testing.T) {
	ctx := context.Background()
	inv := NewInventoryRepo(openTestDB(t))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "adj-2"})

	// join_failed and trust_broken use independent dedup markers, so a machine
	// that flips from one failure kind to the other alerts for each.
	upd, _ := inv.UpdateADJoinStatus(ctx, m.AgentID, ADJoinFailed, "")
	if upd.NotifyEvent != ADJoinFailed {
		t.Fatalf("want join_failed notify, got %q", upd.NotifyEvent)
	}
	upd, _ = inv.UpdateADJoinStatus(ctx, m.AgentID, ADTrustBroken, "secure channel down")
	if upd.NotifyEvent != ADTrustBroken {
		t.Fatalf("want trust_broken notify, got %q", upd.NotifyEvent)
	}
	// Repeat trust_broken deduped.
	upd, _ = inv.UpdateADJoinStatus(ctx, m.AgentID, ADTrustBroken, "still down")
	if upd.NotifyEvent != "" {
		t.Errorf("repeat trust_broken should not re-notify, got %q", upd.NotifyEvent)
	}
}

func TestUpdateADJoinStatusUnknownCoerced(t *testing.T) {
	ctx := context.Background()
	inv := NewInventoryRepo(openTestDB(t))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "adj-3"})

	// An unrecognised token is coerced to "" (unknown) and never notifies.
	upd, err := inv.UpdateADJoinStatus(ctx, m.AgentID, "banana", "")
	if err != nil {
		t.Fatal(err)
	}
	if upd.NotifyEvent != "" || upd.Machine.ADJoinStatus != "" {
		t.Errorf("unknown status should coerce to empty and not notify: %+v", upd)
	}
}
