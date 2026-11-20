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
	"github.com/rusketh/autodeploy/server/internal/payload"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// A re-image an operator queued from the portal runs unattended (the boot
// client skips the access PIN), so the "staging" report must authorise the
// deploy the way a PIN entry does; otherwise the manifest withholds the new
// OS's agent secret and the agent waits for approval for ever. Only the
// flagged image counts, and an unflagged deploy is never authorised here.
func TestOperatorReimageAuthorizesAgentSecret(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	isos, una, imgs := model.NewISORepo(db), model.NewUnattendRepo(db), model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	creds := model.NewAgentCredentialRepo(db)
	mux := http.NewServeMux()
	Register(mux, Repos{
		ISOs: isos, Unattend: una, Images: imgs, Inventory: inv,
		Resolver: resolve.New(imgs, isos, una), Credentials: creds,
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iso, _ := isos.Create(ctx, model.ISO{Name: "W11", OSType: "windows-11"})
	bound, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &iso.ID})
	other, _ := imgs.Create(ctx, model.Image{Name: "office", ISOID: &iso.ID})

	machine := func(uuid string, flag bool, flagImage model.ID) {
		t.Helper()
		m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: uuid})
		if err != nil {
			t.Fatal(err)
		}
		_ = inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &bound.ID})
		// Already on AutoDeploy: it has a secret, so a later request for
		// one would need an operator's approval.
		if _, err := creds.Issue(ctx, m.ID, "pxe", false); err != nil {
			t.Fatal(err)
		}
		if flag {
			_ = inv.SetReimagePending(ctx, m.ID, flagImage)
		}
	}
	staging := func(uuid string, image model.ID) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"identity": map[string]any{"system_uuid": uuid}, "status": "staging", "image_id": image,
		})
		resp, err := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("staging %s = %d", uuid, resp.StatusCode)
		}
	}

	machine("flag-bound", true, 0)           // flag names no image: the bound one
	machine("flag-explicit", true, other.ID) // flag names an image
	machine("flag-mismatch", true, other.ID) // deploys something else
	machine("unflagged", false, 0)           // ordinary boot-menu deploy
	staging("flag-bound", bound.ID)
	staging("flag-explicit", other.ID)
	staging("flag-mismatch", bound.ID)
	staging("unflagged", bound.ID)

	for uuid, want := range map[string]bool{
		"flag-bound": true, "flag-explicit": true, "flag-mismatch": false, "unflagged": false,
	} {
		if got := creds.DeployAuthorized(ctx, uuid, payload.DeployAuthWindow); got != want {
			t.Errorf("%s: deploy authorised = %v, want %v", uuid, got, want)
		}
	}
}
