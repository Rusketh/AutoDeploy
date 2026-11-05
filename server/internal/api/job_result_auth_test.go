package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

func TestAgentJobResultRequiresOwningMachine(t *testing.T) {
	ctx := context.Background()
	f := newToolkitAPIFixture(t)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "OWNER-UUID"})
	other, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "OTHER-UUID"})
	_, jobs, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionScript, Payload: `{"shell":"cmd","body":"echo"}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/agent/jobs/" + strconv.FormatInt(int64(jobs[0].ID), 10) + "/result"
	post := func(body, uuid string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", f.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if uuid != "" {
			req.Header.Set("X-AutoDeploy-Machine-UUID", uuid)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	ok := `{"status":"ok","agent_id":"` + m.AgentID + `"}`

	// Not yet claimed: even the owner can't report it.
	if c := post(ok, ""); c != http.StatusConflict {
		t.Fatalf("unclaimed: %d", c)
	}
	if _, err := f.bulk.ClaimJobsFor(ctx, m.ID, 0); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]int{
		"no identity":        post(`{"status":"ok"}`, ""),
		"other machine UUID": post(`{"status":"ok"}`, "OTHER-UUID"),
		"other agent_id":     post(`{"status":"ok","agent_id":"`+other.AgentID+`"}`, "OWNER-UUID"),
		"bogus agent_id":     post(`{"status":"ok","agent_id":"nope"}`, ""),
	} {
		if c != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", name, c)
		}
	}
	if j, _ := f.bulk.GetJob(ctx, jobs[0].ID); j.Status != "running" {
		t.Fatalf("a refused report changed the job: %s", j.Status)
	}
	if c := post(ok, ""); c != http.StatusNoContent {
		t.Fatalf("owner by agent_id: %d", c)
	}
	// Reported once; a second report is refused.
	if c := post(`{"status":"failed"}`, "owner-uuid"); c != http.StatusConflict {
		t.Fatalf("second report: %d", c)
	}
	// An unknown job reveals nothing beyond "forbidden".
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/v1/agent/jobs/99999/result", strings.NewReader(ok))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown job: %v %v", resp, err)
	}
}

func TestReimageCleanupFinishesOrphanedOperation(t *testing.T) {
	ctx := context.Background()
	f := newToolkitAPIFixture(t)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "RI-1"})
	m2, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "RI-2"})
	kit, _ := f.kits.Create(ctx, model.Toolkit{Name: "kit", StepsJSON: `[{"type":"cmd","script_body":"x"}]`}, "")
	kid := strconv.FormatInt(int64(kit.ID), 10)

	// Op A: only this machine -> completed by the cleanup, override purged.
	opA, _, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionToolkit, Payload: `{"toolkit_id":` + kid + `,"run_as":"user","run_as_user":"svc"}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.kits.SetOperationCredential(ctx, opA.ID, "svc", "pw"); err != nil {
		t.Fatal(err)
	}
	// Op B: also targets another machine still pending -> stays active.
	opB, _, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionToolkit, Payload: `{"toolkit_id":` + kid + `}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID, m2.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}

	finished := clearReimagedState(ctx, Repos{Inventory: f.inv, Bulk: f.bulk}, m.ID)
	if len(finished) != 1 || finished[0] != opA.ID {
		t.Fatalf("finished = %v, want [%d]", finished, opA.ID)
	}
	if a, _, _ := f.bulk.GetOperation(ctx, opA.ID); a.Status != model.BulkStatusCompleted {
		t.Errorf("op A status = %s", a.Status)
	}
	if b, _, _ := f.bulk.GetOperation(ctx, opB.ID); b.Status != model.BulkStatusActive {
		t.Errorf("op B status = %s", b.Status)
	}
	if _, ok, _ := f.kits.HasOperationCredential(ctx, opA.ID); ok {
		t.Error("override password survived the cleanup")
	}
	// The kit is deletable again once nothing is pending for it... except op B.
	if err := f.kits.Delete(ctx, kit.ID); err == nil {
		t.Error("kit deleted while op B is pending")
	}
}
