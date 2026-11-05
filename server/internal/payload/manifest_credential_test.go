package payload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestManifestIssuesAgentSecret(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	isos, una, images := model.NewISORepo(db), model.NewUnattendRepo(db), model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	creds := model.NewAgentCredentialRepo(db)
	pin := false
	mh := &ManifestHandler{Resolver: resolve.New(images, isos, una), Inventory: inv, Creds: creds,
		PINEnabled: func(context.Context) bool { return pin }}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	img, _ := images.Create(ctx, model.Image{Name: "lab"})

	fetch := func(uuid string, want bool) Manifest {
		t.Helper()
		body := `{"system_uuid":"` + uuid + `","want_credential":` + strconv.FormatBool(want) + `}`
		resp, err := http.Post(srv.URL+"/api/v1/images/"+strconv.FormatInt(int64(img.ID), 10)+"/manifest",
			"application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m Manifest
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m
	}

	// No PIN configured, new boot client: the secret is in the manifest.
	m := fetch("MAN-1", true)
	if m.AgentID == "" || len(m.AgentSecret) < model.MinSecretLength {
		t.Fatalf("manifest without secret: %+v", m)
	}
	rec, _ := inv.GetByUUID(ctx, "MAN-1")
	if ok, _ := creds.Verify(ctx, rec.ID, m.AgentSecret); !ok {
		t.Fatal("manifest secret not stored")
	}

	// A re-deploy keeps the running OS's secret valid until the new one is
	// used (an aborted deploy boots back into it).
	old := m.AgentSecret
	m2 := fetch("MAN-1", true)
	if ok, _ := creds.Verify(ctx, rec.ID, old); !ok {
		t.Fatal("old secret dropped before the new OS authenticated")
	}
	if ok, _ := creds.Verify(ctx, rec.ID, m2.AgentSecret); !ok {
		t.Fatal("new secret rejected")
	}

	// Older boot client (no want_credential): no secret, a one-time grant.
	m3 := fetch("MAN-2", false)
	rec2, _ := inv.GetByUUID(ctx, "MAN-2")
	if m3.AgentSecret != "" || !creds.RedeemDeployGrant(ctx, rec2.ID) {
		t.Fatalf("old boot client: secret=%q, no grant", m3.AgentSecret)
	}

	// Access PIN on: nothing issued unless this machine passed the PIN.
	pin = true
	m4 := fetch("MAN-3", true)
	if m4.AgentSecret != "" {
		t.Fatal("secret issued without the PIN")
	}
	_ = creds.AuthorizeDeploy(ctx, "MAN-3")
	if m5 := fetch("MAN-3", true); m5.AgentSecret == "" {
		t.Fatal("secret not issued after the PIN")
	}
	_ = match.Identity{}
}
