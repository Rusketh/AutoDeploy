package portal

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// rawIDText matches a template printing "#" followed directly by an ID
// expression: #{{derefID .X}}, #{{int64 .MachineID}}, #{{.ID}}.
var rawIDText = regexp.MustCompile(`#\{\{-?\s*(derefID\b[^}]*|int64\b[^}]*|[^}]*\bID)\s*-?\}\}`)

// TestTemplatesDontPrintRawIDs guards the IDs-to-names work: page text must
// show names ({{entityName}} / {{entityLink}}) rather than "#12", and a row's
// own number goes through {{idLabel}}. IDs are still fine inside tag
// attributes (href, value, action, data-*, form, id) where operators never
// read them.
func TestTemplatesDontPrintRawIDs(t *testing.T) {
	pages, err := fs.Glob(assetsFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range pages {
		b, err := fs.ReadFile(assetsFS, page)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, loc := range rawIDText.FindAllStringIndex(src, -1) {
			if insideTag(src, loc[0]) {
				continue
			}
			line := strings.Count(src[:loc[0]], "\n") + 1
			t.Errorf("%s:%d prints a raw ID as text: %q — use entityName/entityLink (or idLabel for the row's own number)",
				strings.TrimPrefix(page, "templates/"), line, src[loc[0]:loc[1]])
		}
	}
}

// insideTag reports whether offset i falls between a tag's "<" and ">",
// i.e. in an attribute rather than in rendered text.
func insideTag(src string, i int) bool {
	open := strings.LastIndex(src[:i], "<")
	close := strings.LastIndex(src[:i], ">")
	return open > close
}

func TestRawIDGuardCatchesText(t *testing.T) {
	bad := `<td><code>#{{derefID .ImageID}}</code></td>`
	loc := rawIDText.FindStringIndex(bad)
	if loc == nil || insideTag(bad, loc[0]) {
		t.Fatalf("guard should flag %q", bad)
	}
	ok := `<a href="/portal/bulk/{{int64 .ID}}#{{int64 .ID}}">x</a>`
	loc = rawIDText.FindStringIndex(ok)
	if loc == nil || !insideTag(ok, loc[0]) {
		t.Fatalf("guard should allow attribute use in %q", ok)
	}
}
