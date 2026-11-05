package payload

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestToolkitFilesOnlyForRunningAgent(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, _ := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	blobs, _ := storage.NewBlobStore(t.TempDir())
	inv := model.NewInventoryRepo(db)
	kits := model.NewToolkitRepo(db, bx)
	bulk := model.NewBulkRepo(db, inv)
	svc := &Service{Blobs: blobs, Inventory: inv, Toolkits: kits}
	mux := http.NewServeMux()
	svc.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	kit, _ := kits.Create(ctx, model.Toolkit{Name: "kit", StepsJSON: `[{"type":"cmd","script_body":"x"}]`}, "")
	kid := strconv.FormatInt(int64(kit.ID), 10)
	_, _ = blobs.WriteStream("toolkit/"+kid+"/files/secret.ps1", strings.NewReader("SECRET-SCRIPT"))
	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-file-1"})
	other, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-file-2"})

	get := func(agentID string) (int, string) {
		req, _ := http.NewRequest("GET", srv.URL+"/payload/toolkit/"+kid+"/files/secret.ps1", nil)
		if agentID != "" {
			req.Header.Set("X-AutoDeploy-Agent-ID", agentID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if c, _ := get(""); c != http.StatusForbidden {
		t.Fatalf("anonymous: %d", c)
	}
	_, _, err = bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionToolkit, Payload: `{"toolkit_id":` + kid + `}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := get(m.AgentID); c != http.StatusForbidden {
		t.Fatalf("queued, not yet running: %d", c)
	}
	jobs, _ := bulk.ClaimJobsFor(ctx, m.ID, 0)
	if c, _ := get(other.AgentID); c != http.StatusForbidden {
		t.Fatalf("another machine's agent: %d", c)
	}
	if c, body := get(m.AgentID); c != http.StatusOK || body != "SECRET-SCRIPT" {
		t.Fatalf("running agent: %d %q", c, body)
	}
	_, _ = bulk.CompleteJob(ctx, jobs[0].ID, "ok", "{}")
	if c, _ := get(m.AgentID); c != http.StatusForbidden {
		t.Fatalf("after the job finished: %d", c)
	}
}
