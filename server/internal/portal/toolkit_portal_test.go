package portal

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newToolkitPortal(t *testing.T) *testPortal {
	t.Helper()
	return newTestPortal(t, func(r *Repos, db *storage.DB, bx *secrets.Box) {
		r.Toolkits = model.NewToolkitRepo(db, bx)
	})
}

const tkSecret = "Sup3r-Secret-RunAs"

func TestToolkitPortalCreateEditAndList(t *testing.T) {
	tp := newToolkitPortal(t)
	ctx := context.Background()

	// A specific account without a password is refused.
	form := url.Values{
		"name": {"Printer map"}, "description": {"maps printers"},
		"run_as_mode": {"user"}, "run_as_user": {`CORP\printers`},
		"timeout_minutes": {"20"}, "reboot_policy": {"if_requested"},
		"param_index[]": {"0"}, "param_0_name": {"QUEUE"}, "param_0_label": {"Queue"},
		"param_0_default": {`\\ps01\q`}, "param_0_required": {"1"},
		"step_index[]": {"0", "1"},
		"step_0_type":  {"powershell"}, "step_0_ps_script_body": {"Add-Printer %QUEUE%"},
		"step_1_type": {"cmd"}, "step_1_cmd_script_body": {"echo hi"}, "step_1_run_as_system": {"1"},
	}
	rec := tp.do("POST", "/portal/toolkits", form, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Enter the password") {
		t.Fatalf("create without password: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), tkSecret) {
		t.Fatal("re-rendered form contains a password")
	}

	form.Set("run_as_password", tkSecret)
	rec = tp.do("POST", "/portal/toolkits", form, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	kits, _ := tp.r.Toolkits.List(ctx)
	if len(kits) != 1 {
		t.Fatalf("kits = %d", len(kits))
	}
	k := kits[0]
	if !k.HasPassword || k.TimeoutMinutes != 20 || k.RebootPolicy != model.RebootIfRequested || len(k.Params) != 1 || !k.Params[0].Required {
		t.Fatalf("stored kit wrong: %+v", k)
	}
	if !strings.Contains(k.StepsJSON, `"run_as_system":true`) || strings.Count(k.StepsJSON, "run_as_system") != 1 {
		t.Fatalf("per-step SYSTEM flag not stored right: %s", k.StepsJSON)
	}

	// The edit page never renders the password but says one is stored.
	page := tp.get("/portal/toolkits/" + tkID(k.ID) + "/edit")
	if strings.Contains(page, tkSecret) {
		t.Fatal("edit page leaked the password")
	}
	for _, want := range []string{"Password stored", "Add-Printer %QUEUE%", `name="step_1_run_as_system" value="1" checked`, "Recent runs"} {
		if !strings.Contains(page, want) {
			t.Errorf("edit page missing %q", want)
		}
	}

	// Saving without retyping keeps the password and bumps the revision.
	form.Del("run_as_password")
	form.Set("description", "updated")
	rec = tp.do("POST", "/portal/toolkits/"+tkID(k.ID), form, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	k2, _ := tp.r.Toolkits.Get(ctx, k.ID)
	if !k2.HasPassword || k2.Revision != 2 || k2.Description != "updated" {
		t.Fatalf("after update: %+v", k2)
	}
	// Changing the account demands its password.
	form.Set("run_as_user", `CORP\other`)
	rec = tp.do("POST", "/portal/toolkits/"+tkID(k.ID), form, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("account change without password: %d", rec.Code)
	}

	list := tp.get("/portal/toolkits")
	for _, want := range []string{"Printer map", `CORP\printers`, "/portal/bulk/new?toolkit_id=" + tkID(k.ID)} {
		if !strings.Contains(list, want) {
			t.Errorf("list missing %q", want)
		}
	}

	// Duplicate copies the kit (and its password).
	rec = tp.do("POST", "/portal/toolkits/"+tkID(k.ID)+"/duplicate", url.Values{}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("duplicate: %d", rec.Code)
	}
	kits, _ = tp.r.Toolkits.List(ctx)
	if len(kits) != 2 || !kits[1].HasPassword || kits[1].Name != "Printer map copy" {
		t.Fatalf("duplicate result: %+v", kits)
	}

	// Nav shows Tool Kits.
	if !strings.Contains(list, `href="/portal/toolkits"`) {
		t.Error("nav lacks Tool Kits")
	}
}

func TestToolkitFromSoftwarePackage(t *testing.T) {
	tp := newToolkitPortal(t)
	ctx := context.Background()
	pkg, err := tp.r.Software.Create(ctx, model.SoftwarePackage{
		Name: "7-Zip", DetectionJSON: `[{"type":"file","file_path":"C:\\x"}]`,
		StepsJSON: `[{"type":"msi","msi_path":"7z.msi"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tp.r.Blobs.WriteStream("software/"+tkID(pkg.ID)+"/files/7z.msi", strings.NewReader("MSI")); err != nil {
		t.Fatal(err)
	}
	rec := tp.do("POST", "/portal/toolkits/from-software", url.Values{"software_package_id": {tkID(pkg.ID)}}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("from-software: %d", rec.Code)
	}
	kits, _ := tp.r.Toolkits.List(ctx)
	if len(kits) != 1 || kits[0].RunAsMode != model.RunAsSystem || !strings.Contains(kits[0].StepsJSON, "7z.msi") {
		t.Fatalf("kit = %+v", kits)
	}
	entries, _ := tp.r.Blobs.ListTree("toolkit/" + tkID(kits[0].ID) + "/files")
	if len(entries) != 1 || entries[0].Name != "7z.msi" {
		t.Fatalf("files not copied: %+v", entries)
	}
}

func TestToolkitBulkReviewNeverCarriesPassword(t *testing.T) {
	tp := newToolkitPortal(t)
	ctx := context.Background()
	m, err := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-portal-1"})
	if err != nil {
		t.Fatal(err)
	}
	k, err := tp.r.Toolkits.Create(ctx, model.Toolkit{
		Name: "Fix", StepsJSON: `[{"type":"cmd","script_body":"echo %SITE%"}]`,
		Params: []model.ToolkitParam{{Name: "SITE", Default: "LON", Required: true}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	// The bulk form lists the kit (preselected from ?toolkit_id).
	page := tp.get("/portal/bulk/new?toolkit_id=" + tkID(k.ID))
	if !strings.Contains(page, `value="toolkit" checked`) || !strings.Contains(page, `name="toolkit_param_`+tkID(k.ID)+`_SITE" value="LON"`) {
		t.Fatal("bulk form doesn't preselect the kit with its parameter")
	}

	form := url.Values{
		"action": {"toolkit"}, "toolkit_id": {tkID(k.ID)},
		"toolkit_param_" + tkID(k.ID) + "_SITE": {"NYC"},
		"toolkit_run_as":                        {"user"}, "toolkit_run_as_user": {`CORP\ops`},
		"toolkit_run_as_password": {tkSecret},
		"toolkit_skip_succeeded":  {"1"},
		"machine_ids":             {tkID(m.ID)}, "schedule_kind": {"now"},
	}
	rec := tp.do("POST", "/portal/bulk/review", form, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	review := rec.Body.String()
	if strings.Contains(review, tkSecret) {
		t.Fatal("review page contains the Run As password")
	}
	for _, want := range []string{"Run Fix on 1 machine", `CORP\ops`, "SITE = NYC", "already succeeded are skipped"} {
		if !strings.Contains(review, want) {
			t.Errorf("review missing %q", want)
		}
	}
	tok := regexp.MustCompile(`name="toolkit_password_token" value="([0-9a-f]+)"`).FindStringSubmatch(review)
	if tok == nil {
		t.Fatal("review page carries no password token")
	}

	// "Queue operation" posts the hidden fields: token, no password.
	final := url.Values{}
	for _, mm := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)">`).FindAllStringSubmatch(review, -1) {
		final.Add(mm[1], htmlUnescape(mm[2]))
	}
	if final.Get("toolkit_run_as_password") != "" {
		t.Fatal("hidden fields carry the password")
	}
	rec = tp.do("POST", "/portal/bulk", final, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
	}
	ops, _ := tp.r.Bulk.ListOperations(ctx)
	if len(ops) != 1 || ops[0].Action != model.BulkActionToolkit {
		t.Fatalf("ops = %+v", ops)
	}
	op := ops[0]
	if strings.Contains(op.Payload, tkSecret) {
		t.Fatal("payload contains the password")
	}
	p, err := model.ParseToolkitPayload(op.Payload)
	if err != nil || p.Params["SITE"] != "NYC" || p.RunAsUser != `CORP\ops` || !p.SkipSucceeded || p.Revision != 1 {
		t.Fatalf("payload = %+v, %v", p, err)
	}
	user, pw, err := tp.r.Toolkits.RetrieveOperationCredential(ctx, op.ID)
	if err != nil || user != `CORP\ops` || pw != tkSecret {
		t.Fatalf("operation credential = %q / %v", user, err)
	}
	// The stash entry is consumed.
	if _, ok := reviewCreds.get(tok[1], "admin", `CORP\ops`); ok {
		t.Error("stash token still valid after queuing")
	}

	// Detail page shows the kit, account and parameters; never the password.
	detail := tp.get("/portal/bulk/" + tkID(op.ID))
	if strings.Contains(detail, tkSecret) {
		t.Fatal("detail page leaked the password")
	}
	for _, want := range []string{"Tool kit", `CORP\ops`, "SITE = NYC"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q", want)
		}
	}

	// A different account with no password is refused at review time.
	form.Del("toolkit_run_as_password")
	rec = tp.do("POST", "/portal/bulk/review", form, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `Enter the password for CORP\ops`) {
		t.Fatalf("missing password: %d", rec.Code)
	}
}

func TestToolkitMachineActionAndHistory(t *testing.T) {
	tp := newToolkitPortal(t)
	ctx := context.Background()
	m, _ := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "tk-portal-2"})
	k, _ := tp.r.Toolkits.Create(ctx, model.Toolkit{
		Name: "Diag", StepsJSON: `[{"type":"cmd","script_body":"ipconfig"}]`,
		RunAsMode: model.RunAsUser, RunAsUser: "svc",
	}, "kitpw")

	// Required: a kit choice.
	rec := tp.do("POST", "/portal/machines/"+tkID(m.ID)+"/action", url.Values{"action": {"toolkit"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose the tool kit to run") {
		t.Fatalf("no kit: %d", rec.Code)
	}

	// Kit default account: uses the kit's stored password, no override row.
	rec = tp.do("POST", "/portal/machines/"+tkID(m.ID)+"/action", url.Values{
		"action": {"toolkit"}, "toolkit_id": {tkID(k.ID)}, "toolkit_run_as": {"kit"},
	}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body.String())
	}
	ops, _ := tp.r.Bulk.ListOperations(ctx)
	if len(ops) != 1 {
		t.Fatalf("ops = %d", len(ops))
	}
	if _, ok, _ := tp.r.Toolkits.HasOperationCredential(ctx, ops[0].ID); ok {
		t.Fatal("kit-default run stored an override credential")
	}

	// The agent reports a result; the machine page shows it.
	jobs, _ := tp.r.Bulk.ClaimJobsFor(ctx, m.ID, 0)
	if _, err := tp.r.Bulk.CompleteJob(ctx, jobs[0].ID, "failed",
		`{"toolkit_id":`+tkID(k.ID)+`,"revision":1,"run_as":"user","run_as_user":"svc","steps":[{"step":1,"type":"cmd","exit_code":5,"error":"Access is denied","output":"IPCONFIG-OUTPUT"}]}`); err != nil {
		t.Fatal(err)
	}
	page := tp.get("/portal/machines/" + tkID(m.ID))
	for _, want := range []string{"Tool Kit runs", "Diag", "IPCONFIG-OUTPUT", "Access is denied", "Run tool kit"} {
		if !strings.Contains(page, want) {
			t.Errorf("machine page missing %q", want)
		}
	}
	if strings.Contains(page, "kitpw") {
		t.Fatal("machine page leaked a password")
	}

	// The kit can be deleted now nothing is pending.
	rec = tp.do("POST", "/portal/toolkits/"+tkID(k.ID)+"/delete", url.Values{}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := tp.r.Toolkits.Get(ctx, k.ID); err == nil {
		t.Fatal("kit not deleted")
	}
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&quot;", `"`, `&#92;`, `\`)
	return r.Replace(s)
}
func tkID(id model.ID) string { return strconv.FormatInt(int64(id), 10) }
