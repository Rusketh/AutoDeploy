package api

import (
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

// newSelfTestServer wires the repos /api/v1/agent/self needs (Inventory +
// Bulk + Resolver), which the shared newTestServer harness omits.
func newSelfTestServer(t *testing.T) (*httptest.Server, Repos) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		ISOs:      model.NewISORepo(db),
		Unattend:  model.NewUnattendRepo(db),
		Software:  model.NewSoftwarePackageRepo(db),
		Images:    model.NewImageRepo(db),
		Inventory: inv,
		Bulk:      model.NewBulkRepo(db, inv),
		Resolver:  resolve.New(model.NewImageRepo(db), model.NewISORepo(db), model.NewUnattendRepo(db)),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func TestAgentSelfByAgentID(t *testing.T) {
	srv, repos := newSelfTestServer(t)
	ctx := context.Background()

	// A known machine bound to an image.
	m, err := repos.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "self-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if m.AgentID == "" {
		t.Fatal("machine has no minted agent_id")
	}
	img, err := repos.Images.Create(ctx, model.Image{Name: "base"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}

	// Poll /self by the server-minted agent_id.
	resp, err := http.Get(srv.URL + "/api/v1/agent/self?id=" + m.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out AgentSelfResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.MachineID != m.ID || out.AgentID != m.AgentID || out.ImageID != img.ID {
		t.Errorf("self response mismatch: %+v (machine %d image %d)", out, m.ID, img.ID)
	}
	// Software/Jobs are always non-nil arrays (never null) for the agent.
	if out.Software == nil || out.Jobs == nil {
		t.Errorf("software/jobs should be empty arrays, not null: %+v", out)
	}

	// Unknown agent_id -> 404; missing id -> 400.
	if r, _ := http.Get(srv.URL + "/api/v1/agent/self?id=nope"); r.StatusCode != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", r.StatusCode)
	}
	if r, _ := http.Get(srv.URL + "/api/v1/agent/self"); r.StatusCode != http.StatusBadRequest {
		t.Errorf("missing id status = %d, want 400", r.StatusCode)
	}
}

// TestAgentSelfSetupLock asserts /self advertises the per-image setup-lock
// toggle so the agent knows whether to maintain the lock marker.
func TestAgentSelfSetupLock(t *testing.T) {
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	images := model.NewImageRepo(db)
	setupLock := model.NewSetupLockRepo(db)
	repos := Repos{
		Images:    images,
		Inventory: inv,
		Bulk:      model.NewBulkRepo(db, inv),
		Resolver:  resolve.New(images, model.NewISORepo(db), model.NewUnattendRepo(db)),
		SetupLock: setupLock,
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "lock-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Create(ctx, model.Image{Name: "locked"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}

	get := func() AgentSelfResponse {
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

	if got := get(); got.SetupLock {
		t.Errorf("SetupLock = true, want false when unset")
	}
	if err := setupLock.Set(ctx, model.ImageSetupLock{ImageID: img.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got := get(); !got.SetupLock {
		t.Errorf("SetupLock = false, want true when enabled")
	}
	if err := setupLock.Set(ctx, model.ImageSetupLock{ImageID: img.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if got := get(); got.SetupLock {
		t.Errorf("SetupLock = true, want false after disable")
	}
}
