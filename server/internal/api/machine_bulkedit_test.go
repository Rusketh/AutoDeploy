package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestBulkBindingEndpoint(t *testing.T) {
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		Users:     auth.New(db),
		Inventory: inv,
		Images:    model.NewImageRepo(db),
		Logs:      model.NewLogRepo(db),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ctx := context.Background()

	m1, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "be-1"})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "be-2"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := repos.Images.Create(ctx, model.Image{Name: "lab"})
	if err != nil {
		t.Fatal(err)
	}

	// Unauthenticated -> 401.
	resp, err := http.Post(srv.URL+"/api/v1/machines/bulk-binding", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", resp.StatusCode)
	}

	client := authedClient(t, srv, repos)

	// Unknown image -> 400.
	r, _ := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/machines/bulk-binding", map[string]any{
		"machine_ids": []model.ID{m1.ID},
		"set":         map[string]any{"image_id": 999},
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown image status = %d, want 400", r.StatusCode)
	}

	// Happy path: set image + OU + groups on both machines.
	r, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/machines/bulk-binding", map[string]any{
		"machine_ids": []model.ID{m1.ID, m2.ID},
		"set": map[string]any{
			"image_id":   img.ID,
			"target_ou":  "OU=Lab,DC=corp",
			"add_groups": []string{"Lab-Computers"},
		},
	})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("bulk edit status = %d body=%s", r.StatusCode, body)
	}
	var out struct {
		Matched int `json:"matched"`
		Updated int `json:"updated"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Matched != 2 || out.Updated != 2 {
		t.Errorf("response = %s (err %v)", body, err)
	}

	b, err := inv.GetBinding(ctx, m2.ID)
	if err != nil || b.ImageID == nil || *b.ImageID != img.ID ||
		b.TargetOU != "OU=Lab,DC=corp" || len(b.GroupMemberships) != 1 {
		t.Errorf("binding = %+v err=%v", b, err)
	}

	// The edit is audited.
	events, err := repos.Logs.Search(ctx, model.LogSearch{Action: "binding.bulk_edit"})
	if err != nil || len(events) != 1 || events[0].Actor != "admin" {
		t.Errorf("audit events: %v %+v", err, events)
	}

	// An empty edit -> 400 (validation).
	r, _ = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/machines/bulk-binding", map[string]any{
		"machine_ids": []model.ID{m1.ID},
		"set":         map[string]any{},
	})
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("empty edit status = %d, want 400", r.StatusCode)
	}
}
