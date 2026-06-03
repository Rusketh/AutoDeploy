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

// TestAgentDeployProgressAdvancesOpenRow verifies the agent's deploy-progress
// callback advances an open row through the agent-online and installing phases
// (recording the package counts) without closing it, validates the phase token,
// and is a no-op once the row has been closed.
func TestAgentDeployProgressAdvancesOpenRow(t *testing.T) {
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

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "prog-1"})
	depID, _ := inv.RecordDeployment(ctx, m.ID, nil)

	post := func(phase string, done, total int) int {
		body, _ := json.Marshal(map[string]any{
			"identity":      map[string]any{"system_uuid": "prog-1"},
			"deployment_id": depID,
			"phase":         phase, "done": done, "total": total,
			"notes": "n",
		})
		resp, _ := http.Post(srv.URL+"/api/v1/agent/deploy-progress", "application/json", bytes.NewReader(body))
		resp.Body.Close()
		return resp.StatusCode
	}

	// agent-online: Windows installed, agent running.
	if code := post("agent-online", 0, 0); code != http.StatusOK {
		t.Fatalf("agent-online = %d", code)
	}
	if open, _ := inv.LatestOpenDeployment(ctx, m.ID); open.Phase != "agent-online" {
		t.Errorf("phase after agent-online = %q", open.Phase)
	}

	// installing 2/5.
	if code := post("installing", 2, 5); code != http.StatusOK {
		t.Fatalf("installing = %d", code)
	}
	open, _ := inv.LatestOpenDeployment(ctx, m.ID)
	if open.Phase != "installing" || open.ProgressDone != 2 || open.ProgressTotal != 5 || open.Outcome != "in_progress" {
		t.Errorf("after installing 2/5: %+v", open)
	}
	if label, pct, _ := model.DeployRecordProgress(open); label != "Installing software (2/5)" || pct < 75 || pct > 92 {
		t.Errorf("render = (%q,%d)", label, pct)
	}

	// A bogus phase is rejected (can't park the bar on an unknown token).
	if code := post("bogus", 0, 0); code != http.StatusBadRequest {
		t.Errorf("bogus phase = %d, want 400", code)
	}

	// Once the agent closes the row, a late progress post is a no-op.
	if err := inv.CompleteDeployment(ctx, depID, "ok", "done"); err != nil {
		t.Fatal(err)
	}
	if code := post("installing", 4, 5); code != http.StatusOK {
		t.Fatalf("late progress = %d", code)
	}
	hist, _ := inv.HistoryFor(ctx, m.ID)
	if hist[0].Outcome != "ok" || hist[0].Phase != "complete" || hist[0].ProgressDone != 2 {
		t.Errorf("late progress mutated closed row: %+v", hist[0])
	}
}
