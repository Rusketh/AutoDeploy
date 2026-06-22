package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newGroupTestServer(t *testing.T) (*httptest.Server, Repos) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		Users:     auth.New(db),
		Inventory: inv,
		Groups:    model.NewMachineGroupRepo(db, inv),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func TestMachineGroupCRUDOverHTTP(t *testing.T) {
	srv, repos := newGroupTestServer(t)
	client := authedClient(t, srv, repos)

	// Create a manual group.
	resp, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/machine-groups", map[string]any{
		"name": "Lab", "kind": "manual",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var g model.MachineGroup
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatal(err)
	}
	if g.ID == 0 || g.Kind != model.MachineGroupManual {
		t.Fatalf("bad create body: %+v", g)
	}

	// A dynamic group with no filter is a 400.
	resp, _ = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/machine-groups", map[string]any{
		"name": "BadDyn", "kind": "dynamic",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty dynamic filter: want 400, got %d", resp.StatusCode)
	}

	// List returns the one group.
	resp, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/machine-groups", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: %d", resp.StatusCode)
	}
	var list []model.MachineGroup
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}

	// Seed a machine, add it as a member, read members back.
	rec, err := repos.Inventory.UpsertFromIdentity(context.Background(), match.Identity{SystemUUID: "uX"})
	if err != nil {
		t.Fatal(err)
	}
	membersURL := fmt.Sprintf("%s/api/v1/machine-groups/%d/members", srv.URL, g.ID)
	resp, body = doJSON(t, client, http.MethodPost, membersURL, map[string]any{
		"machine_ids": []int64{int64(rec.ID)},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add members: %d %s", resp.StatusCode, body)
	}
	resp, body = doJSON(t, client, http.MethodGet, membersURL, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get members: %d", resp.StatusCode)
	}
	var mb groupMembersBody
	if err := json.Unmarshal(body, &mb); err != nil {
		t.Fatal(err)
	}
	if len(mb.MachineIDs) != 1 || mb.MachineIDs[0] != rec.ID {
		t.Errorf("members = %+v, want [%d]", mb.MachineIDs, rec.ID)
	}

	// Delete.
	resp, _ = doJSON(t, client, http.MethodDelete, fmt.Sprintf("%s/api/v1/machine-groups/%d", srv.URL, g.ID), nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, client, http.MethodGet, fmt.Sprintf("%s/api/v1/machine-groups/%d", srv.URL, g.ID), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("get after delete: want 404, got %d", resp.StatusCode)
	}
}
