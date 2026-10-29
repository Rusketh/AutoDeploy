package portal

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// List search. Every list page's search box is a plain GET form
// (?q=…): the search runs when the operator presses Enter or the
// Search button, each search is a normal navigation (so Back/Forward
// step through earlier searches), and the box is rendered from the URL
// so it always matches the results below it. Filtering happens here on
// the server, before any pagination.

// searchQuery is the trimmed ?q= value.
func searchQuery(req *http.Request) string {
	return strings.TrimSpace(req.URL.Query().Get("q"))
}

// matchesQuery reports whether any of fields contains needle,
// case-insensitively. needle must already be lower-cased; an empty
// needle matches everything.
func matchesQuery(needle string, fields ...string) bool {
	if needle == "" {
		return true
	}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

// filterByQuery keeps the items whose visible fields (names, titles,
// descriptions — not IDs) contain q, case-insensitively. An empty q
// returns items unchanged.
func filterByQuery[T any](items []T, q string, fields func(T) []string) []T {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if matchesQuery(needle, fields(it)...) {
			out = append(out, it)
		}
	}
	return out
}

// withQuery returns path with the request's current query string,
// minus ?page= (a changed view restarts at page 1), with kv applied as
// name/value pairs — an empty value removes that parameter. It is the
// one helper behind every "change the view but keep the search" link:
// group sidebar entries, sort headers, the Clear link.
func withQuery(req *http.Request, path string, kv ...string) string {
	q := url.Values{}
	if req != nil {
		q = req.URL.Query()
	}
	q.Del("page")
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			q.Del(kv[i])
		} else {
			q.Set(kv[i], kv[i+1])
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

// searchParam is a hidden name/value the search form re-submits so a
// search keeps the rest of the view (group, sort, page size).
type searchParam struct{ Name, Value string }

// SearchBox is what the shared "search_form" / "search_empty" partials
// render: the form action, the current query, the hidden params it
// must carry, the Clear link and the result counts.
type SearchBox struct {
	Action      string
	Query       string
	Placeholder string
	Label       string
	Title       string // optional tooltip naming the searched fields
	Hidden      []searchParam
	ClearURL    string
	Matched     int // rows matching Query
	Total       int // rows before filtering
	Noun        string
	// ID names the form so filter controls rendered elsewhere on the
	// page can join it with form="…" (optional).
	ID string
	// Filtered is true while a filter other than q is set; the Clear
	// link then clears those too.
	Filtered bool
}

// newSearchBox builds the search box state for the current request.
// Every other query parameter except q and page rides along as a
// hidden field, so searching keeps the group, sort and page size.
func newSearchBox(req *http.Request, placeholder, label string) SearchBox {
	sb := SearchBox{
		Action:      req.URL.Path,
		Query:       searchQuery(req),
		Placeholder: placeholder,
		Label:       label,
		ClearURL:    withQuery(req, req.URL.Path, "q", ""),
	}
	q := req.URL.Query()
	for name, vals := range q {
		if name == "q" || name == "page" || len(vals) == 0 || vals[0] == "" {
			continue
		}
		sb.Hidden = append(sb.Hidden, searchParam{Name: name, Value: vals[0]})
	}
	// Stable field order so the markup (and tests) don't flap.
	sort.Slice(sb.Hidden, func(i, j int) bool { return sb.Hidden[i].Name < sb.Hidden[j].Name })
	return sb
}

// counted records the before/after counts and the noun used in the
// "N of M images match" line.
func (sb SearchBox) counted(matched, total int, noun string) SearchBox {
	sb.Matched, sb.Total, sb.Noun = matched, total, noun
	return sb
}

// withFilters adapts the box for a page with its own filter controls:
// the named parameters are submitted by those controls (joined to this
// form by ID), so they are not repeated as hidden fields, and Clear
// drops them along with the search text. active says whether any of
// them is currently set.
func (sb SearchBox) withFilters(req *http.Request, id string, active bool, names ...string) SearchBox {
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	kept := sb.Hidden[:0:0]
	for _, h := range sb.Hidden {
		if !drop[h.Name] {
			kept = append(kept, h)
		}
	}
	sb.Hidden = kept
	sb.ID = id
	sb.Filtered = active
	clear := []string{"q", ""}
	for _, n := range names {
		clear = append(clear, n, "")
	}
	sb.ClearURL = withQuery(req, req.URL.Path, clear...)
	return sb
}
