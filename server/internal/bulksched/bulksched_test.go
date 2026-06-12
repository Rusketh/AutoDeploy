package bulksched

import (
	"context"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

type fakeWaker struct {
	woken []model.ID
}

func (f *fakeWaker) WakeForJobs(_ context.Context, jobs []model.BulkJob) int {
	for _, j := range jobs {
		f.woken = append(f.woken, j.MachineID)
	}
	return len(jobs)
}

func openRepos(t *testing.T) (*model.InventoryRepo, *model.BulkRepo) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	return inv, model.NewBulkRepo(db, inv)
}

// TestSchedulerWakesDueOperations: a due one-time op with wake_on_lan set
// materialises its run AND wakes the targeted machines; one without the
// option does not wake anything.
func TestSchedulerWakesDueOperations(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openRepos(t)
	rec, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Minute)
	withWake, _, err := bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionScript, Payload: `{"shell":"cmd","body":"x"}`,
		Target:       model.BulkTarget{MachineIDs: []model.ID{rec.ID}},
		ScheduleKind: model.BulkScheduleOnce, RunAt: &past,
		WakeOnLAN: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	past2 := time.Now().Add(-time.Minute)
	if _, _, err := bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionScript, Payload: `{"shell":"cmd","body":"x"}`,
		Target:       model.BulkTarget{MachineIDs: []model.ID{rec.ID}},
		ScheduleKind: model.BulkScheduleOnce, RunAt: &past2,
	}); err != nil {
		t.Fatal(err)
	}

	fw := &fakeWaker{}
	s := &Scheduler{Bulk: bulk, Waker: fw}
	s.runOnce(ctx)

	if len(fw.woken) != 1 || fw.woken[0] != rec.ID {
		t.Errorf("expected exactly the wake_on_lan op to wake machine %d, got %v", rec.ID, fw.woken)
	}
	op, jobs, _ := bulk.GetOperation(ctx, withWake.ID)
	if len(jobs) != 1 {
		t.Errorf("due op should have materialised 1 job, got %d", len(jobs))
	}
	if op.NextRunAt != nil {
		t.Error("one-time op should clear next_run_at after firing")
	}
}
