package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestAgentDomainJoinEndpoint(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	inv := model.NewInventoryRepo(db)
	images := model.NewImageRepo(db)
	dj := model.NewDomainJoinRepo(db, bx)
	repos := Repos{Inventory: inv, Images: images, DomainJoin: dj}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "dj-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Create(ctx, model.Image{Name: "win11-dj"})
	if err != nil {
		t.Fatal(err)
	}

	post := func(t *testing.T) AgentDomainJoinResponse {
		t.Helper()
		resp, err := http.Post(srv.URL+"/api/v1/agent/domain-join",
			"application/json", strings.NewReader(`{"agent_id":"`+m.AgentID+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out AgentDomainJoinResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// No binding yet -> no join.
	if post(t).Join {
		t.Error("expected join=false with no binding")
	}

	// Bind to the image, but domain join not configured -> no join.
	if err := inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	if post(t).Join {
		t.Error("expected join=false before domain join is enabled")
	}

	// Configure + enable agent join on the image.
	if err := dj.Set(ctx, model.ImageDomainJoin{
		ImageID: img.ID, Enabled: true,
		Domain: "corp.example.com", OU: "OU=Default,DC=corp,DC=example",
		JoinUser: "CORP\\join",
	}, "join-pw"); err != nil {
		t.Fatal(err)
	}
	got := post(t)
	if !got.Join || got.Domain != "corp.example.com" || got.User != "CORP\\join" || got.Password != "join-pw" {
		t.Fatalf("expected full join config; got %+v", got)
	}
	if got.OU != "OU=Default,DC=corp,DC=example" {
		t.Errorf("OU = %q (want image default)", got.OU)
	}

	// A binding TargetOU overrides the image's default OU.
	if err := inv.UpsertBinding(ctx, model.MachineBinding{
		MachineID: m.ID, ImageID: &img.ID, TargetOU: "OU=Lab,DC=corp,DC=example",
	}); err != nil {
		t.Fatal(err)
	}
	if got := post(t); got.OU != "OU=Lab,DC=corp,DC=example" {
		t.Errorf("binding OU should override; got %q", got.OU)
	}

	// Disabling turns the join back off.
	if err := dj.Set(ctx, model.ImageDomainJoin{ImageID: img.ID, Enabled: false, Domain: "corp.example.com"}, ""); err != nil {
		t.Fatal(err)
	}
	if post(t).Join {
		t.Error("expected join=false once disabled")
	}
}
