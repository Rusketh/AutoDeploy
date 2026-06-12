package portal

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// TestPaginateNumberedLinks: the pager exposes a numbered strip (first,
// last, a window around the current page, gaps between) and every link
// preserves the other query parameters — jumping pages must never drop
// an active ?q= filter.
func TestPaginateNumberedLinks(t *testing.T) {
	req := httptest.NewRequest("GET", "/portal/machines?q=dell&size=10&page=7", nil)
	p := paginate(req, 200, 50) // 200 rows @ size 10 → 20 pages

	if p.Size != 10 || p.Total != 20 || p.Current != 7 {
		t.Fatalf("unexpected page math: %+v", p)
	}
	var nums []int
	gaps := 0
	for _, l := range p.Links {
		if l.Gap {
			gaps++
			continue
		}
		nums = append(nums, l.Num)
		if !strings.Contains(l.URL, "q=dell") {
			t.Errorf("page link %d dropped the filter: %s", l.Num, l.URL)
		}
		if l.Current != (l.Num == 7) {
			t.Errorf("link %d has wrong Current flag", l.Num)
		}
	}
	want := []int{1, 5, 6, 7, 8, 9, 20}
	if len(nums) != len(want) {
		t.Fatalf("want pages %v, got %v", want, nums)
	}
	for i := range want {
		if nums[i] != want[i] {
			t.Fatalf("want pages %v, got %v", want, nums)
		}
	}
	if gaps != 2 {
		t.Errorf("want 2 gap markers, got %d", gaps)
	}

	// Size options always include the active size, even a hand-typed one.
	req2 := httptest.NewRequest("GET", "/portal/machines?size=75", nil)
	p2 := paginate(req2, 10, 50)
	found := false
	for _, s := range p2.SizeOptions {
		if s == 75 {
			found = true
		}
	}
	if !found {
		t.Errorf("active size 75 missing from options %v", p2.SizeOptions)
	}
}

// TestMachineMatchesQuery: the ?q= predicate must search the fields an
// operator can see or paste — including the base-board identity, the
// reported/bound machine names and the bound image's name.
func TestMachineMatchesQuery(t *testing.T) {
	imgID := model.ID(4)
	m := model.MachineRecord{
		SystemUUID:         "abcd1234-0000",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "OptiPlex 7090",
		SystemSerial:       "SER99",
		BoardManufacturer:  "Dell Inc.",
		BoardProduct:       "0K240Y",
		ReportedName:       "LAB-PC-07",
	}
	b := model.MachineBinding{MachineName: "LAB-PC-07-NEW", ImageID: &imgID}
	names := map[model.ID]string{imgID: "Win11 Lab"}

	for _, q := range []string{"0k240y", "optiplex", "lab-pc-07", "win11 lab", "abcd1234", "ser99", "-new"} {
		if !machineMatchesQuery(m, b, names, q) {
			t.Errorf("query %q should match", q)
		}
	}
	for _, q := range []string{"latitude", "hp", "zzz"} {
		if machineMatchesQuery(m, b, names, q) {
			t.Errorf("query %q should NOT match", q)
		}
	}
}

// renderMachineList executes machine_list.html's body with the real
// template (minimal funcmap), guarding the filter box, counts, pager and
// empty states against template regressions.
func renderMachineList(t *testing.T, data map[string]any) string {
	t.Helper()
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"formatTime": func(tm time.Time) string { return tm.Format(time.RFC3339) },
		"add":        func(a, b int) int { return a + b },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/machine_list.html")
	if err != nil {
		t.Fatalf("parse machine_list.html: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec machine_list.html: %v", err)
	}
	return buf.String()
}

type machineListRowT struct {
	Machine        model.MachineRecord
	Name           string
	ImageName      string
	BLSet          bool
	OS             string
	ReimagePending bool
}

func TestMachineListRender(t *testing.T) {
	page := PageInfo{
		Current: 2, Total: 3, Size: 10, TotalRow: 25, Offset: 10, End: 20,
		PrevURL: "/portal/machines?page=1&q=dell", NextURL: "/portal/machines?page=3&q=dell",
		Links: []PageLink{
			{Num: 1, URL: "/portal/machines?page=1&q=dell"},
			{Num: 2, Current: true},
			{Num: 3, URL: "/portal/machines?page=3&q=dell"},
		},
		SizeOptions: []int{10, 25, 50},
	}
	row := machineListRowT{Machine: model.MachineRecord{
		ID: 1, SystemUUID: "abcd1234-0000-0000-0000-000000000000", LastSeen: time.Now(),
	}}

	t.Run("filter box carries the query; counts show match total", func(t *testing.T) {
		out := renderMachineList(t, map[string]any{
			"Rows": []machineListRowT{row}, "Page": page, "Query": "dell", "TotalAll": 40,
		})
		if !strings.Contains(out, `name="q"`) || !strings.Contains(out, `value="dell"`) {
			t.Errorf("want a server filter input carrying the query; got:\n%s", out)
		}
		if !strings.Contains(out, "25 of 40 machines match") {
			t.Errorf("want filtered count line; got:\n%s", out)
		}
		// Numbered pager with the current page marked, links keeping ?q=.
		if !strings.Contains(out, `aria-current="page"`) || !strings.Contains(out, "q=dell") {
			t.Errorf("want numbered pager preserving the filter; got:\n%s", out)
		}
		// Items-per-page selector present with the active size selected.
		if !strings.Contains(out, "data-page-size") || !strings.Contains(out, `<option value="10" selected>`) {
			t.Errorf("want items-per-page select with active size; got:\n%s", out)
		}
	})

	t.Run("no matches: filtered empty state with clear link", func(t *testing.T) {
		out := renderMachineList(t, map[string]any{
			"Rows": []machineListRowT{}, "Page": PageInfo{}, "Query": "zzz", "TotalAll": 40,
		})
		if !strings.Contains(out, "No machines match") || !strings.Contains(out, "Clear filter") {
			t.Errorf("want filtered empty state; got:\n%s", out)
		}
		if strings.Contains(out, "No machines yet") {
			t.Errorf("must not show the no-inventory empty state when filtering; got:\n%s", out)
		}
	})

	t.Run("single page still offers items-per-page", func(t *testing.T) {
		p := PageInfo{Current: 1, Total: 1, Size: 50, TotalRow: 1, Offset: 0, End: 1,
			SizeOptions: []int{10, 25, 50}}
		out := renderMachineList(t, map[string]any{
			"Rows": []machineListRowT{row}, "Page": p, "Query": "", "TotalAll": 1,
		})
		if !strings.Contains(out, "data-page-size") {
			t.Errorf("want size selector even on a single page; got:\n%s", out)
		}
		if strings.Contains(out, "pager-num") {
			t.Errorf("numbered strip should hide on a single page; got:\n%s", out)
		}
	})
}
