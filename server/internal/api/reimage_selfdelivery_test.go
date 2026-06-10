package api

import (
	"context"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestReimageJobDeliveredViaSelf mirrors the machine-page flow: an operator
// queues a re-image bulk operation for one machine, then the resident agent
// polls /api/v1/agent/self. We assert the agent receives a "reimage" job
// (so it knows to reboot) AND that the machine is flagged reimage_pending.
func TestReimageJobDeliveredViaSelf(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	bulk := model.NewBulkRepo(db, inv)

	iso, _ := isos.Create(ctx, model.ISO{Name: "W11", OSType: "windows-11"})
	isoID := iso.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID})

	// Register a machine with an agent_id + binding, as a deployed box has.
	// UpsertFromIdentity mints the server-side agent_id.
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u-1"})
	if m.AgentID == "" {
		t.Fatal("expected a minted agent_id")
	}
	_ = inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID})

	// Operator queues a re-image for this one machine (machine-page action).
	if _, _, err := bulk.CreateOperation(ctx, model.BulkOperation{
		Action:  model.BulkActionReimage,
		Payload: "{}",
		Target:  model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	}); err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}

	// Flag must be set so the boot client auto-deploys after reboot.
	pending, _, _ := inv.ReimagePending(ctx, "u-1")
	if !pending {
		t.Error("reimage_pending should be set after queuing re-image")
	}

	// The agent polls /self: claim its jobs the same way handleAgentSelf does.
	jobs, err := bulk.ClaimJobsFor(ctx, m.ID, 8)
	if err != nil {
		t.Fatalf("ClaimJobsFor: %v", err)
	}
	var gotReimage bool
	for _, j := range jobs {
		if j.Action == model.BulkActionReimage {
			gotReimage = true
		}
	}
	if !gotReimage {
		t.Errorf("agent did not receive a reimage job via self; jobs=%+v", jobs)
	}
}

// TestCancelPendingReimage covers the operator "cancel re-image" path: clearing
// the flag and neutralising the queued job so the agent won't reboot and the
// boot client won't auto-deploy.
func TestCancelPendingReimage(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	inv := model.NewInventoryRepo(db)
	bulk := model.NewBulkRepo(db, inv)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u-cancel"})
	if _, _, err := bulk.CreateOperation(ctx, model.BulkOperation{
		Action:  model.BulkActionReimage,
		Payload: "{}",
		Target:  model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	}); err != nil {
		t.Fatalf("CreateOperation: %v", err)
	}

	// Precondition: flagged + a queued reimage job is visible.
	if pending, _, _ := inv.ReimagePendingByID(ctx, m.ID); !pending {
		t.Fatal("expected reimage pending before cancel")
	}
	job, _ := bulk.LatestReimageJob(ctx, m.ID)
	if job == nil || job.Status != "queued" {
		t.Fatalf("expected a queued reimage job, got %+v", job)
	}

	// Cancel: clear the flag and neutralise the job.
	if err := inv.ClearReimagePending(ctx, m.ID); err != nil {
		t.Fatalf("ClearReimagePending: %v", err)
	}
	n, err := bulk.CancelReimageJobsForMachine(ctx, m.ID)
	if err != nil || n != 1 {
		t.Fatalf("CancelReimageJobsForMachine = %d, %v; want 1, nil", n, err)
	}

	// Postcondition: not pending, and the agent's next /self poll gets no
	// reimage job (the cancelled one is no longer claimable).
	if pending, _, _ := inv.ReimagePendingByID(ctx, m.ID); pending {
		t.Error("machine should not be pending after cancel")
	}
	claimed, _ := bulk.ClaimJobsFor(ctx, m.ID, 8)
	for _, j := range claimed {
		if j.Action == model.BulkActionReimage {
			t.Errorf("reimage job should not be claimable after cancel; got %+v", j)
		}
	}
}
