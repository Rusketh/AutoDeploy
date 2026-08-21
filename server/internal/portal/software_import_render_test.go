package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// The winget import page is mostly client-side JS; this smoke test
// catches template syntax errors and keeps the API endpoints it drives
// honest.
func TestSoftwareImportPageRenders(t *testing.T) {
	out := renderBody(t, "software_import.html", nil)
	for _, s := range []string{
		"Import from winget",
		"/api/v1/winget/search",
		"/api/v1/winget/manifest",
		"/api/v1/software/import",
		"import-status",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("import page missing %q", s)
		}
	}
}

// The software list must offer both the winget import path and manual
// package creation.
func TestSoftwareListOffersImportAndManual(t *testing.T) {
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"humanBytes": func(n int64) string { return "X" },
		"pct":        func(a, b int) int { return 0 },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/software_list.html")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": map[string]any{"Packages": nil}}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	out := buf.String()
	for _, s := range []string{"/portal/software/import", "/portal/software/new"} {
		if !strings.Contains(out, s) {
			t.Errorf("software list missing %q", s)
		}
	}
}
