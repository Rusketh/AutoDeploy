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

// TestAgentSelfSequenceCursor verifies that /agent/self returns the resolved
// sequence from the deployment's cursor, and that /agent/sequence-progress
// advances it — the mechanism that makes a mid-sequence reboot resumable.
func TestAgentSelfSequenceCursor(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	inv := model.NewInventoryRepo(db)
	sequences := model.NewSequenceRepo(db)
	tasks := model.NewTaskRepo(db)
	images := model.NewImageRepo(db)
	repos := Repos{
		ISOs:      model.NewISORepo(db),
		Unattend:  model.NewUnattendRepo(db),
		Software:  model.NewSoftwarePackageRepo(db),
		Images:    images,
		Inventory: inv,
		Bulk:      model.NewBulkRepo(db, inv),
		Sequences: sequences,
		Tasks:     tasks,
		Resolver: resolve.New(images, model.NewISORepo(db), model.NewUnattendRepo(db)).
			WithSequences(sequences, tasks),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// A 3-step sequence linked to an image the machine is bound to.
	seq, err := sequences.Create(ctx, model.Sequence{
		Name: "deploy",
		Steps: []model.SequenceStep{
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinDomainJoin, OrderValue: 10},
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinReboot, OrderValue: 20},
			{Kind: model.SeqStepBuiltin, BuiltinAction: model.BuiltinSoftware, OrderValue: 30},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Create(ctx, model.Image{Name: "img", SequenceID: &seq.ID})
	if err != nil {
		t.Fatal(err)
	}
	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "seq-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	dep, err := inv.RecordDeployment(ctx, m.ID, &img.ID)
	if err != nil {
		t.Fatal(err)
	}

	poll := func() AgentSelfResponse {
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

	// First poll: full sequence at cursor 0.
	out := poll()
	if out.DeploymentID != dep {
		t.Fatalf("deployment_id = %d, want %d", out.DeploymentID, dep)
	}
	if len(out.Sequence) != 3 || out.SeqCursor != 0 {
		t.Fatalf("first poll sequence=%d cursor=%d, want 3/0", len(out.Sequence), out.SeqCursor)
	}
	if out.Sequence[0].BuiltinAction != model.BuiltinDomainJoin {
		t.Errorf("first step = %q, want domainjoin", out.Sequence[0].BuiltinAction)
	}

	// Agent reports step 0 done → cursor advances to 1.
	body, _ := json.Marshal(AgentSequenceProgressRequest{DeploymentID: dep, CompletedIndex: 0, Status: "ok"})
	pr, err := http.Post(srv.URL+"/api/v1/agent/sequence-progress", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("sequence-progress status = %d, want 200", pr.StatusCode)
	}

	// Second poll: remaining two steps at cursor 1 (reboot, software).
	out = poll()
	if len(out.Sequence) != 2 || out.SeqCursor != 1 {
		t.Fatalf("second poll sequence=%d cursor=%d, want 2/1", len(out.Sequence), out.SeqCursor)
	}
	if out.Sequence[0].BuiltinAction != model.BuiltinReboot {
		t.Errorf("resumed step = %q, want reboot", out.Sequence[0].BuiltinAction)
	}
}
