package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// TestLogsPageRenders exercises the redesigned logs page: each event renders a
// summary row plus a hidden detail row, the actor UUID is resolved to a machine
// name (with a raw-actor fallback), and the level pills + time-range presets are
// present for the client-side controller to wire up.
func TestLogsPageRenders(t *testing.T) {
	funcs := template.FuncMap{
		"formatTime": func(tm time.Time) string { return tm.Format(time.RFC3339) },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/logs.html")
	if err != nil {
		t.Fatalf("parse logs.html: %v", err)
	}
	now := time.Now()
	data := map[string]any{
		"Query": model.LogSearch{Component: "agent"},
		"Events": []model.LogEvent{
			{ID: 2, OccurredAt: now, Component: "agent", Level: "ERROR",
				Actor: "abcd-uuid", Action: "update.fail", Target: "KB5031",
				Fields: `{"exit_code":1}`, MachineName: "WS-DEV-09"},
			{ID: 1, OccurredAt: now, Component: "server", Level: "INFO",
				Actor: "system", Action: "login.ok", Fields: "{}"},
		},
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec logs.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"WS-DEV-09",               // resolved machine name is shown
		"system",                  // non-machine actor falls back to the raw actor
		`data-level="ERROR"`,      // level attr drives pill filtering
		`data-level-pill="ERROR"`, // level pills are present
		"log-detail",              // expandable detail row
		"log-fields",              // the Fields JSON lives in the detail row
		`data-since-preset="60"`,  // time-range presets
		"logwrap",                 // scroll container (overflow fix)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs page missing %q", want)
		}
	}
}
