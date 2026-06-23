package portal

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/branding"
)

// TestBrandingLogoPreviewRenders guards the logo display fix: a data:image
// URL must survive into the <img src> rather than being rewritten to
// html/template's "#ZgotmplZ" URL-sanitiser sentinel (which is why pasted /
// uploaded logos never showed). Fails against the pre-fix template.
func TestBrandingLogoPreviewRenders(t *testing.T) {
	funcs := template.FuncMap{
		"brandLogo": func(s string) template.URL {
			if v, ok := branding.SafeLogoURL(s); ok {
				return template.URL(v)
			}
			return template.URL("")
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/settings_branding.html")
	if err != nil {
		t.Fatalf("parse settings_branding.html: %v", err)
	}
	dataURL := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": map[string]any{
		"B": branding.Brand{ProductName: "Acme", LogoDataURL: dataURL},
	}}); err != nil {
		t.Fatalf("exec settings_branding.html: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "ZgotmplZ") {
		t.Errorf("logo data URL was sanitised away (ZgotmplZ); got:\n%s", out)
	}
	// The data:image scheme must reach the src verbatim (base64 bytes like '+'
	// are harmlessly HTML-entity-encoded by the attribute escaper, so assert on
	// the scheme prefix rather than the whole payload).
	if !strings.Contains(out, `src="data:image/png;base64,`) {
		t.Errorf("logo data URL did not reach the <img src>; got:\n%s", out)
	}
}
