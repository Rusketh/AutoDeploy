package api

import (
	"context"
	"encoding/json"
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

type toolkitAPIFixture struct {
	srv   *httptest.Server
	inv   *model.InventoryRepo
	bulk  *model.BulkRepo
	kits  *model.ToolkitRepo
	logs  *model.LogRepo
	blobs *storage.BlobStore
	db    *storage.DB
}

func newToolkitAPIFixture(t *testing.T) toolkitAPIFixture {
	t.Helper()
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
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inv := model.NewInventoryRepo(db)
	f := toolkitAPIFixture{
		inv: inv, bulk: model.NewBulkRepo(db, inv), kits: model.NewToolkitRepo(db, bx),
		logs: model.NewLogRepo(db), blobs: blobs, db: db,
	}
	mux := http.NewServeMux()
	Register(mux, Repos{Inventory: inv, Bulk: f.bulk, Toolkits: f.kits, Logs: f.logs, Blobs: blobs})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f toolkitAPIFixture) post(t *testing.T, path, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(f.srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func jobBody(agentID string, jobID model.ID) string {
	return `{"agent_id":"` + agentID + `","job_id":` + strconv.FormatInt(int64(jobID), 10) + `}`
}

func TestAgentToolkitJobAndCredential(t *testing.T) {
	ctx := context.Background()
	f := newToolkitAPIFixture(t)

	m, err := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-other"})
	if err != nil {
		t.Fatal(err)
	}
	kit, err := f.kits.Create(ctx, model.Toolkit{
		Name:      "Map printers",
		StepsJSON: `[{"type":"powershell","script_body":"Add-Printer %QUEUE%"},{"type":"copy","source_path":"cfg.xml","destination_path":"%APPDATA%\\x","run_as_system":true}]`,
		Params:    []model.ToolkitParam{{Name: "QUEUE", Default: "\\\\ps01\\default", Required: true}},
		RunAsMode: model.RunAsUser, RunAsUser: `CORP\printers`,
		TimeoutMinutes: 15, RebootPolicy: model.RebootIfRequested,
	}, "kit-secret-pw")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.blobs.WriteStream("toolkit/"+strconv.FormatInt(int64(kit.ID), 10)+"/files/cfg.xml", strings.NewReader("<x/>")); err != nil {
		t.Fatal(err)
	}

	op, jobs, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
		Action:  model.BulkActionToolkit,
		Payload: `{"toolkit_id":` + strconv.FormatInt(int64(kit.ID), 10) + `,"revision":1,"params":{"queue":"\\\\ps02\\floor3"}}`,
		Target:  model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := jobs[0]

	// Queued (not yet claimed) -> refused.
	if code, _ := f.post(t, "/api/v1/agent/toolkit-job", jobBody(m.AgentID, job.ID)); code != http.StatusConflict {
		t.Fatalf("queued job: status %d, want 409", code)
	}
	if _, err := f.bulk.ClaimJobsFor(ctx, m.ID, 0); err != nil {
		t.Fatal(err)
	}

	// Wrong agent -> forbidden; unknown agent -> forbidden.
	if code, _ := f.post(t, "/api/v1/agent/toolkit-job", jobBody(other.AgentID, job.ID)); code != http.StatusForbidden {
		t.Fatalf("foreign agent: status %d", code)
	}
	if code, _ := f.post(t, "/api/v1/agent/job-credential", jobBody("not-an-agent", job.ID)); code != http.StatusForbidden {
		t.Fatalf("unknown agent: status %d", code)
	}
	if code, _ := f.post(t, "/api/v1/agent/toolkit-job", `{"agent_id":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("missing job id: status %d", code)
	}

	// The definition: parameters expanded, files listed, no password.
	code, body := f.post(t, "/api/v1/agent/toolkit-job", jobBody(m.AgentID, job.ID))
	if code != http.StatusOK {
		t.Fatalf("toolkit-job: %d %s", code, body)
	}
	if strings.Contains(string(body), "kit-secret-pw") {
		t.Fatal("toolkit-job response leaked the password")
	}
	var def AgentToolkitJobResponse
	if err := json.Unmarshal(body, &def); err != nil {
		t.Fatal(err)
	}
	if def.RunAs != model.RunAsUser || def.RunAsUser != `CORP\printers` || !def.NeedsPassword {
		t.Fatalf("run-as wrong: %+v", def)
	}
	if def.TimeoutMinutes != 15 || def.RebootPolicy != model.RebootIfRequested || def.Revision != 1 {
		t.Fatalf("settings wrong: %+v", def)
	}
	if got := def.Item.InstallSteps[0].ScriptBody; got != `Add-Printer \\ps02\floor3` {
		t.Fatalf("param not expanded: %q", got)
	}
	if !def.Item.InstallSteps[1].RunAsSystem {
		t.Fatal("run_as_system dropped")
	}
	if len(def.Item.Files) != 1 || def.Item.Files[0].Name != "cfg.xml" ||
		!strings.HasPrefix(def.Item.Files[0].URL, "/payload/toolkit/") {
		t.Fatalf("files: %+v", def.Item.Files)
	}

	// Credential: the kit's own password, audit-logged without the value.
	code, body = f.post(t, "/api/v1/agent/job-credential", jobBody(m.AgentID, job.ID))
	if code != http.StatusOK {
		t.Fatalf("credential: %d %s", code, body)
	}
	var cred AgentJobCredentialResponse
	_ = json.Unmarshal(body, &cred)
	if cred.User != `CORP\printers` || cred.Password != "kit-secret-pw" {
		t.Fatalf("credential = %+v", cred)
	}
	var n int
	var fields, target string
	if err := f.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(fields), MAX(target) FROM log_event WHERE action='secret.access'`).Scan(&n, &fields, &target); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(fields, "printers") || target != "toolkit:"+strconv.FormatInt(int64(kit.ID), 10) {
		t.Fatalf("want 1 secret.access event for the kit, got %d (%s %s)", n, target, fields)
	}
	if strings.Contains(fields, "kit-secret-pw") {
		t.Fatal("audit event contains the password")
	}

	// Once the job finishes, nothing more is released.
	if _, err := f.bulk.CompleteJob(ctx, job.ID, "ok", "{}"); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.post(t, "/api/v1/agent/job-credential", jobBody(m.AgentID, job.ID)); code != http.StatusConflict {
		t.Fatalf("finished job credential: status %d", code)
	}
	_ = op
}

func TestAgentJobCredentialOverrideAndModes(t *testing.T) {
	ctx := context.Background()
	f := newToolkitAPIFixture(t)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk2"})
	kit, err := f.kits.Create(ctx, model.Toolkit{
		Name: "kit", StepsJSON: `[{"type":"cmd","script_body":"whoami"}]`,
		RunAsMode: model.RunAsUser, RunAsUser: "svc",
	}, "kit-pw")
	if err != nil {
		t.Fatal(err)
	}
	kid := strconv.FormatInt(int64(kit.ID), 10)
	run := func(payload string) model.BulkJob {
		t.Helper()
		op, jobs, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
			Action: model.BulkActionToolkit, Payload: payload,
			Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = op
		if _, err := f.bulk.ClaimJobsFor(ctx, m.ID, 0); err != nil {
			t.Fatal(err)
		}
		return jobs[0]
	}

	// Override account with an operation password.
	j := run(`{"toolkit_id":` + kid + `,"run_as":"user","run_as_user":"CORP\\ops"}`)
	if err := f.kits.SetOperationCredential(ctx, j.OperationID, `CORP\ops`, "op-pw"); err != nil {
		t.Fatal(err)
	}
	code, body := f.post(t, "/api/v1/agent/job-credential", jobBody(m.AgentID, j.ID))
	var cred AgentJobCredentialResponse
	_ = json.Unmarshal(body, &cred)
	if code != http.StatusOK || cred.Password != "op-pw" || cred.User != `CORP\ops` {
		t.Fatalf("override credential: %d %+v", code, cred)
	}
	_, _ = f.bulk.CompleteJob(ctx, j.ID, "ok", "{}")

	// Override to a different account with NO stored password -> 404, and the
	// kit's password is never handed out for a different account.
	j = run(`{"toolkit_id":` + kid + `,"run_as":"user","run_as_user":"CORP\\someone"}`)
	code, body = f.post(t, "/api/v1/agent/job-credential", jobBody(m.AgentID, j.ID))
	if code != http.StatusNotFound || strings.Contains(string(body), "kit-pw") {
		t.Fatalf("missing override: %d %s", code, body)
	}
	_, _ = f.bulk.CompleteJob(ctx, j.ID, "failed", "{}")

	// SYSTEM override: definition says so and the credential is refused.
	j = run(`{"toolkit_id":` + kid + `,"run_as":"system"}`)
	_, body = f.post(t, "/api/v1/agent/toolkit-job", jobBody(m.AgentID, j.ID))
	var def AgentToolkitJobResponse
	_ = json.Unmarshal(body, &def)
	if def.RunAs != model.RunAsSystem || def.NeedsPassword {
		t.Fatalf("system override: %+v", def)
	}
	if code, _ := f.post(t, "/api/v1/agent/job-credential", jobBody(m.AgentID, j.ID)); code != http.StatusConflict {
		t.Fatalf("system credential: status %d", code)
	}
}
