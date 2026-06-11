package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// The catalog import page is mostly client-side JS; this smoke test
// catches template syntax errors and keeps the API endpoints it drives
// honest.
func TestImportPageRenders(t *testing.T) {
	out := renderBody(t, "windowsupdate_import.html", nil)
	for _, s := range []string{
		"Import from Microsoft",
		"/api/v1/mscatalog/search",
		"/api/v1/updates/import",
		"import-status",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("import page missing %q", s)
		}
	}
}

// The updates list must offer both paths: catalog import AND manual
// creation/upload.
func TestUpdateListOffersImportAndManual(t *testing.T) {
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"humanBytes": func(n int64) string { return "X" },
		"pct":        func(a, b int) int { return 0 },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/windowsupdate_list.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	render := func(data map[string]any) string {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
			t.Fatalf("exec: %v", err)
		}
		return buf.String()
	}

	// Empty state: both entry points offered.
	out := render(map[string]any{"Updates": nil})
	for _, s := range []string{"/portal/updates/import", "/portal/updates/new"} {
		if !strings.Contains(out, s) {
			t.Errorf("empty state missing %q", s)
		}
	}

	// Populated list: toolbar still offers both.
	out = render(map[string]any{
		"Updates":    []model.WindowsUpdate{{ID: 1, KBNumber: "KB5034122", Title: "CU"}},
		"Compliance": map[model.ID]model.ComplianceSummary{},
	})
	for _, s := range []string{"/portal/updates/import", "/portal/updates/new", "KB5034122"} {
		if !strings.Contains(out, s) {
			t.Errorf("list missing %q", s)
		}
	}
}
