package portal

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func namesTestDB(t *testing.T) (*storage.DB, *model.InventoryRepo) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, model.NewInventoryRepo(db)
}

func TestEntityFuncs(t *testing.T) {
	ctx := context.Background()
	db, inv := namesTestDB(t)
	img, err := model.NewImageRepo(db).Create(ctx, model.Image{Name: "Lab-A"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "AAAA1111-2222"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = inv.UpdateObservedIdentity(ctx, m.AgentID, "PC-0001", "")
	if err := inv.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "LAB-01"}); err != nil {
		t.Fatal(err)
	}
	fm := nameFuncs(model.NewNameLookup(ctx, inv))
	link := fm["entityLink"].(func(string, any) template.HTML)
	name := fm["entityName"].(func(string, any) string)

	if got := string(link("image", img.ID)); got != fmt.Sprintf(`<a href="/portal/images/%d/edit">Lab-A</a>`, img.ID) {
		t.Errorf("image link = %s", got)
	}
	idp := img.ID
	if got := name("image", &idp); got != "Lab-A" {
		t.Errorf("image name via pointer = %q", got)
	}
	if got := string(link("iso", model.ID(12))); !strings.Contains(got, "#12 (deleted)") || strings.Contains(got, "href") {
		t.Errorf("deleted link = %s", got)
	}
	var nilID *model.ID
	if got := string(link("image", nilID)); !strings.Contains(got, "—") {
		t.Errorf("nil link = %s", got)
	}
	if got := string(link("machine", m.ID)); !strings.Contains(got, ">LAB-01</a>") || !strings.Contains(got, "PC-0001") {
		t.Errorf("machine link (with rename tooltip) = %s", got)
	}
	if got := string(fm["renameHint"].(func(any) template.HTML)(m.ID)); !strings.Contains(got, "renaming from PC-0001") {
		t.Errorf("rename hint = %s", got)
	}
	if got := idLabel(model.ID(7)); got != "#7" {
		t.Errorf("idLabel = %q", got)
	}
	// Without repos everything degrades to "#N" without claiming deletion.
	bare := nameFuncs(model.NewNameLookup(ctx, nil))["entityName"].(func(string, any) string)
	if got := bare("image", model.ID(3)); got != "#3" {
		t.Errorf("repo-less name = %q", got)
	}
}

func TestTimeFuncs(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	loc, _ := time.LoadLocation("Europe/London")
	fm := timeFuncs(loc, func() time.Time { return now })
	ago := fm["timeAgo"].(func(any) template.HTML)
	got := string(ago(now.Add(-5 * time.Minute)))
	want := `<time datetime="2026-10-27T11:55:00Z" title="27 Oct 2026, 11:55:00 GMT">5m ago</time>`
	if got != want {
		t.Errorf("timeAgo = %s, want %s", got, want)
	}
	next := now.Add(3 * time.Hour)
	if got := string(ago(&next)); !strings.Contains(got, ">in 3h<") {
		t.Errorf("future timeAgo = %s", got)
	}
	if got := string(ago((*time.Time)(nil))); !strings.Contains(got, "—") {
		t.Errorf("nil timeAgo = %s", got)
	}
	if got := string(fm["timeAbs"].(func(any) template.HTML)(now)); !strings.Contains(got, ">27 Oct 2026, 12:00:00 GMT<") {
		t.Errorf("timeAbs = %s", got)
	}
	if relativeTime(now.Add(-40*24*time.Hour), now, time.UTC) != "17 Sep 2026" {
		t.Errorf("old dates fall back to the date")
	}
}

func TestHumanBytesAcceptsInts(t *testing.T) {
	hb := funcsFor(httptest.NewRequest("GET", "/portal/", nil), Repos{})["humanBytes"].(func(any) string)
	for in, want := range map[any]string{int64(512): "512 B", 1536: "1.5 KiB", int64(6442450944): "6.00 GiB"} {
		if got := hb(in); got != want {
			t.Errorf("humanBytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildNavPrefixActive(t *testing.T) {
	active := func(path string) (group, item string) {
		for _, g := range buildNav(path) {
			for _, it := range g.Items {
				if it.On {
					if !g.On {
						t.Errorf("%s: item %s on but group %s off", path, it.Label, g.Label)
					}
					return g.Label, it.Label
				}
			}
		}
		return "", ""
	}
	cases := map[string][2]string{
		"/portal/machines/12":            {"Fleet", "Machines"},
		"/portal/machines.csv":           {"Fleet", "Machines"},
		"/portal/images/3/edit":          {"Build", "Images"},
		"/portal/update-deployments/4":   {"Patching", "Windows Updates"},
		"/portal/settings/notifications": {"System", "Settings"},
		"/portal/":                       {"", ""},
	}
	for path, want := range cases {
		if g, i := active(path); g != want[0] || i != want[1] {
			t.Errorf("%s: active = %s/%s, want %s/%s", path, g, i, want[0], want[1])
		}
	}
	// Labels: "Updates" is now "Windows Updates"; URLs are unchanged.
	found := false
	for _, g := range navLayout {
		for _, it := range g.Items {
			if it.URL == "/portal/updates" && it.Label == "Windows Updates" {
				found = true
			}
		}
	}
	if !found {
		t.Error("want /portal/updates labelled Windows Updates")
	}
}

func TestFormIDsAcceptsRepeatedAndCommaSeparated(t *testing.T) {
	got := formIDs([]string{"3", "1, 2,3", "junk", "", "-4", "5;6"})
	want := []model.ID{3, 1, 2, 5, 6}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("formIDs = %v, want %v", got, want)
	}
}

func TestFriendlyErrorAndSection(t *testing.T) {
	sec := sectionFor("/portal/images/99999/edit")
	if sec.kind != model.KindImage || sec.url != "/portal/images" {
		t.Errorf("section = %+v", sec)
	}
	if s := sectionFor("/portal/update-deployments/3"); s.url != "/portal/updates" {
		t.Errorf("deployment section = %+v", s)
	}
	nf := fmt.Errorf("image 99999: %w", model.ErrNotFound)
	if got := friendlyError(model.KindImage, http.StatusNotFound, nf); got != "That image no longer exists. It may have been deleted." {
		t.Errorf("not found = %q", got)
	}
	val := fmt.Errorf("%w: name required", model.ErrValidation)
	if got := friendlyError(model.KindISO, http.StatusBadRequest, val); got != "Name required." {
		t.Errorf("validation = %q", got)
	}
	if got := friendlyError("", http.StatusInternalServerError, errors.New("disk I/O error at /var/x")); strings.Contains(got, "/var/x") {
		t.Errorf("5xx must not leak detail: %q", got)
	}
}

// Script-initiated requests (upload XHR, fetch) keep the plain-text error
// body their UI prints inline; navigations get the styled page (covered by
// TestErrorPageBody and the visual check).
func TestFailPageBackgroundKeepsPlainText(t *testing.T) {
	for _, hdr := range [][2]string{{"Sec-Fetch-Mode", "cors"}, {"X-Requested-With", "fetch"}} {
		bg := httptest.NewRequest("POST", "/portal/isos/3/upload", nil)
		bg.Header.Set(hdr[0], hdr[1])
		bw := httptest.NewRecorder()
		failPage(bw, bg, Repos{}, http.StatusNotFound, fmt.Errorf("iso 3: %w", model.ErrNotFound))
		if bw.Code != http.StatusNotFound || !strings.Contains(bw.Body.String(), "iso 3: not found") {
			t.Errorf("%s: background fail = %d %q", hdr[0], bw.Code, bw.Body.String())
		}
	}
	nav := httptest.NewRequest("GET", "/portal/images/1/edit", nil)
	nav.Header.Set("Sec-Fetch-Mode", "navigate")
	if isBackgroundRequest(nav) || isBackgroundRequest(httptest.NewRequest("GET", "/", nil)) {
		t.Error("navigations must get the styled page")
	}
}

func TestErrorPageBody(t *testing.T) {
	out := renderBodyWith(t, "error.html", map[string]any{
		"Code": 404, "Heading": "Image not found", "Message": "That image no longer exists. It may have been deleted.",
		"BackURL": "/portal/images", "BackLabel": "Images",
	})
	for _, want := range []string{"Image not found", "That image no longer exists", `href="/portal/images"`, "Back to Images", "Error 404"} {
		if !strings.Contains(out, want) {
			t.Errorf("error page missing %q:\n%s", want, out)
		}
	}
}

func renderBodyWith(t *testing.T, page string, data map[string]any) string {
	t.Helper()
	tmpl, err := template.New("").Funcs(withPortalFuncs(nil)).ParseFS(assetsFS, "templates/"+page, "templates/_search.html", "templates/_form_error.html")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.ExecuteTemplate(&b, "body", map[string]any{"Data": data}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestDeleteFlash(t *testing.T) {
	read := func(w *httptest.ResponseRecorder) string {
		req := httptest.NewRequest("GET", "/", nil)
		for _, c := range w.Result().Cookies() {
			req.AddCookie(c)
		}
		_, msg := readFlash(httptest.NewRecorder(), req)
		return msg
	}
	w := httptest.NewRecorder()
	deleteFlash(w, httptest.NewRequest("POST", "/portal/isos/1/delete", nil), model.KindISO, "Win11 23H2", nil)
	if got := read(w); got != `Deleted "Win11 23H2".` {
		t.Errorf("ok flash = %q", got)
	}
	w = httptest.NewRecorder()
	inUse := &model.InUseError{Kind: "iso", ID: 1, Name: "Win11 23H2",
		Refs: []model.InUseRef{{Noun: "image", Names: []string{"Lab", "Office", "Kiosk"}}}}
	deleteFlash(w, httptest.NewRequest("POST", "/portal/isos/1/delete", nil), model.KindISO, "Win11 23H2", inUse)
	if got := read(w); got != `Can't delete "Win11 23H2": used by 3 images (Lab, Office, Kiosk)` {
		t.Errorf("in-use flash = %q", got)
	}
}
