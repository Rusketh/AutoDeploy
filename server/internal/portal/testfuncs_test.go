package portal

import (
	"html/template"
	"net/http/httptest"
)

// withPortalFuncs returns the real template funcmap (funcsFor with no repos,
// so name lookups resolve nothing and times use UTC) overlaid with a test's
// own stubs. Render tests start from it so a template using a newly added
// helper doesn't fail to parse just because the test's stub map predates it.
func withPortalFuncs(overrides template.FuncMap) template.FuncMap {
	fm := funcsFor(httptest.NewRequest("GET", "/portal/", nil), Repos{})
	for k, f := range overrides {
		fm[k] = f
	}
	return fm
}
