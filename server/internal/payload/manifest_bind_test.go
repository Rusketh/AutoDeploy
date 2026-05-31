package payload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// A menu-driven deploy must bind the machine to the image it deploys, so
// the agent's /api/v1/agent/self can resolve that image's software set
// post-OS. Without this the machine is left unbound and nothing installs.
func TestManifestBindsDeployedImage(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)

	mh := &ManifestHandler{Resolver: resolve.New(imgs, isos, una), Inventory: inv}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	isoID := iso.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID})

	// POST with identity = a real deploy of img to this machine.
	body, _ := json.Marshal(match.Identity{SystemUUID: "bind-uuid"})
	resp, err := http.Post(fmt.Sprintf("%s/api/v1/images/%d/manifest", srv.URL, int64(img.ID)),
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status = %d, want 200", resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.AgentID == "" {
		t.Error("manifest carried no agent_id for registry provisioning")
	}

	// The machine must now be bound to the deployed image.
	machine, err := inv.GetByUUID(ctx, "bind-uuid")
	if err != nil {
		t.Fatal(err)
	}
	b, err := inv.GetBinding(ctx, machine.ID)
	if err != nil {
		t.Fatalf("expected a binding after deploy: %v", err)
	}
	if b.ImageID == nil || *b.ImageID != img.ID {
		t.Errorf("binding image = %v, want %d", b.ImageID, int64(img.ID))
	}
}
