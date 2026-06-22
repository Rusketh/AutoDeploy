package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestDeployStagingClearsReimagedState verifies that reporting a "staging"
// deploy status resets the machine's stale per-machine runtime state —
// software detection results, Windows-update compliance + jobs, and bulk
// jobs — while preserving deployment history and the fleet-wide operation
// rows.
func TestDeployStagingClearsReimagedState(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	sw := model.NewSoftwarePackageRepo(db)
	bulk := model.NewBulkRepo(db, inv)
	updates := model.NewWindowsUpdateRepo(db, inv)
	repos := Repos{
		ISOs: isos, Unattend: una, Images: imgs, Inventory: inv,
		Software: sw, Bulk: bulk, Updates: updates,
		Resolver: resolve.New(imgs, isos, una),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iso, _ := isos.Create(ctx, model.ISO{Name: "W11", OSType: "windows-11"})
	isoID := iso.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID})
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "reimage-1"})
	_ = inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID})

	// Seed stale per-machine state.
	pkg, _ := sw.Create(ctx, model.SoftwarePackage{Name: "Acme"})
	if err := inv.RecordDetectedState(ctx, model.DetectedState{
		MachineID: m.ID, SoftwarePackageID: pkg.ID, Detected: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := updates.UpsertMachineStatuses(ctx, m.ID, []model.MachineUpdateStatus{
		{KBNumber: "KB5000001", Status: "installed"},
	}); err != nil {
		t.Fatal(err)
	}
	// A bulk operation targeting this machine creates a bulk_job row.
	op, jobs, err := bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionScript, Payload: `{"shell":"cmd","body":"echo hi"}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 seeded bulk job, got %d", len(jobs))
	}

	// Sanity: state is present before staging.
	if d, _ := inv.DetectedStateFor(ctx, m.ID); len(d) != 1 {
		t.Fatalf("precondition: want 1 detected row, got %d", len(d))
	}
	if s, _ := updates.ListMachineStatuses(ctx, m.ID); len(s) != 1 {
		t.Fatalf("precondition: want 1 update status, got %d", len(s))
	}

	// Report staging — the cleanup hook fires here.
	sbody, _ := json.Marshal(map[string]any{
		"identity": map[string]any{"system_uuid": "reimage-1"}, "status": "staging",
	})
	resp, _ := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(sbody))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deploy-status staging = %d", resp.StatusCode)
	}

	// Stale per-machine state is gone.
	if d, _ := inv.DetectedStateFor(ctx, m.ID); len(d) != 0 {
		t.Errorf("detected state not cleared: %+v", d)
	}
	if s, _ := updates.ListMachineStatuses(ctx, m.ID); len(s) != 0 {
		t.Errorf("update status not cleared: %+v", s)
	}
	if remaining := countBulkJobs(t, db, m.ID); remaining != 0 {
		t.Errorf("bulk jobs not cleared: %d remain", remaining)
	}

	// Fleet-wide operation row survives (only the per-machine job is gone).
	if gotOp, _, gerr := bulk.GetOperation(ctx, op.ID); gerr != nil || gotOp.ID != op.ID {
		t.Errorf("bulk_operation should survive cleanup: err=%v op=%+v", gerr, gotOp)
	}
	// Deployment history is preserved (the staging report itself adds a row).
	if h, _ := inv.HistoryFor(ctx, m.ID); len(h) == 0 {
		t.Error("deployment history should be preserved/created, got none")
	}
}

// TestReimageRecordsHistoryEvent verifies that re-imaging a remote-flagged
// machine records a re-image history event, while a first-time deploy (no
// flag, no prior deploy) records none.
func TestReimageRecordsHistoryEvent(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		ISOs: isos, Unattend: una, Images: imgs, Inventory: inv,
		Resolver: resolve.New(imgs, isos, una),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iso, _ := isos.Create(ctx, model.ISO{Name: "W11", OSType: "windows-11"})
	isoID := iso.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID})
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "ri-hist"})
	_ = inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID})

	stage := func() {
		sbody, _ := json.Marshal(map[string]any{
			"identity": map[string]any{"system_uuid": "ri-hist"}, "status": "staging",
		})
		resp, _ := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(sbody))
		resp.Body.Close()
	}

	// First staging with NO re-image flag and no prior deploy = first install:
	// no re-image event recorded.
	stage()
	if ev, _ := inv.ListReimageEvents(ctx, m.ID); len(ev) != 0 {
		t.Fatalf("first install should record no re-image event, got %d", len(ev))
	}

	// Now flag a remote re-image and stage again.
	if err := inv.SetReimagePending(ctx, m.ID, 0); err != nil {
		t.Fatal(err)
	}
	stage()

	events, _ := inv.ListReimageEvents(ctx, m.ID)
	if len(events) != 1 {
		t.Fatalf("want 1 re-image event after remote re-image, got %d", len(events))
	}
	if events[0].Source != model.ReimageSourceRemoteFlag {
		t.Errorf("source = %q, want %q", events[0].Source, model.ReimageSourceRemoteFlag)
	}
}

func countBulkJobs(t *testing.T, db *storage.DB, machineID model.ID) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM bulk_job WHERE machine_id=?`, machineID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
