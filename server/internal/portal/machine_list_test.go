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
// reported/bound machine names, the reported OS and the bound image's name.
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
	os := "Microsoft Windows 11 Pro"

	for _, q := range []string{"0k240y", "optiplex", "lab-pc-07", "win11 lab", "abcd1234", "ser99", "-new", "windows 11"} {
		if !machineMatchesQuery(m, b, names, os, q) {
			t.Errorf("query %q should match", q)
		}
	}
	for _, q := range []string{"latitude", "hp", "zzz"} {
		if machineMatchesQuery(m, b, names, os, q) {
			t.Errorf("query %q should NOT match", q)
		}
	}
}

// TestSortMachineRecords: server-side sorting orders the WHOLE set by the
// displayed value — including columns derived from bindings (name, image)
// — in both directions, with a stable ID tie-break.
func TestSortMachineRecords(t *testing.T) {
	imgID := model.ID(9)
	v := []model.MachineRecord{
		{ID: 1, SystemManufacturer: "HP", SystemProduct: "EliteDesk"},
		{ID: 2, SystemManufacturer: "Dell Inc.", SystemProduct: "OptiPlex"},
		{ID: 3, SystemManufacturer: "Acer", SystemProduct: "Veriton"},
	}
	bindings := map[model.ID]model.MachineBinding{
		1: {MachineName: "PC-C"},
		2: {MachineName: "PC-A", ImageID: &imgID},
		3: {MachineName: "PC-B"},
	}
	names := map[model.ID]string{imgID: "Win11"}

	sortMachineRecords(v, "model", "asc", bindings, names, nil)
	if v[0].ID != 3 || v[1].ID != 2 || v[2].ID != 1 {
		t.Errorf("model asc: got order %d,%d,%d", v[0].ID, v[1].ID, v[2].ID)
	}
	sortMachineRecords(v, "name", "desc", bindings, names, nil)
	if v[0].ID != 1 || v[1].ID != 3 || v[2].ID != 2 {
		t.Errorf("name desc: got order %d,%d,%d", v[0].ID, v[1].ID, v[2].ID)
	}
	// Image sort: bound machine vs unbound (empty sorts first asc).
	sortMachineRecords(v, "image", "asc", bindings, names, nil)
	if v[2].ID != 2 {
		t.Errorf("image asc: bound machine should sort last, got %d,%d,%d", v[0].ID, v[1].ID, v[2].ID)
	}
}

// TestMachineSortColumns: clicking the active column flips direction, any
// other column starts on its natural one, and links keep the filter while
// resetting to page 1.
func TestMachineSortColumns(t *testing.T) {
	req := httptest.NewRequest("GET", "/portal/machines?q=dell&sort=name&dir=asc&page=3", nil)
	cols := machineSortColumns(req, "name", "asc")
	if cols["name"].State != "asc" || !strings.Contains(cols["name"].URL, "dir=desc") {
		t.Errorf("active column should flip to desc: %+v", cols["name"])
	}
	if cols["model"].State != "" || !strings.Contains(cols["model"].URL, "dir=asc") {
		t.Errorf("inactive text column should start asc: %+v", cols["model"])
	}
	if !strings.Contains(cols["last_seen"].URL, "dir=desc") {
		t.Errorf("last_seen should start desc: %+v", cols["last_seen"])
	}
	for k, c := range cols {
		if !strings.Contains(c.URL, "q=dell") {
			t.Errorf("column %s dropped the filter: %s", k, c.URL)
		}
		if strings.Contains(c.URL, "page=") {
			t.Errorf("column %s should reset paging: %s", k, c.URL)
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
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS,
		"templates/machine_list.html", "templates/_pagination.html")
	if err != nil {
		t.Fatalf("parse machine_list.html: %v", err)
	}
	if data["Sort"] == nil {
		data["Sort"] = machineSortColumns(httptest.NewRequest("GET", "/portal/machines", nil), "last_seen", "desc")
	}
	if data["CSVURL"] == nil {
		data["CSVURL"] = "/portal/machines.csv"
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
		// Sortable headers are server-driven links (sort the whole fleet,
		// not the visible page).
		if !strings.Contains(out, `class="sortable`) || !strings.Contains(out, "sort=model") {
			t.Errorf("want server-sort header links; got:\n%s", out)
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

	t.Run("group add control is present whenever manual groups exist", func(t *testing.T) {
		// The selection bar must offer "add ticked machines to a group" on ANY
		// view (here: All machines, no active group) so an empty manual group
		// can be filled — not only when that group is the active filter.
		out := renderMachineList(t, map[string]any{
			"Rows": []machineListRowT{row}, "Page": page, "Query": "", "TotalAll": 1,
			"ManualGroups":  []model.MachineGroup{{ID: 7, Name: "Lab", Kind: model.MachineGroupManual}},
			"ActiveGroupID": int64(0),
		})
		if !strings.Contains(out, `name="group_id"`) || !strings.Contains(out, "Add to group") {
			t.Errorf("want a group dropdown + Add to group in the selection bar; got:\n%s", out)
		}
		if !strings.Contains(out, "/portal/machines/group-membership") {
			t.Errorf("want the membership formaction; got:\n%s", out)
		}
		if !strings.Contains(out, `value="7"`) || !strings.Contains(out, "Lab") {
			t.Errorf("want the manual group offered as an option; got:\n%s", out)
		}
	})
}
