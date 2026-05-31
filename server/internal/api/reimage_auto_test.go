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

func TestBootMenuAutoDeployWhenReimageFlagged(t *testing.T) {
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

	menu := func() BootMenuResponse {
		body, _ := json.Marshal(map[string]any{"system_uuid": "auto-1"})
		resp, _ := http.Post(srv.URL+"/api/v1/clients/menu", "application/json", bytes.NewReader(body))
		defer resp.Body.Close()
		var out BootMenuResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	// Register the machine + bind it to the image.
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "auto-1"})
	_ = inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID})

	// Not flagged: no auto-deploy.
	if menu().AutoDeployImageID != 0 {
		t.Error("auto-deploy should be 0 when not flagged")
	}
	// Flag with image 0 => falls back to the binding's image.
	_ = inv.SetReimagePending(ctx, m.ID, 0)
	if got := menu().AutoDeployImageID; got != img.ID {
		t.Errorf("auto-deploy = %d, want bound image %d", got, img.ID)
	}
	// Boot client reports "staging" -> flag clears -> no more auto-deploy.
	sbody, _ := json.Marshal(map[string]any{
		"identity": map[string]any{"system_uuid": "auto-1"}, "status": "staging",
	})
	r2, _ := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(sbody))
	r2.Body.Close()
	if menu().AutoDeployImageID != 0 {
		t.Error("auto-deploy should be cleared after staging report")
	}
}
