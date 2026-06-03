package model

import (
	"context"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
)

// seedMachine creates a machine with a name binding and returns its ID.
func seedMachine(t *testing.T, inv *InventoryRepo, uuid, name, ou string) ID {
	t.Helper()
	ctx := context.Background()
	rec, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: uuid})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpsertBinding(ctx, MachineBinding{MachineID: rec.ID, MachineName: name, TargetOU: ou}); err != nil {
		t.Fatal(err)
	}
	return rec.ID
}

func TestScheduledOnceDoesNotQueueUntilRun(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	id := seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")

	at := time.Now().Add(time.Hour)
	op, jobs, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:       BulkActionScript,
		Payload:      `{"shell":"cmd","body":"echo hi"}`,
		Target:       BulkTarget{MachineIDs: []ID{id}},
		ScheduleKind: BulkScheduleOnce,
		RunAt:        &at,
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != BulkStatusScheduled {
		t.Errorf("status = %q, want scheduled", op.Status)
	}
	if len(jobs) != 0 {
		t.Errorf("scheduled op should queue no jobs yet, got %d", len(jobs))
	}
	if op.NextRunAt == nil {
		t.Fatal("scheduled op should have a next_run_at")
	}

	// Not due yet.
	if due, _ := bulk.ListDueOperations(ctx, time.Now()); len(due) != 0 {
		t.Errorf("op should not be due yet, got %d", len(due))
	}
	// Due in the future.
	due, _ := bulk.ListDueOperations(ctx, at.Add(time.Minute))
	if len(due) != 1 {
		t.Fatalf("op should be due, got %d", len(due))
	}

	got, err := bulk.RunOperationNow(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 job after run, got %d", len(got))
	}
	if err := bulk.AdvanceSchedule(ctx, op.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _, _ := bulk.GetOperation(ctx, op.ID)
	if after.NextRunAt != nil {
		t.Errorf("one-time op should clear next_run_at after running")
	}
	if after.Status != BulkStatusActive {
		t.Errorf("status = %q, want active", after.Status)
	}
}

func TestRecurringFilterReResolves(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")

	op, _, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:       BulkActionScript,
		Payload:      `{"shell":"cmd","body":"echo hi"}`,
		Target:       BulkTarget{NameRegex: "^LAB-"},
		TargetMode:   BulkTargetFilter,
		ScheduleKind: BulkScheduleRecurring,
		RecurSpec:    `{"every":1,"unit":"day","at":"02:00"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// First run: one machine.
	if jobs, _ := bulk.RunOperationNow(ctx, op.ID); len(jobs) != 1 {
		t.Fatalf("first run expected 1 job, got %d", len(jobs))
	}
	// A new matching machine appears.
	seedMachine(t, inv, "u2", "LAB-02", "OU=Lab")
	jobs, err := bulk.RunOperationNow(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Second run re-resolves the filter: both machines in the latest run.
	latest := 0
	for _, j := range jobs {
		if j.RunNo > latest {
			latest = j.RunNo
		}
	}
	count := 0
	for _, j := range jobs {
		if j.RunNo == latest {
			count++
		}
	}
	if count != 2 {
		t.Errorf("re-resolved recurring run expected 2 jobs, got %d", count)
	}
}

func TestRecurringSelectionStaysFrozen(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	id1 := seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")

	op, _, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:       BulkActionScript,
		Payload:      `{"shell":"cmd","body":"echo hi"}`,
		Target:       BulkTarget{MachineIDs: []ID{id1}},
		TargetMode:   BulkTargetSelection,
		ScheduleKind: BulkScheduleRecurring,
		RecurSpec:    `{"every":1,"unit":"hour"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	bulk.RunOperationNow(ctx, op.ID)
	seedMachine(t, inv, "u2", "LAB-02", "OU=Lab") // should NOT be picked up
	jobs, _ := bulk.RunOperationNow(ctx, op.ID)
	latest := 0
	for _, j := range jobs {
		if j.RunNo > latest {
			latest = j.RunNo
		}
	}
	count := 0
	for _, j := range jobs {
		if j.RunNo == latest {
			count++
		}
	}
	if count != 1 {
		t.Errorf("frozen selection run expected 1 job, got %d", count)
	}
}

func TestCancelStopsQueuedJobs(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	id := seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")

	op, jobs, err := bulk.CreateOperation(ctx, BulkOperation{
		Action:  BulkActionScript,
		Payload: `{"shell":"cmd","body":"echo hi"}`,
		Target:  BulkTarget{MachineIDs: []ID{id}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if err := bulk.CancelOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	after, _, _ := bulk.GetOperation(ctx, op.ID)
	if after.Status != BulkStatusCancelled {
		t.Errorf("status = %q, want cancelled", after.Status)
	}
	// Agent should claim nothing from a cancelled op.
	claimed, _ := bulk.ClaimJobsFor(ctx, id, 10)
	if len(claimed) != 0 {
		t.Errorf("cancelled op jobs should not be claimable, got %d", len(claimed))
	}
	prog, _ := bulk.ProgressFor(ctx, op.ID)
	if prog.Cancelled != 1 {
		t.Errorf("expected 1 cancelled job, got %+v", prog)
	}
}

func TestClearCompletedRemovesTerminalOps(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	id := seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")

	// One op that we cancel (terminal), one scheduled (kept).
	cancelled, _, _ := bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionScript, Payload: `{"shell":"cmd","body":"x"}`,
		Target: BulkTarget{MachineIDs: []ID{id}},
	})
	_ = bulk.CancelOperation(ctx, cancelled.ID)

	at := time.Now().Add(time.Hour)
	kept, _, _ := bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionScript, Payload: `{"shell":"cmd","body":"x"}`,
		Target: BulkTarget{MachineIDs: []ID{id}}, ScheduleKind: BulkScheduleOnce, RunAt: &at,
	})

	n, err := bulk.ClearCompleted(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected to clear 1 op, cleared %d", n)
	}
	ops, _ := bulk.ListOperations(ctx)
	if len(ops) != 1 || ops[0].ID != kept.ID {
		t.Errorf("scheduled op should remain, got %+v", ops)
	}
}

func TestCompleteJobMarksOperationCompleted(t *testing.T) {
	ctx := context.Background()
	inv, bulk := openBulk(t)
	id := seedMachine(t, inv, "u1", "LAB-01", "OU=Lab")
	op, jobs, _ := bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionScript, Payload: `{"shell":"cmd","body":"x"}`,
		Target: BulkTarget{MachineIDs: []ID{id}},
	})
	if err := bulk.CompleteJob(ctx, jobs[0].ID, "ok", `{}`); err != nil {
		t.Fatal(err)
	}
	after, _, _ := bulk.GetOperation(ctx, op.ID)
	if after.Status != BulkStatusCompleted {
		t.Errorf("status = %q, want completed", after.Status)
	}
}

func TestNextRunPresets(t *testing.T) {
	base := time.Date(2026, 6, 3, 10, 0, 0, 0, time.Local) // a Wednesday

	got, err := nextRun(base, RecurSpec{Every: 2, Unit: "hour"})
	if err != nil || !got.Equal(base.Add(2*time.Hour)) {
		t.Errorf("hourly: got %v err %v", got, err)
	}

	got, _ = nextRun(base, RecurSpec{Every: 1, Unit: "day", At: "02:00"})
	want := time.Date(2026, 6, 4, 2, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("daily: got %v want %v", got, want)
	}

	// Weekly on Monday (weekday 1) from a Wednesday -> next Monday.
	got, _ = nextRun(base, RecurSpec{Every: 1, Unit: "week", At: "02:00", Weekday: 1})
	if got.Weekday() != time.Monday || !got.After(base) {
		t.Errorf("weekly: got %v (weekday %v)", got, got.Weekday())
	}
}

func TestCronNext(t *testing.T) {
	base := time.Date(2026, 6, 3, 10, 30, 0, 0, time.Local)
	// 0 2 * * 1 -> next Monday 02:00.
	got, err := cronNext(base, "0 2 * * 1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Weekday() != time.Monday || got.Hour() != 2 || got.Minute() != 0 {
		t.Errorf("cron next = %v, want a Monday 02:00", got)
	}
	if _, err := parseCron("bogus"); err == nil {
		t.Error("expected parse error for bad cron")
	}
}
