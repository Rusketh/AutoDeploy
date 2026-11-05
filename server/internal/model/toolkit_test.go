package model

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

type toolkitFixture struct {
	db   *storage.DB
	kits *ToolkitRepo
	inv  *InventoryRepo
	bulk *BulkRepo
}

func openToolkit(t *testing.T) toolkitFixture {
	t.Helper()
	db := openTestDB(t)
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	inv := NewInventoryRepo(db)
	return toolkitFixture{db: db, kits: NewToolkitRepo(db, bx), inv: inv, bulk: NewBulkRepo(db, inv)}
}

const psStep = `[{"type":"powershell","script_body":"Write-Output %SITE%"}]`

func TestToolkitCRUDAndRevision(t *testing.T) {
	ctx := context.Background()
	f := openToolkit(t)

	k, err := f.kits.Create(ctx, Toolkit{
		Name: "Printer setup", StepsJSON: psStep,
		Params:    []ToolkitParam{{Name: "SITE", Label: "Site code", Default: "LON", Required: true}},
		RunAsMode: RunAsUser, RunAsUser: `CORP\svc-printers`,
		TimeoutMinutes: 30, RebootPolicy: RebootIfRequested,
	}, "pa55word")
	if err != nil {
		t.Fatal(err)
	}
	if k.Revision != 1 || !k.HasPassword || k.RunAsUser != `CORP\svc-printers` || len(k.Params) != 1 {
		t.Fatalf("created kit wrong: %+v", k)
	}
	pw, err := f.kits.RetrievePassword(ctx, k.ID)
	if err != nil || pw != "pa55word" {
		t.Fatalf("RetrievePassword = %q, %v", pw, err)
	}

	// A save without a password keeps the stored one and bumps the revision.
	k.Description = "maps the site printers"
	if err := f.kits.Update(ctx, k, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := f.kits.Get(ctx, k.ID)
	if got.Revision != 2 || !got.HasPassword {
		t.Fatalf("after update: revision=%d has_password=%v", got.Revision, got.HasPassword)
	}
	if pw, _ := f.kits.RetrievePassword(ctx, k.ID); pw != "pa55word" {
		t.Fatalf("password not preserved, got %q", pw)
	}

	// Switching to SYSTEM drops the account and the stored password.
	got.RunAsMode = RunAsSystem
	if err := f.kits.Update(ctx, got, "ignored"); err != nil {
		t.Fatal(err)
	}
	got, _ = f.kits.Get(ctx, k.ID)
	if got.HasPassword || got.RunAsUser != "" || got.Revision != 3 {
		t.Fatalf("switch to SYSTEM kept credentials: %+v", got)
	}

	if err := f.kits.BumpRevision(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.kits.Get(ctx, k.ID); got.Revision != 4 {
		t.Fatalf("BumpRevision: revision=%d", got.Revision)
	}

	// Duplicate names collide.
	if _, err := f.kits.Create(ctx, Toolkit{Name: "Printer setup"}, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
}

func TestToolkitValidation(t *testing.T) {
	ctx := context.Background()
	f := openToolkit(t)
	cases := map[string]Toolkit{
		"bad step":        {Name: "a", StepsJSON: `[{"type":"nope"}]`},
		"user needs name": {Name: "b", RunAsMode: RunAsUser},
		"bad mode":        {Name: "c", RunAsMode: "root"},
		"bad reboot":      {Name: "d", RebootPolicy: "sometimes"},
		"bad timeout":     {Name: "e", TimeoutMinutes: MaxToolkitTimeoutMinutes + 1},
		"bad param name":  {Name: "f", Params: []ToolkitParam{{Name: "1abc"}}},
		"reserved param":  {Name: "g", Params: []ToolkitParam{{Name: "PkgDir"}}},
		"dup param":       {Name: "h", Params: []ToolkitParam{{Name: "X"}, {Name: "x"}}},
		"bad account":     {Name: "i", RunAsMode: RunAsUser, RunAsUser: "a/b"},
	}
	for name, k := range cases {
		if _, err := f.kits.Create(ctx, k, ""); !errors.Is(err, ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", name, err)
		}
	}
	for _, ok := range []string{`CORP\user`, `user@corp.example`, `.\localadmin`, `localadmin`} {
		if err := ValidateRunAsUser(ok); err != nil {
			t.Errorf("ValidateRunAsUser(%q) = %v", ok, err)
		}
	}
}

func TestToolkitDuplicateCopiesPassword(t *testing.T) {
	ctx := context.Background()
	f := openToolkit(t)
	k, err := f.kits.Create(ctx, Toolkit{Name: "src", StepsJSON: psStep, RunAsMode: RunAsUser, RunAsUser: "svc"}, "pw1")
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.kits.Duplicate(ctx, k.ID, "src copy")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == k.ID || d.Revision != 1 || d.RunAsUser != "svc" || !d.HasPassword {
		t.Fatalf("duplicate wrong: %+v", d)
	}
	if pw, _ := f.kits.RetrievePassword(ctx, d.ID); pw != "pw1" {
		t.Fatalf("duplicate password = %q", pw)
	}
}

func TestToolkitParamsAndExpansion(t *testing.T) {
	declared := []ToolkitParam{
		{Name: "SITE", Default: "LON", Required: true},
		{Name: "Queue", Default: ""},
	}
	vals, err := ResolveParams(declared, map[string]string{"site": "NYC", "unknown": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if vals["SITE"] != "NYC" || vals["Queue"] != "" || len(vals) != 2 {
		t.Fatalf("ResolveParams = %v", vals)
	}
	if _, err := ResolveParams(declared, map[string]string{"SITE": "  "}); !errors.Is(err, ErrValidation) {
		t.Fatalf("blank required param: %v", err)
	}

	steps := []swspec.InstallStep{{
		Type: "exe", ExePath: `%site%\setup.exe`, ExeArgs: []string{"/q=%QUEUE%", "%ProgramFiles%"},
		ScriptBody: "", Description: "for %SITE%",
	}, {Type: "powershell", ScriptBody: "echo %SITE% %pkgdir% $1"}}
	out := ExpandToolkitSteps(steps, map[string]string{"SITE": "N$1YC", "Queue": "Q1"})
	if out[0].ExePath != `N$1YC\setup.exe` || out[0].ExeArgs[0] != "/q=Q1" || out[0].ExeArgs[1] != "%ProgramFiles%" {
		t.Fatalf("exe expansion wrong: %+v", out[0])
	}
	if out[0].Description != "for N$1YC" {
		t.Fatalf("description: %q", out[0].Description)
	}
	if out[1].ScriptBody != "echo N$1YC %pkgdir% $1" {
		t.Fatalf("script body: %q", out[1].ScriptBody)
	}
	// A value containing another parameter's token is not re-expanded.
	cross := ExpandToolkitSteps([]swspec.InstallStep{{Type: "cmd", ScriptBody: "%A% %B%"}},
		map[string]string{"A": "%B%", "B": "b"})
	if cross[0].ScriptBody != "%B% b" {
		t.Fatalf("cross expansion: %q", cross[0].ScriptBody)
	}
	if steps[0].ExePath != `%site%\setup.exe` {
		t.Fatal("ExpandToolkitSteps mutated its input")
	}
}

func TestToolkitPayloadAndEffectiveRunAs(t *testing.T) {
	kit := Toolkit{RunAsMode: RunAsUser, RunAsUser: "svc"}
	cases := []struct {
		payload    string
		mode, user string
		wantErr    bool
	}{
		{`{"toolkit_id":1}`, RunAsUser, "svc", false},
		{`{"toolkit_id":1,"run_as":"kit"}`, RunAsUser, "svc", false},
		{`{"toolkit_id":1,"run_as":"system"}`, RunAsSystem, "", false},
		{`{"toolkit_id":1,"run_as":"logged_on_user","run_as_user":"x"}`, RunAsLoggedOnUser, "", false},
		{`{"toolkit_id":1,"run_as":"user","run_as_user":"CORP\\other"}`, RunAsUser, `CORP\other`, false},
		{`{"toolkit_id":1,"run_as":"user"}`, "", "", true},
		{`{"toolkit_id":0}`, "", "", true},
		{`{"toolkit_id":1,"run_as":"admin"}`, "", "", true},
	}
	for _, c := range cases {
		p, err := ParseToolkitPayload(c.payload)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v", c.payload, err)
			continue
		}
		if err != nil {
			continue
		}
		mode, user := EffectiveRunAs(kit, p)
		if mode != c.mode || user != c.user {
			t.Errorf("%s: EffectiveRunAs = %s/%s, want %s/%s", c.payload, mode, user, c.mode, c.user)
		}
	}
}

func TestToolkitBulkOperationLifecycle(t *testing.T) {
	ctx := context.Background()
	f := openToolkit(t)
	m1 := seedMachine(t, f.inv, "u1", "LAB-01", "")
	m2 := seedMachine(t, f.inv, "u2", "LAB-02", "")
	k, err := f.kits.Create(ctx, Toolkit{Name: "kit", StepsJSON: psStep}, "")
	if err != nil {
		t.Fatal(err)
	}

	// Unknown kit is refused.
	if _, _, err := f.bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionToolkit, Payload: `{"toolkit_id":999}`,
		Target: BulkTarget{MachineIDs: []ID{m1}},
	}); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown kit: %v", err)
	}

	payload := `{"toolkit_id":` + kid(k.ID) + `,"run_as":"user","run_as_user":"CORP\\ops","skip_succeeded":true}`
	op, jobs, err := f.bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionToolkit, Payload: payload,
		Target: BulkTarget{MachineIDs: []ID{m1, m2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(jobs))
	}
	if err := f.kits.SetOperationCredential(ctx, op.ID, `CORP\ops`, "override-pw"); err != nil {
		t.Fatal(err)
	}
	if u, ok, _ := f.kits.HasOperationCredential(ctx, op.ID); !ok || u != `CORP\ops` {
		t.Fatalf("HasOperationCredential = %q %v", u, ok)
	}

	// The kit can't be deleted while the operation is live.
	if err := f.kits.Delete(ctx, k.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete while in use: %v", err)
	}
	if ops, n, _ := f.kits.PendingUse(ctx, k.ID); ops != 1 || n != 2 {
		t.Fatalf("PendingUse = %d ops %d jobs", ops, n)
	}

	// m1 succeeds, m2 fails; the last completion finishes the op and purges
	// the override password.
	claimed, _ := f.bulk.ClaimJobsFor(ctx, m1, 0)
	if _, err := f.bulk.CompleteJob(ctx, claimed[0].ID, "ok", `{"toolkit_id":`+kid(k.ID)+`,"revision":1,"run_as":"user","steps":[{"step":1,"type":"powershell","exit_code":0,"output":"LON"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.kits.HasOperationCredential(ctx, op.ID); !ok {
		t.Fatal("credential purged before the operation finished")
	}
	claimed, _ = f.bulk.ClaimJobsFor(ctx, m2, 0)
	done, err := f.bulk.CompleteJob(ctx, claimed[0].ID, "failed", `{"error":"boom"}`)
	if err != nil || done != op.ID {
		t.Fatalf("CompleteJob finished op = %d, %v", done, err)
	}
	if _, ok, _ := f.kits.HasOperationCredential(ctx, op.ID); ok {
		t.Fatal("credential survived operation completion")
	}

	// A second skip-succeeded run only targets the machine that failed.
	_, jobs, err = f.bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionToolkit, Payload: payload,
		Target: BulkTarget{MachineIDs: []ID{m1, m2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].MachineID != m2 {
		t.Fatalf("skip_succeeded: jobs = %+v", jobs)
	}

	// History.
	runs, err := f.kits.RunsForMachine(ctx, m1, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("RunsForMachine = %d, %v", len(runs), err)
	}
	if !runs[0].HasResult || runs[0].Result.Steps[0].Output != "LON" || runs[0].Payload.RunAsUser != `CORP\ops` {
		t.Fatalf("run history wrong: %+v", runs[0])
	}
	all, _ := f.kits.RunsForToolkit(ctx, k.ID, 10)
	if len(all) != 3 {
		t.Fatalf("RunsForToolkit = %d", len(all))
	}
	stats, err := f.kits.AllRunStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s := stats[k.ID]; s.OK != 1 || s.Failed != 1 || s.Pending != 1 || s.LastRun == nil {
		t.Fatalf("stats = %+v", s)
	}
}

func TestToolkitCredentialPurgeOnCancel(t *testing.T) {
	ctx := context.Background()
	f := openToolkit(t)
	m1 := seedMachine(t, f.inv, "u1", "LAB-01", "")
	k, _ := f.kits.Create(ctx, Toolkit{Name: "kit", StepsJSON: psStep}, "")
	op, _, err := f.bulk.CreateOperation(ctx, BulkOperation{
		Action: BulkActionToolkit, Payload: `{"toolkit_id":` + kid(k.ID) + `}`,
		Target: BulkTarget{MachineIDs: []ID{m1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.kits.SetOperationCredential(ctx, op.ID, "svc", "pw"); err != nil {
		t.Fatal(err)
	}
	if err := f.bulk.CancelOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := f.kits.HasOperationCredential(ctx, op.ID); ok {
		t.Fatal("credential survived cancellation")
	}
	// The backstop sweep is harmless when nothing is left.
	if n, err := PurgeFinishedCredentials(ctx, f.db); err != nil || n != 0 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	// Delete is allowed once nothing is pending.
	if err := f.kits.Delete(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
}

func TestToolkitResultParse(t *testing.T) {
	if _, ok := ParseToolkitResult(`{}`); ok {
		t.Fatal("empty result reported ok")
	}
	if _, ok := ParseToolkitResult(`{"cancelled":"x"}`); ok {
		t.Fatal("foreign result reported ok")
	}
	r, ok := ParseToolkitResult(`{"toolkit_id":3,"timed_out":true}`)
	if !ok || !r.TimedOut {
		t.Fatalf("parse: %+v %v", r, ok)
	}
	if !strings.Contains(RunAsLabel(RunAsLoggedOnUser), "Logged") || RebootLabel("") != "Never" {
		t.Fatal("labels")
	}
}

func kid(id ID) string { return strconv.FormatInt(int64(id), 10) }
