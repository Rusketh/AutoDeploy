package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newEnrollTestServer(t *testing.T) (*httptest.Server, Repos, *model.ImageRepo) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	repos := Repos{Inventory: inv}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos, model.NewImageRepo(db)
}

func postJSON(t *testing.T, url string, body any, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// Enrollment mints (or returns the existing) agent_id for a machine
// identified by SMBIOS identity — the bridge that lets a manually
// installed agent become a provisioned one.
func TestAgentEnroll(t *testing.T) {
	srv, repos, _ := newEnrollTestServer(t)

	var first struct {
		MachineID model.ID `json:"machine_id"`
		AgentID   string   `json:"agent_id"`
	}
	code := postJSON(t, srv.URL+"/api/v1/agent/enroll",
		map[string]any{"identity": map[string]any{"system_uuid": "manual-uuid"}}, &first)
	if code != http.StatusOK || first.MachineID == 0 || first.AgentID == "" {
		t.Fatalf("enroll: code=%d resp=%+v", code, first)
	}

	// Idempotent: the same machine gets the same identity back.
	var second struct {
		MachineID model.ID `json:"machine_id"`
		AgentID   string   `json:"agent_id"`
	}
	postJSON(t, srv.URL+"/api/v1/agent/enroll",
		map[string]any{"identity": map[string]any{"system_uuid": "manual-uuid"}}, &second)
	if second.MachineID != first.MachineID || second.AgentID != first.AgentID {
		t.Errorf("re-enroll changed identity: %+v vs %+v", first, second)
	}

	// The machine is in inventory.
	if _, err := repos.Inventory.GetByAgentID(context.Background(), first.AgentID); err != nil {
		t.Errorf("enrolled machine not found by agent_id: %v", err)
	}

	// Missing identity -> 400.
	if code := postJSON(t, srv.URL+"/api/v1/agent/enroll",
		map[string]any{"identity": map[string]any{}}, nil); code != http.StatusBadRequest {
		t.Errorf("empty identity code = %d, want 400", code)
	}
}

// The deploy report response must carry the agent_id (fallback source for
// agents talking to it before enrolling) and bind the deployed image to
// the machine when no operator-chosen image exists.
func TestAgentReportReturnsAgentIDAndBindsImage(t *testing.T) {
	srv, repos, images := newEnrollTestServer(t)
	ctx := context.Background()
	deployed, err := images.Create(ctx, model.Image{Name: "deployed"})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := images.Create(ctx, model.Image{Name: "operator-choice"})
	if err != nil {
		t.Fatal(err)
	}
	imageID := deployed.ID

	var open struct {
		MachineID    model.ID `json:"machine_id"`
		DeploymentID model.ID `json:"deployment_id"`
		AgentID      string   `json:"agent_id"`
	}
	code := postJSON(t, srv.URL+"/api/v1/agent/report", map[string]any{
		"identity": map[string]any{"system_uuid": "manual-uuid"},
		"image_id": imageID,
		"outcome":  "in_progress",
	}, &open)
	if code != http.StatusOK || open.AgentID == "" {
		t.Fatalf("report: code=%d resp=%+v", code, open)
	}

	b, err := repos.Inventory.GetBinding(ctx, open.MachineID)
	if err != nil || b.ImageID == nil || *b.ImageID != imageID {
		t.Fatalf("binding after report: %+v err=%v", b, err)
	}

	// An operator-chosen image is never overwritten by a later deploy
	// report.
	operatorImage := operator.ID
	b.ImageID = &operatorImage
	if err := repos.Inventory.UpsertBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	postJSON(t, srv.URL+"/api/v1/agent/report", map[string]any{
		"identity": map[string]any{"system_uuid": "manual-uuid"},
		"image_id": imageID,
		"outcome":  "in_progress",
	}, nil)
	b, _ = repos.Inventory.GetBinding(ctx, open.MachineID)
	if b.ImageID == nil || *b.ImageID != operatorImage {
		t.Errorf("operator image overwritten: %+v", b)
	}
}
