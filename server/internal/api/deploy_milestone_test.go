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

// TestDeployMilestoneUpdatesOpenRow verifies that a "specialize" milestone
// (the unattend's install-status callback) advances the open in_progress
// row's note, and that a late milestone arriving after the agent closed the
// row is a no-op.
func TestDeployMilestoneUpdatesOpenRow(t *testing.T) {
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

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "ms-1"})
	depID, _ := inv.RecordDeployment(ctx, m.ID, nil)

	post := func(status, notes string) int {
		body, _ := json.Marshal(map[string]any{
			"identity": map[string]any{"system_uuid": "ms-1"},
			"status":   status, "notes": notes,
		})
		resp, _ := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(body))
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("specialize", "reached specialize"); code != http.StatusOK {
		t.Fatalf("specialize milestone = %d", code)
	}
	hist, _ := inv.HistoryFor(ctx, m.ID)
	if hist[0].Outcome != "in_progress" || hist[0].CompletedAt != nil || hist[0].Notes != "reached specialize" || hist[0].Phase != "specialize" {
		t.Errorf("after milestone: %+v", hist[0])
	}

	// Agent closes ok; a late milestone must not change anything.
	if err := inv.CompleteDeployment(ctx, depID, "ok", "done"); err != nil {
		t.Fatal(err)
	}
	if code := post("first-logon", "late callback"); code != http.StatusOK {
		t.Fatalf("late milestone = %d", code)
	}
	hist, _ = inv.HistoryFor(ctx, m.ID)
	if hist[0].Outcome != "ok" || hist[0].Notes != "done" || hist[0].Phase != "complete" {
		t.Errorf("late milestone mutated closed row: %+v", hist[0])
	}
}
