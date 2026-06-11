package model

import (
	"context"
	"errors"
	"testing"
)

// newUpdateTestEnv creates a machine (with optional OS caption) and returns
// the repos shared by the Windows Update tests.
func newUpdateTestEnv(t *testing.T) (*WindowsUpdateRepo, *InventoryRepo, func(uuid, caption string) ID) {
	t.Helper()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	repo := NewWindowsUpdateRepo(db, inv)
	ctx := context.Background()
	addMachine := func(uuid, caption string) ID {
		t.Helper()
		if _, err := db.ExecContext(ctx,
			`INSERT INTO machine_record (system_uuid) VALUES (?)`, uuid); err != nil {
			t.Fatal(err)
		}
		var id ID
		if err := db.QueryRowContext(ctx,
			`SELECT id FROM machine_record WHERE system_uuid=?`, uuid).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if caption != "" {
			if err := inv.UpdateHardware(ctx, id, Hardware{OSCaption: caption}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	return repo, inv, addMachine
}

func TestCreateDeploymentMarksPending(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	m1 := addMachine("uuid-1", "")
	m2 := addMachine("uuid-2", "")

	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB5034441", Title: "CU"})
	if err != nil {
		t.Fatal(err)
	}
	// m2 already reports the KB installed; a deployment must not downgrade it.
	if err := repo.UpsertMachineStatuses(ctx, m2, []MachineUpdateStatus{
		{MachineID: m2, KBNumber: "KB5034441", Status: "installed"},
	}); err != nil {
		t.Fatal(err)
	}

	_, jobs, err := repo.CreateDeployment(ctx, UpdateDeployment{
		UpdateIDs: []ID{u.ID},
		Target:    BulkTarget{MachineIDs: []ID{m1, m2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	statusOf := func(machine ID) string {
		t.Helper()
		statuses, err := repo.ListMachineStatuses(ctx, machine)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range statuses {
			if s.KBNumber == "KB5034441" {
				return s.Status
			}
		}
		return ""
	}
	if got := statusOf(m1); got != "pending" {
		t.Errorf("m1 status = %q, want pending", got)
	}
	if got := statusOf(m2); got != "installed" {
		t.Errorf("m2 status = %q, want installed (never downgraded)", got)
	}
}

func TestCreateDeploymentValidatesUpdateIDs(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	m := addMachine("uuid-1", "")
	_, _, err := repo.CreateDeployment(ctx, UpdateDeployment{
		UpdateIDs: []ID{999},
		Target:    BulkTarget{MachineIDs: []ID{m}},
	})
	if !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error for unknown update id, got %v", err)
	}
}

func TestGetUpdateJob(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	m := addMachine("uuid-1", "")
	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB1", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	_, jobs, err := repo.CreateDeployment(ctx, UpdateDeployment{
		UpdateIDs: []ID{u.ID},
		Target:    BulkTarget{MachineIDs: []ID{m}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetUpdateJob(ctx, jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.MachineID != m || got.UpdateID != u.ID || got.Status != "queued" {
		t.Errorf("GetUpdateJob = %+v", got)
	}
	if _, err := repo.GetUpdateJob(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetByKB(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := newUpdateTestEnv(t)
	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB5034441", Title: "CU"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetByKB(ctx, "KB5034441")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetByKB = %+v err=%v", got, err)
	}
	if _, err := repo.GetByKB(ctx, "KB0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// A job that sat in 'running' too long (agent crashed, rebooted, or is
// too old to act on update jobs) must be requeued and claimable again;
// a freshly claimed job must not be double-claimed.
func TestClaimUpdateJobsRequeuesStaleRunning(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	m := addMachine("uuid-1", "")
	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB1", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateDeployment(ctx, UpdateDeployment{
		UpdateIDs: []ID{u.ID},
		Target:    BulkTarget{MachineIDs: []ID{m}},
	}); err != nil {
		t.Fatal(err)
	}

	first, err := repo.ClaimUpdateJobs(ctx, m, 4)
	if err != nil || len(first) != 1 {
		t.Fatalf("first claim: %v %+v", err, first)
	}

	// A fresh 'running' claim is NOT handed out again.
	again, err := repo.ClaimUpdateJobs(ctx, m, 4)
	if err != nil || len(again) != 0 {
		t.Fatalf("second claim should be empty: %v %+v", err, again)
	}

	// Backdate the claim past the two-hour window: the job requeues and
	// is claimed again on the next poll.
	if _, err := repo.db.ExecContext(ctx, `
		UPDATE update_deployment_job
		SET claimed_at = datetime('now', '-3 hours')
		WHERE id = ?`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.ClaimUpdateJobs(ctx, m, 4)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].ID != first[0].ID {
		t.Fatalf("stale job should be reclaimed: %v %+v", err, reclaimed)
	}

	// A completed job is never resurrected, no matter how old its claim.
	if err := repo.CompleteUpdateJob(ctx, first[0].ID, "ok", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `
		UPDATE update_deployment_job
		SET claimed_at = datetime('now', '-3 hours')
		WHERE id = ?`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	final, err := repo.ClaimUpdateJobs(ctx, m, 4)
	if err != nil || len(final) != 0 {
		t.Fatalf("completed job must stay completed: %v %+v", err, final)
	}
}

func TestAutoDeployFlagRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := newUpdateTestEnv(t)
	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB1", Title: "t", AutoDeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, u.ID); !got.AutoDeploy {
		t.Errorf("AutoDeploy lost on Get: %+v", got)
	}
	list, _ := repo.List(ctx)
	if len(list) != 1 || !list[0].AutoDeploy {
		t.Errorf("AutoDeploy lost on List: %+v", list)
	}
	u.AutoDeploy = false
	if err := repo.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, u.ID); got.AutoDeploy {
		t.Errorf("AutoDeploy not cleared on Update: %+v", got)
	}
}

func TestEnsureAutoDeployJobs(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	win10 := addMachine("uuid-10", "Microsoft Windows 10 Pro")
	win11 := addMachine("uuid-11", "Microsoft Windows 11 Pro")
	done := addMachine("uuid-done", "Microsoft Windows 10 Pro")

	u, err := repo.Create(ctx, WindowsUpdate{
		KBNumber: "KB5034122", Title: "CU", OSFilter: "windows-10", AutoDeploy: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// No payload yet: nothing queues anywhere.
	if err := repo.EnsureAutoDeployJobs(ctx, win10); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, win10, 4); len(jobs) != 0 {
		t.Fatalf("payloadless update must not auto-queue: %+v", jobs)
	}

	if err := repo.SetPayload(ctx, u.ID, "updates/1/kb.msu", "kb.msu", 1); err != nil {
		t.Fatal(err)
	}
	// done already has the KB installed.
	if err := repo.UpsertMachineStatuses(ctx, done, []MachineUpdateStatus{
		{MachineID: done, KBNumber: "KB5034122", Status: "installed"},
	}); err != nil {
		t.Fatal(err)
	}

	for _, m := range []ID{win10, win11, done} {
		if err := repo.EnsureAutoDeployJobs(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// Idempotent: a second pass adds nothing.
	if err := repo.EnsureAutoDeployJobs(ctx, win10); err != nil {
		t.Fatal(err)
	}

	if jobs, _ := repo.ClaimUpdateJobs(ctx, win10, 8); len(jobs) != 1 || jobs[0].UpdateID != u.ID {
		t.Fatalf("win10 should have exactly one auto job: %+v", jobs)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, win11, 8); len(jobs) != 0 {
		t.Fatalf("win11 fails the OS filter, got %+v", jobs)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, done, 8); len(jobs) != 0 {
		t.Fatalf("already-installed machine must not requeue, got %+v", jobs)
	}

	// The auto jobs hang off one per-update deployment owned by 'auto-deploy'.
	deps, err := repo.ListDeployments(ctx)
	if err != nil || len(deps) != 1 || deps[0].CreatedBy != "auto-deploy" {
		t.Fatalf("auto deployment row: %v %+v", err, deps)
	}

	// A reimage wipes the machine's update state; the next ensure pass
	// queues the update again.
	if err := repo.ClearMachineState(ctx, win10); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureAutoDeployJobs(ctx, win10); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, win10, 8); len(jobs) != 1 {
		t.Fatalf("reimaged machine should be requeued: %+v", jobs)
	}
}

func TestEnsureAutoDeployRetryPolicy(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	m := addMachine("uuid-1", "Microsoft Windows 10 Pro")
	u, err := repo.Create(ctx, WindowsUpdate{KBNumber: "KB1", Title: "t", AutoDeploy: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetPayload(ctx, u.ID, "updates/1/kb.msu", "kb.msu", 1); err != nil {
		t.Fatal(err)
	}

	failOnce := func() UpdateDeploymentJob {
		t.Helper()
		if err := repo.EnsureAutoDeployJobs(ctx, m); err != nil {
			t.Fatal(err)
		}
		jobs, err := repo.ClaimUpdateJobs(ctx, m, 8)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("expected one job to claim: %v %+v", err, jobs)
		}
		if err := repo.CompleteUpdateJob(ctx, jobs[0].ID, "failed", "{}"); err != nil {
			t.Fatal(err)
		}
		return jobs[0]
	}
	backdate := func(jobID ID) {
		t.Helper()
		if _, err := repo.db.ExecContext(ctx, `
			UPDATE update_deployment_job
			SET completed_at = datetime('now', '-7 hours')
			WHERE id = ?`, jobID); err != nil {
			t.Fatal(err)
		}
	}

	// Attempt 1 fails; a fresh failure blocks an immediate retry.
	j := failOnce()
	if err := repo.EnsureAutoDeployJobs(ctx, m); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, m, 8); len(jobs) != 0 {
		t.Fatalf("recent failure must not retry immediately: %+v", jobs)
	}

	// Once the failure ages past the spacing window, attempt 2 queues.
	backdate(j.ID)
	j = failOnce()
	backdate(j.ID)
	j = failOnce()
	backdate(j.ID)

	// Three attempts exhausted: no more auto retries, ever.
	if err := repo.EnsureAutoDeployJobs(ctx, m); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := repo.ClaimUpdateJobs(ctx, m, 8); len(jobs) != 0 {
		t.Fatalf("attempt cap must hold: %+v", jobs)
	}
}

// matchMachines must honor the OS filter: the portal's dash-style values
// ("windows-10") must match WMI captions ("Microsoft Windows 10 Pro"), and a
// machine with no reported hardware never matches an explicit filter.
func TestMatchMachinesOSFilter(t *testing.T) {
	ctx := context.Background()
	repo, _, addMachine := newUpdateTestEnv(t)
	win10 := addMachine("uuid-10", "Microsoft Windows 10 Pro")
	win11 := addMachine("uuid-11", "Microsoft Windows 11 Pro")
	srv := addMachine("uuid-srv", "Microsoft Windows Server 2022 Standard")
	noHW := addMachine("uuid-nohw", "")

	ids := func(ms []MachineRecord) map[ID]bool {
		out := map[ID]bool{}
		for _, m := range ms {
			out[m.ID] = true
		}
		return out
	}

	got, err := repo.PreviewTargets(ctx, BulkTarget{}, "windows-10")
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(got) != 1 || !g[win10] {
		t.Errorf("windows-10 matched %v, want only machine %d", got, win10)
	}

	got, err = repo.PreviewTargets(ctx, BulkTarget{}, "server-2022")
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(got) != 1 || !g[srv] {
		t.Errorf("server-2022 matched %v, want only machine %d", got, srv)
	}

	// No filter: everyone, including the hardware-less machine.
	got, err = repo.PreviewTargets(ctx, BulkTarget{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := ids(got); len(got) != 4 || !g[win11] || !g[noHW] {
		t.Errorf("no filter matched %v, want all 4", got)
	}
}
