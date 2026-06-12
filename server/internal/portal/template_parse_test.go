package portal

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// TestAllTemplatesParse parses every page template with the same partial
// set render() uses. Templates are parsed per request in production, so a
// syntax error (or a partial missing from render's list) would otherwise
// surface as a 500 the first time an operator opens that page.
func TestAllTemplatesParse(t *testing.T) {
	pages, err := fs.Glob(assetsFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	// A permissive funcmap mirroring funcsFor's names: they must exist for
	// parse; bodies don't run. Keep in sync when adding template funcs.
	funcs := template.FuncMap{}
	for _, name := range []string{
		"derefID", "idEq", "int64", "derefIDtoID", "deref", "join",
		"hasItems", "formatTime", "list", "dict", "add", "sub", "min",
		"derefInt", "lt", "toFloat", "div", "mul", "pct", "humanBytes",
		"relTime", "formatDate",
	} {
		funcs[name] = func(args ...any) string { return "" }
	}
	for _, page := range pages {
		base := strings.TrimPrefix(page, "templates/")
		if strings.HasPrefix(base, "_") {
			continue // partial, parsed alongside every page below
		}
		if _, err := template.New("").Funcs(funcs).ParseFS(assetsFS,
			"templates/_layout.html", "templates/_icons.html",
			"templates/_action_picker.html", "templates/_pagination.html",
			page); err != nil {
			t.Errorf("parse %s: %v", base, err)
		}
	}
}

// TestNotificationsRender: the notifications page uses the shared pager
// now; make sure the body executes with the handler's data shape.
func TestNotificationsRender(t *testing.T) {
	funcs := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS,
		"templates/notifications.html", "templates/_pagination.html")
	if err != nil {
		t.Fatalf("parse notifications.html: %v", err)
	}
	page := PageInfo{
		Current: 1, Total: 2, Size: 50, TotalRow: 60, Offset: 0, End: 50,
		NextURL:     "/portal/notifications?page=2&severity=error",
		Links:       []PageLink{{Num: 1, Current: true}, {Num: 2, URL: "/portal/notifications?page=2&severity=error"}},
		SizeOptions: []int{10, 25, 50},
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": map[string]any{
		"Notifications": []model.Notification{{
			ID: 1, Severity: "error", Title: "Disk full", Event: "system.disk",
			OccurredAt: time.Now(),
		}},
		"Page":       page,
		"Severity":   "error",
		"Event":      "",
		"Unread":     false,
		"Categories": notify.EventCategories,
	}})
	if err != nil {
		t.Fatalf("exec notifications.html: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "data-page-size") || !strings.Contains(out, `aria-current="page"`) {
		t.Errorf("want shared pager on notifications; got:\n%s", out)
	}
	// Page links must carry the active severity filter.
	if !strings.Contains(out, "severity=error") {
		t.Errorf("pager dropped the severity filter; got:\n%s", out)
	}
}
