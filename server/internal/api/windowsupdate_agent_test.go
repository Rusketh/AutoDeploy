package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// newUpdatesTestServer wires the repos the agent update endpoints need.
func newUpdatesTestServer(t *testing.T) (*httptest.Server, Repos) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		Images:    model.NewImageRepo(db),
		Inventory: inv,
		Bulk:      model.NewBulkRepo(db, inv),
		Resolver:  resolve.New(model.NewImageRepo(db), model.NewISORepo(db), model.NewUnattendRepo(db)),
		Updates:   model.NewWindowsUpdateRepo(db, inv),
		Logs:      model.NewLogRepo(db),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

// deployOneUpdate creates a machine, an update with a payload, and a
// deployment targeting the machine; returns both records.
func deployOneUpdate(t *testing.T, repos Repos) (model.MachineRecord, model.WindowsUpdate) {
	t.Helper()
	ctx := context.Background()
	m, err := repos.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "wu-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := repos.Updates.Create(ctx, model.WindowsUpdate{
		KBNumber: "KB5034441", Title: "2024-01 Cumulative Update", RebootAfter: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Updates.SetPayload(ctx, u.ID,
		"updates/1/windows10.0-kb5034441-x64.msu", "windows10.0-kb5034441-x64.msu", 123456); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repos.Updates.CreateDeployment(ctx, model.UpdateDeployment{
		UpdateIDs: []model.ID{u.ID},
		Target:    model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	return m, u
}

// /agent/self must deliver update jobs enriched with kb_number,
// payload_filename, and reboot_after — and stay decodable by old agents
// that only know the bare job fields.
func TestAgentSelfEnrichedUpdateJobs(t *testing.T) {
	srv, repos := newUpdatesTestServer(t)
	m, u := deployOneUpdate(t, repos)

	resp, err := http.Get(srv.URL + "/api/v1/agent/self?id=" + m.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw bytes.Buffer
	var out AgentSelfResponse
	if err := json.NewDecoder(io.TeeReader(resp.Body, &raw)).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.UpdateJobs) != 1 {
		t.Fatalf("expected 1 update job, got %+v", out.UpdateJobs)
	}
	j := out.UpdateJobs[0]
	if j.UpdateID != u.ID || j.Status != "running" {
		t.Errorf("job = %+v, want update %d running", j, u.ID)
	}
	if j.KBNumber != "KB5034441" || j.PayloadFilename != "windows10.0-kb5034441-x64.msu" ||
		!j.RebootAfter || j.SizeBytes != 123456 {
		t.Errorf("enrichment missing: %+v", j)
	}

	// Backward compatibility: the same JSON decodes into the legacy agent's
	// job shape (bare IDs only, unknown fields ignored).
	var legacy struct {
		UpdateJobs []struct {
			ID       int64  `json:"id"`
			UpdateID int64  `json:"update_id"`
			Status   string `json:"status"`
		} `json:"update_jobs"`
	}
	if err := json.Unmarshal(raw.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.UpdateJobs) != 1 || legacy.UpdateJobs[0].UpdateID != int64(u.ID) {
		t.Errorf("legacy decode mismatch: %+v", legacy.UpdateJobs)
	}

	// The hand-off is audited so operators can see jobs reached the
	// machine even when its agent logs nothing.
	events, err := repos.Logs.Search(context.Background(), model.LogSearch{Action: "update.jobs.claimed"})
	if err != nil || len(events) != 1 {
		t.Fatalf("claim audit events: %v %+v", err, events)
	}
	if events[0].Actor != m.AgentID || !bytes.Contains([]byte(events[0].Fields), []byte("KB5034441")) {
		t.Errorf("claim audit event = %+v", events[0])
	}
}

// An auto-deploy update must reach an applicable machine through the
// normal /agent/self poll with NO operator-created deployment.
func TestAgentSelfAutoDeployJobs(t *testing.T) {
	srv, repos := newUpdatesTestServer(t)
	ctx := context.Background()

	m, err := repos.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "auto-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.UpdateHardware(ctx, m.ID, model.Hardware{
		OSCaption: "Microsoft Windows 10 Pro",
	}); err != nil {
		t.Fatal(err)
	}
	u, err := repos.Updates.Create(ctx, model.WindowsUpdate{
		KBNumber: "KB5034122", Title: "CU", OSFilter: "windows-10", AutoDeploy: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Updates.SetPayload(ctx, u.ID, "updates/1/kb.msu", "kb.msu", 7); err != nil {
		t.Fatal(err)
	}

	poll := func() AgentSelfResponse {
		t.Helper()
		resp, err := http.Get(srv.URL + "/api/v1/agent/self?id=" + m.AgentID)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out AgentSelfResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := poll()
	if len(first.UpdateJobs) != 1 || first.UpdateJobs[0].UpdateID != u.ID ||
		first.UpdateJobs[0].KBNumber != "KB5034122" {
		t.Fatalf("auto job not delivered: %+v", first.UpdateJobs)
	}
	// The job is now running; the next poll must not duplicate it.
	if second := poll(); len(second.UpdateJobs) != 0 {
		t.Fatalf("auto job duplicated on second poll: %+v", second.UpdateJobs)
	}
}

// A job result must update per-machine compliance immediately: ok →
// installed, failed → failed.
func TestUpdateJobResultUpdatesMachineStatus(t *testing.T) {
	srv, repos := newUpdatesTestServer(t)
	m, u := deployOneUpdate(t, repos)
	ctx := context.Background()

	// Deployment marked it pending.
	statusOf := func() string {
		t.Helper()
		statuses, err := repos.Updates.ListMachineStatuses(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range statuses {
			if s.KBNumber == u.KBNumber {
				return s.Status
			}
		}
		return ""
	}
	if got := statusOf(); got != "pending" {
		t.Fatalf("status after deployment = %q, want pending", got)
	}

	// Claim the job like the agent poll does.
	jobs, err := repos.Updates.ClaimUpdateJobs(ctx, m.ID, 4)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: %v %+v", err, jobs)
	}

	post := func(jobID model.ID, status string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"status": status, "result_json": `{"exit_code":3010}`})
		resp, err := http.Post(
			srv.URL+"/api/v1/agent/update-job/"+strconv.FormatInt(int64(jobID), 10)+"/result",
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("result status = %d, want 200", resp.StatusCode)
		}
	}

	post(jobs[0].ID, "failed")
	if got := statusOf(); got != "failed" {
		t.Errorf("status after failed result = %q, want failed", got)
	}
	post(jobs[0].ID, "ok")
	if got := statusOf(); got != "installed" {
		t.Errorf("status after ok result = %q, want installed", got)
	}

	// Both results were audited; the failed one at WARN.
	events, err := repos.Logs.Search(ctx, model.LogSearch{Action: "update.job.result"})
	if err != nil || len(events) != 2 {
		t.Fatalf("result audit events: %v %+v", err, events)
	}
	levels := map[string]bool{}
	for _, e := range events {
		levels[e.Level] = true
	}
	if !levels["WARN"] || !levels["INFO"] {
		t.Errorf("expected one WARN and one INFO result event, got %+v", events)
	}
}
