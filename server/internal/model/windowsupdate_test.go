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
