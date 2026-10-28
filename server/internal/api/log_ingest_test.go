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

func logTestServer(t *testing.T) (*httptest.Server, Repos) {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	isos, una, imgs := model.NewISORepo(db), model.NewUnattendRepo(db), model.NewImageRepo(db)
	repos := Repos{
		ISOs: isos, Unattend: una, Images: imgs, Inventory: model.NewInventoryRepo(db),
		Resolver: resolve.New(imgs, isos, una), Logs: model.NewLogRepo(db),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func postIngest(t *testing.T, url string, header string, events []map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"events": events})
	req, _ := http.NewRequest(http.MethodPost, url+"/api/v1/logs/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if header != "" {
		req.Header.Set(machineUUIDHeader, header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ingest = %d", resp.StatusCode)
	}
}

// Old clients (no header, no deployment_id, Go errors shipped as {}) are
// accepted unchanged and attributed through their actor; new clients are
// attributed through the header and tagged with their deployment. Columns a
// client tries to set itself (user, machine) are ignored.
func TestLogIngestDerivesContext(t *testing.T) {
	ctx := context.Background()
	srv, r := logTestServer(t)
	m, _ := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "UUID-OLD"})
	n, _ := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "UUID-NEW"})

	postIngest(t, srv.URL, "", []map[string]any{
		{"component": "agent", "level": "ERROR", "actor": "uuid-old", "action": "package.install.fail",
			"target": "Chrome", "fields": `{"error":{}}`},
		{"component": "agent", "level": "INFO", "actor": "nobody", "action": "agent.start", "fields": "{}",
			"user": "admin", "machine_id": 999},
	})
	postIngest(t, srv.URL, "UUID-NEW", []map[string]any{
		{"component": "agent", "level": "ERROR", "actor": "something-else", "action": "package.install.fail",
			"target": "7-Zip", "fields": `{"exit_code":1618,"deployment_id":42,"error":"exit status 1618"}`},
	})
	evs, err := r.Logs.Search(ctx, model.LogSearch{ByID: true})
	if err != nil || len(evs) != 3 {
		t.Fatalf("search: %v %d", err, len(evs))
	}
	newEv, stray, oldEv := evs[0], evs[1], evs[2]
	if oldEv.MachineID != m.ID || oldEv.Message != "Chrome failed to install" || oldEv.Outcome != "failed" || oldEv.Error != "" {
		t.Errorf("old-style row: %+v", oldEv)
	}
	if stray.MachineID != 0 || stray.User != "" {
		t.Errorf("client-set columns must be ignored: %+v", stray)
	}
	if newEv.MachineID != n.ID || newEv.DeploymentID != 42 || newEv.Error != "exit status 1618" ||
		newEv.Message != "7-Zip failed to install — exit code 1618 (another installation is already in progress)" || newEv.Hint == "" {
		t.Errorf("new-style row: %+v", newEv)
	}
}

// The live tail's query: the JSON API accepts the Logs page filters and
// after=<id>, and fills message/category for stored rows.
func TestLogSearchAPIFilters(t *testing.T) {
	ctx := context.Background()
	_, r := logTestServer(t)
	_ = r.Logs.AppendBatch(ctx, []model.LogEvent{
		{Component: "agent", Level: "INFO", Action: "kbscan.reported", Fields: `{"count":3}`},
		{Component: "agent", Level: "ERROR", Action: "domainjoin.fail", Fields: `{"domain":"corp"}`},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs?level=ERROR&after=0", nil)
	q := model.ParseLogQuery(req.Context(), req.URL.Query(), r.Inventory, nil)
	evs, _ := r.Logs.Search(ctx, q.Search)
	if len(evs) != 1 || evs[0].Action != "domainjoin.fail" {
		t.Fatalf("level filter: %+v", evs)
	}
	q = model.ParseLogQuery(ctx, map[string][]string{"after": {"1"}}, r.Inventory, nil)
	evs, _ = r.Logs.Search(ctx, q.Search)
	if len(evs) != 1 || evs[0].ID != 2 {
		t.Fatalf("after cursor: %+v", evs)
	}
	model.NewNameLookup(ctx, r.Inventory).DescribeLogs(evs)
	if evs[0].Message != "Couldn't join the domain corp" || evs[0].Category != model.LogCatDomain {
		t.Errorf("describe: %+v", evs[0])
	}
}

// Deploy-status milestones write log rows tagged with the machine and run.
func TestDeployStatusWritesLogRows(t *testing.T) {
	ctx := context.Background()
	srv, r := logTestServer(t)
	post := func(status, notes string) {
		body, _ := json.Marshal(map[string]any{"identity": map[string]any{"system_uuid": "DS-1"}, "status": status, "notes": notes})
		resp, err := http.Post(srv.URL+"/api/v1/clients/deploy-status", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	post("staging", "")
	post("failed", "Couldn't prepare the disk")
	evs, _ := r.Logs.Search(ctx, model.LogSearch{ByID: true})
	if len(evs) != 2 || evs[0].Action != "deploy.failed" || evs[1].Action != "deploy.started" ||
		evs[0].DeploymentID == 0 || evs[0].DeploymentID != evs[1].DeploymentID || evs[0].MachineID == 0 {
		t.Fatalf("deploy rows: %+v", evs)
	}
	if evs[0].Message != "Deployment failed: Couldn't prepare the disk" {
		t.Errorf("message %q", evs[0].Message)
	}
}
