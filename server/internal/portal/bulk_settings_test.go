package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/config"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/runtime"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ---- U8: honest bulk status labels ----

func TestBulkStatusLabels(t *testing.T) {
	op := func(status, kind string, p model.BulkProgress) model.BulkOperation {
		p.Total = p.Queued + p.Running + p.OK + p.Failed + p.Cancelled
		return model.BulkOperation{Status: status, ScheduleKind: kind, Progress: p}
	}
	cases := []struct {
		name      string
		op        model.BulkOperation
		label     string
		tone      string
		noteMatch string
	}{
		{"all ok", op("completed", "now", model.BulkProgress{OK: 6}), "Completed", "ok", ""},
		{"some failed", op("completed", "now", model.BulkProgress{OK: 12, Failed: 3}), "Completed with 3 failures", "warn", ""},
		{"one failed", op("completed", "now", model.BulkProgress{OK: 1, Failed: 1}), "Completed with 1 failure", "bad", ""},
		{"all failed", op("completed", "now", model.BulkProgress{Failed: 4}), "Failed on all 4 machines", "bad", ""},
		{"expired jobs", op("completed", "now", model.BulkProgress{OK: 2, Cancelled: 1}), "Completed · 1 not delivered", "warn", "check in"},
		{"running", op("active", "now", model.BulkProgress{OK: 4, Failed: 1, Running: 3, Queued: 7}), "Running", "accent", "1 failed so far"},
		{"scheduled", op("scheduled", "once", model.BulkProgress{}), "Scheduled", "", ""},
		{"cancelled", op("cancelled", "now", model.BulkProgress{OK: 3, Cancelled: 5}), "Cancelled", "muted", "3 of 8"},
		{"paused", op("paused", "recurring", model.BulkProgress{OK: 4}), "Paused", "muted", ""},
		{"recurring between runs", op("active", "recurring", model.BulkProgress{OK: 3}), "Recurring", "accent", "Last run: completed"},
		{"recurring run in flight", op("active", "recurring", model.BulkProgress{Queued: 3}), "Running", "accent", ""},
		{"nothing matched", op("completed", "now", model.BulkProgress{}), "Completed", "muted", "No machines"},
	}
	for _, c := range cases {
		v := bulkStatus(c.op)
		if v.Label != c.label || v.Tone != c.tone || !strings.Contains(v.Note, c.noteMatch) {
			t.Errorf("%s: got %+v, want label %q tone %q note ~%q", c.name, v, c.label, c.tone, c.noteMatch)
		}
		if v.Label == "Completed" && (c.op.Progress.Failed > 0 || c.op.Progress.Cancelled > 0) {
			t.Errorf("%s: plain Completed despite failures", c.name)
		}
		if v.Label == "Running" && v.Tone == "warn" {
			t.Errorf("%s: Running must not be warning-styled", c.name)
		}
	}
}

func TestSegmentedProgressAndWords(t *testing.T) {
	sp := bulkProgressBar(model.BulkProgress{Total: 4, OK: 2, Failed: 1, Queued: 1})
	if len(sp.Segs) != 5 || sp.Segs[0].Pct != 50 || sp.Segs[1].Pct != 25 {
		t.Errorf("segments = %+v", sp.Segs)
	}
	if sp.Summary != "4 machines: 2 succeeded, 1 failed, 1 queued" || sp.Short != "2 succeeded · 1 failed · 1 queued" {
		t.Errorf("summary = %q / %q", sp.Summary, sp.Short)
	}
	london, _ := time.LoadLocation("Europe/London")
	for spec, want := range map[string]string{
		`{"every":1,"unit":"day","at":"02:00"}`:              "every day at 02:00 (Europe/London)",
		`{"every":2,"unit":"week","at":"06:30","weekday":1}`: "every 2 weeks on Monday at 06:30 (Europe/London)",
		`{"every":1,"unit":"week","at":"02:00","weekday":6}`: "every Saturday at 02:00 (Europe/London)",
		`{"every":3,"unit":"hour"}`:                          "every 3 hours",
		`{"cron":"0 2 * * 1"}`:                               "on a custom schedule: cron “0 2 * * 1” (Europe/London)",
	} {
		if got := recurText(spec, london); got != want {
			t.Errorf("recurText(%s) = %q, want %q", spec, got, want)
		}
	}
	for raw, want := range map[string]string{
		`{"error":"exit code 1603: installer failed"}`: "exit code 1603: installer failed",
		`{"exit_code":0}`:       "Exit code 0",
		`{"exit_code":2359302}`: "Exit code 2359302 (0x00240006)",
		``:                      "",
		`not json`:              "not json",
	} {
		if got := jobResultText(raw); got != want {
			t.Errorf("jobResultText(%q) = %q, want %q", raw, got, want)
		}
	}
	if model.BulkActionLabel("software_push") != "Install software" {
		t.Error("software_push must read as Install software")
	}
}

// ---- F5: review step, and the final POST with and without it ----

func seedMachines(t *testing.T, tp *testPortal, n int) []model.ID {
	t.Helper()
	ctx := context.Background()
	var ids []model.ID
	for i := 0; i < n; i++ {
		m, err := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{
			SystemUUID:   fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i+1),
			SystemSerial: fmt.Sprintf("SN-%02d", i+1), SystemManufacturer: "Dell Inc.",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func bulkForm(ids []model.ID, kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	for _, id := range ids {
		v.Add("machine_ids", idStr(id))
	}
	return v
}

func bulkCount(t *testing.T, tp *testPortal) int {
	t.Helper()
	ops, err := tp.r.Bulk.ListOperations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(ops)
}

func TestBulkReviewStep(t *testing.T) {
	tp := newTestPortal(t)
	ids := seedMachines(t, tp, 25)

	// The form posts to the review step, not straight to create.
	if out := tp.get("/portal/bulk/new"); !strings.Contains(out, `action="/portal/bulk/review"`) {
		t.Error("bulk form must post to the review step")
	}
	// No default action is pre-selected.
	if out := tp.get("/portal/bulk/new"); strings.Contains(out, `value="rename" checked`) {
		t.Error("no action should be pre-selected")
	}

	// Review a script run: summary in words, names, nothing queued yet.
	form := bulkForm(ids[:3], "action", "script", "script_shell", "powershell", "script_body", "Get-Service",
		"schedule_kind", "once", "run_at", "2030-01-02T03:04", "wake_on_lan", "1",
		"cancel_after_value", "4", "cancel_after_unit", "hours", "name", "Svc check")
	rec := tp.do("POST", "/portal/bulk/review", form, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("review = %d\n%s", rec.Code, body)
	}
	for _, want := range []string{"Run a script on 3 machines", "Once, at Wed 2 Jan 2030 03:04 (UTC)",
		"PowerShell script", "Get-Service", "4 hours", "Wake-on-LAN: <strong>on</strong>",
		`name="script_body" value="Get-Service"`, `name="reviewed" value="1"`, `action="/portal/bulk"`, "Back to edit"} {
		if !strings.Contains(body, want) {
			t.Errorf("review missing %q", want)
		}
	}
	if strings.Contains(body, "confirm_count") {
		t.Error("a non-destructive action must not ask for the count")
	}
	if bulkCount(t, tp) != 0 {
		t.Fatal("the review step must not queue anything")
	}

	// Back to edit: the form again with every value kept.
	back := bulkForm(ids[:3], "action", "script", "script_body", "Get-Service", "schedule_kind", "once",
		"run_at", "2030-01-02T03:04", "name", "Svc check", "review_back", "1")
	rec = tp.do("POST", "/portal/bulk/review", back, nil)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `value="Svc check"`) || !strings.Contains(body, "Get-Service") ||
		!strings.Contains(body, `value="2030-01-02T03:04"`) || !strings.Contains(body, `value="script" checked`) {
		t.Errorf("back to edit lost values (%d)", rec.Code)
	}

	// Review a re-image of 25: red, count to type, names first 20 + "and 5 more".
	ri := bulkForm(ids, "action", "reimage", "schedule_kind", "now")
	rec = tp.do("POST", "/portal/bulk/review", ri, nil)
	body = rec.Body.String()
	for _, want := range []string{"Re-image 25 machines, starting now", `review-card danger`, `data-confirm-count="25"`, "and 5 more"} {
		if !strings.Contains(body, want) {
			t.Errorf("reimage review missing %q", want)
		}
	}

	// Final submit from the review page with the wrong count: refused, 422.
	final := bulkForm(ids, "action", "reimage", "schedule_kind", "now", "reviewed", "1", "confirm_count", "24")
	rec = tp.do("POST", "/portal/bulk", final, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Type 25 to confirm") {
		t.Errorf("wrong count = %d", rec.Code)
	}
	if bulkCount(t, tp) != 0 {
		t.Fatal("a wrong count must queue nothing")
	}
	// The right count queues it.
	final.Set("confirm_count", "25")
	if rec = tp.do("POST", "/portal/bulk", final, nil); rec.Code != http.StatusFound {
		t.Fatalf("confirmed reimage = %d\n%s", rec.Code, rec.Body.String())
	}

	// Scripted / older callers post straight to /portal/bulk without any
	// review fields — still accepted, re-image included.
	old := bulkForm(ids[:2], "action", "reimage", "schedule_kind", "now")
	if rec = tp.do("POST", "/portal/bulk", old, nil); rec.Code != http.StatusFound {
		t.Fatalf("direct POST without review = %d\n%s", rec.Code, rec.Body.String())
	}
	if bulkCount(t, tp) != 2 {
		t.Errorf("want 2 operations, got %d", bulkCount(t, tp))
	}
}

func TestBulkFormInlineErrors(t *testing.T) {
	tp := newTestPortal(t)
	ids := seedMachines(t, tp, 2)
	for _, c := range []struct {
		form  url.Values
		field string
		msg   string
	}{
		{bulkForm(ids, "schedule_kind", "now"), "action", "Choose what to do"},
		{bulkForm(ids, "action", "rename", "schedule_kind", "now"), "rename_find", "part of the name to find"},
		{bulkForm(nil, "action", "script", "script_body", "x", "schedule_kind", "now"), "machine_ids", "at least one machine"},
		{bulkForm(ids, "action", "script", "script_body", "x", "schedule_kind", "once"), "run_at", "date and time"},
		{bulkForm(ids, "action", "script", "script_body", "x", "cancel_after_value", "soon"), "cancel_after_value", "whole number"},
	} {
		for _, target := range []string{"/portal/bulk", "/portal/bulk/review"} {
			rec := tp.do("POST", target, c.form, nil)
			body := rec.Body.String()
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `id="err-`+c.field+`"`) || !strings.Contains(body, c.msg) {
				t.Errorf("%s %s: status %d, want inline error on %s", target, c.form.Encode(), rec.Code, c.field)
			}
			if !strings.Contains(body, "data-form-error") {
				t.Errorf("%s: want the top-of-form error", target)
			}
		}
	}
}

// ---- F6: settings hub ----

func TestSettingsHubCards(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	find := func(cards []hubCard, title string) hubCard {
		for _, c := range cards {
			if c.Title == title {
				return c
			}
		}
		t.Fatalf("no %s card", title)
		return hubCard{}
	}
	quiet := hubCards(hubState{Now: now, PINOn: true, PortalNotify: true, Users: 3, RetentionDays: 90,
		ServerVersion: "v1.2.0", AgentVersion: "1.8.0", FreeKnown: true, FreeBytes: 200 << 30})
	for title, want := range map[string]string{
		"Access PIN": "PIN required", "Notifications": "1 channel on · SMTP not configured",
		"Active Directory": "AD: not configured", "Operational": "Logs kept 90 days", "Network": "HTTP only",
		"Updates": "Server v1.2.0 · Agent 1.8.0", "Local accounts": "3 users",
	} {
		if c := find(quiet, title); c.Value != want || c.Tone == "warn" || c.Tone == "bad" {
			t.Errorf("%s = %q (%s), want %q without a warning", title, c.Value, c.Tone, want)
		}
	}

	warn := hubCards(hubState{Now: now, EmailOn: true, Users: 1, BootstrapPresent: true,
		ADURL: "ldaps://dc", ADTest: &adTestResult{URL: "ldaps://dc", Err: "connection refused", At: now},
		HTTPSAddr: ":8443", CertPath: "/x.pem", CertExpiry: now.Add(6*24*time.Hour - time.Hour),
		StorageProblems: []string{"iso folder: directory does not exist yet"},
		ServerVersion:   "v1.2.0", LatestVersion: "v1.3.0"})
	for title, want := range map[string]struct{ value, tone, reason string }{
		"Notifications":    {"SMTP not configured", "warn", "no mail server"},
		"Local accounts":   {"1 user", "bad", "admin-bootstrap.txt"},
		"Active Directory": {"AD: connection failing", "bad", "connection refused"},
		"Network":          {"HTTPS on · cert expires in 6 days", "warn", "Renew"},
		"Storage":          {"Default locations", "bad", "iso folder"},
		"Updates":          {"update available", "warn", "v1.3.0"},
	} {
		c := find(warn, title)
		if !strings.Contains(c.Value, want.value) || c.Tone != want.tone || !strings.Contains(c.Reason, want.reason) {
			t.Errorf("%s = %+v, want value ~%q tone %s reason ~%q", title, c, want.value, want.tone, want.reason)
		}
	}
	if c := find(hubCards(hubState{Now: now, ADURL: "ldaps://dc", ADTest: &adTestResult{OK: true, At: now}}), "Active Directory"); c.Value != "AD: connected" {
		t.Errorf("tested AD = %q", c.Value)
	}
	if c := find(hubCards(hubState{Now: now, HTTPSAddr: ":8443", CertPath: "/x", CertExpiry: now.Add(-time.Hour)}), "Network"); c.Tone != "bad" {
		t.Errorf("expired cert tone = %q", c.Tone)
	}
	if c := find(hubCards(hubState{Now: now, FreeKnown: true, FreeBytes: 2 << 30}), "Storage"); c.Tone != "warn" {
		t.Errorf("low disk tone = %q", c.Tone)
	}
}

// ---- Settings save errors: 422, values kept, secrets never echoed ----

func withRuntime(r *Repos, db *storage.DB, bx *secrets.Box) {
	st, err := runtime.New(context.Background(), db, bx, config.Config{})
	if err != nil {
		panic(err)
	}
	r.Runtime = st
	r.Settings = auth.MustNewSettingsRepo(r.Users)
}

func TestSettingsSaveErrorsRerender(t *testing.T) {
	tp := newTestPortal(t, withRuntime)
	if out := tp.get("/portal/settings"); !strings.Contains(out, "PIN off") || !strings.Contains(out, "AD: not configured") {
		t.Error("hub should show live state")
	}
	for _, c := range []struct {
		path   string
		form   url.Values
		field  string
		kept   string
		secret string
	}{
		{"/portal/settings/active-directory", url.Values{"url": {"dc.corp"}, "bind_dn": {"CN=svc,DC=corp"}, "bind_password": {"ad-secret-pw"}},
			"url", `value="CN=svc,DC=corp"`, "ad-secret-pw"},
		{"/portal/settings/network", url.Values{"http_addr": {"8080"}, "external_url": {"deploy.example.com"}},
			"external_url", `value="deploy.example.com"`, ""},
		{"/portal/settings/operational", url.Values{"retention_days": {"-3"}, "display_timezone": {"Mars/Olympus"}},
			"display_timezone", `value="Mars/Olympus"`, ""},
		{"/portal/settings/notifications", url.Values{"smtp_host": {"smtp.example.com"}, "smtp_port": {"99999"}, "smtp_password": {"smtp-secret-pw"}},
			"smtp_port", `value="smtp.example.com"`, "smtp-secret-pw"},
		{"/portal/settings/storage", url.Values{"storage_iso": {"relative/iso"}}, "storage_iso", `value="relative/iso"`, ""},
		{"/portal/settings/branding", url.Values{"product_name": {"Acme"}, "logo_data_url": {"javascript:alert(1)"}}, "logo_data_url", `value="Acme"`, ""},
		{"/portal/settings/access-pin", url.Values{"pin": {strings.Repeat("7", 80)}}, "pin", "", strings.Repeat("7", 80)},
		{"/portal/settings/accounts", url.Values{"username": {"bob"}, "password": {""}}, "password", `value="bob"`, ""},
	} {
		rec := tp.do("POST", c.path, c.form, nil)
		body := rec.Body.String()
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", c.path, rec.Code)
			continue
		}
		if !strings.Contains(body, `id="err-`+c.field+`"`) || !strings.Contains(body, "data-form-error") {
			t.Errorf("%s: want an inline error on %s", c.path, c.field)
		}
		if c.kept != "" && !strings.Contains(body, c.kept) {
			t.Errorf("%s: submitted value %s not kept", c.path, c.kept)
		}
		if c.secret != "" && strings.Contains(body, c.secret) {
			t.Errorf("%s: secret echoed back", c.path)
		}
	}
	// Nothing was saved by the rejected operational form (validate first).
	if tp.r.Runtime.DisplayTimezone() != "" {
		t.Error("rejected save must change nothing")
	}
	// A good save still redirects.
	rec := tp.do("POST", "/portal/settings/operational", url.Values{"retention_days": {"30"}, "display_timezone": {"Europe/London"}}, nil)
	if rec.Code != http.StatusFound || tp.r.Runtime.LogRetentionDays() != 30 {
		t.Errorf("good save = %d", rec.Code)
	}
	if out := tp.get("/portal/settings"); !strings.Contains(out, "Logs kept 30 days") {
		t.Error("hub should reflect the saved retention")
	}
}
