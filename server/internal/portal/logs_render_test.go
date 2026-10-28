package portal

import (
	"bytes"
	"html/template"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// TestLogsPageRenders exercises the Logs page: one server-side GET search
// form (no client-side filter box or level pills), rows with relative time,
// level dot + text, category, plain-English message and the action code as
// a tag, an expanded key/value view with a Raw toggle, folded command output
// and the cursor pager.
func TestLogsPageRenders(t *testing.T) {
	tmpl, err := template.New("").Funcs(withPortalFuncs(nil)).ParseFS(assetsFS, "templates/logs.html")
	if err != nil {
		t.Fatalf("parse logs.html: %v", err)
	}
	now := time.Now()
	ev := []model.LogEvent{
		{ID: 5, OccurredAt: now, Component: "agent", Level: "ERROR", Actor: "abcd-uuid",
			Action: "package.install.fail", Target: "Chrome", MachineName: "WS-DEV-09",
			Fields: `{"exit_code":1603,"step":2,"steps":2,"stderr_tail":"line one\nline two"}`},
		{ID: 4, OccurredAt: now, Component: "agent", Level: "INFO", Action: "steps.exec.stderr", Actor: "abcd-uuid", Fields: `{"line":"MSI error 1714"}`},
		{ID: 3, OccurredAt: now, Component: "agent", Level: "INFO", Action: "steps.exec.stdout", Actor: "abcd-uuid", Fields: `{"line":"hello"}`},
		{ID: 1, OccurredAt: now, Component: "portal", Level: "INFO", Actor: "admin", User: "admin", Action: "auth.login.ok", Fields: "{}"},
	}
	raws := map[model.ID]string{}
	for _, e := range ev {
		raws[e.ID] = rawLogJSON(e)
	}
	for i := range ev {
		model.ApplyLogDescription(&ev[i], model.LogNames{})
	}
	rows := buildLogRows(ev, nil, time.UTC, raws)
	if len(rows) != 3 || len(rows[1].Output) != 2 || rows[1].Output[0].Message != "hello" {
		t.Fatalf("expected the two output lines folded oldest-first, got %+v", rows)
	}
	lq := model.ParseLogQuery(nil, url.Values{"q": {"chrome"}, "from": {"2026-10-01T09:00"}, "action": {"package.install.fail"}}, nil, time.UTC)
	data := map[string]any{
		"Rows": rows, "Query": lq, "Total": 450, "Start": 1, "End": 200,
		"OlderURL": "/portal/logs?before=1", "Levels": model.LogLevelChoices,
		"Categories": model.LogCategories, "Zone": "Europe/London",
		"Legacy": legacyLogChips(lq.Values()), "ClearURL": "/portal/logs",
		"TailURL": "/api/v1/logs?q=chrome&limit=50",
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec logs.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"WS-DEV-09",                 // resolved machine name
		"Chrome failed to install",  // catalogue message
		`class="log-code"`,          // raw action code as a small tag
		"package.install.fail",      //
		"Output (2 lines)",          // folded output
		"What to try:",              // hint in the expanded view
		`<details class="log-raw">`, // raw JSON behind a toggle
		"Showing 1–200 of 450",      // pager summary
		"Older →",                   //
		`value="chrome"`,            // search text kept in the form
		`value="2026-10-01T09:00"`,  // From kept in the form
		"From <span class=\"muted\">(Europe/London)</span>", // zone label
		`name="action" value="package.install.fail"`,        // old param kept as a chip
		`data-url="/api/v1/logs?q=chrome&amp;limit=50"`,     // tail honours filters
		"Warning", "Error", // level text, not colour alone
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs page missing %q", want)
		}
	}
	for _, gone := range []string{`id="log-filter"`, "data-level-pill", "Filter rows"} {
		if strings.Contains(out, gone) {
			t.Errorf("logs page still has client-side filtering (%q)", gone)
		}
	}
}
