package portal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// testPortal is the real portal mux over an in-memory database, plus a
// signed-in session cookie.
type testPortal struct {
	t      *testing.T
	mux    *http.ServeMux
	r      Repos
	cookie *http.Cookie
}

func newTestPortal(t *testing.T) *testPortal {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "key.bin"))
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inv := model.NewInventoryRepo(db)
	r := Repos{
		ISOs: model.NewISORepo(db), Unattend: model.NewUnattendRepo(db),
		Drivers: model.NewDriverPackageRepo(db), Software: model.NewSoftwarePackageRepo(db),
		Loadouts: model.NewSoftwareLoadoutRepo(db), Images: model.NewImageRepo(db),
		ImageGroups: model.NewImageGroupRepo(db), Inventory: inv,
		Bulk: model.NewBulkRepo(db, inv), Logs: model.NewLogRepo(db),
		Users: auth.New(db), Branding: branding.New(db),
		Mirrors: model.NewPayloadMirrorRepo(db), Blobs: blobs,
		DomainJoin: model.NewDomainJoinRepo(db, bx), SetupLock: model.NewSetupLockRepo(db),
		Updates: model.NewWindowsUpdateRepo(db, inv), Notifications: model.NewNotificationRepo(db),
		WebhookRepo: model.NewWebhookRepo(db, bx), Groups: model.NewMachineGroupRepo(db, inv),
		ImportedAssets: model.NewImportedAssetRepo(db), SecretsBox: bx,
	}
	mux := http.NewServeMux()
	if err := Register(mux, r); err != nil {
		t.Fatal(err)
	}
	u, err := r.Users.CreateUser(ctx, "admin", "pw-for-tests")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := r.Users.CreateSession(ctx, u.ID, auth.SessionIdleTimeout)
	if err != nil {
		t.Fatal(err)
	}
	return &testPortal{t: t, mux: mux, r: r, cookie: &http.Cookie{Name: sessionCookieName, Value: tok}}
}

func (tp *testPortal) do(method, target string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	tp.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if tp.cookie != nil {
		req.AddCookie(tp.cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tp.mux.ServeHTTP(rec, req)
	return rec
}

func (tp *testPortal) get(target string) string {
	tp.t.Helper()
	rec := tp.do("GET", target, nil, nil)
	if rec.Code != http.StatusOK {
		tp.t.Fatalf("GET %s = %d\n%s", target, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// ---- Server-side ?q= list search ----

func TestListSearchFiltersServerSide(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	iso, _ := tp.r.ISOs.Create(ctx, model.ISO{Name: "Win11 23H2", OSType: "windows-11"})
	_, _ = tp.r.ISOs.Create(ctx, model.ISO{Name: "Server 2022", OSType: "windows-server-2022"})
	_, _ = tp.r.Images.Create(ctx, model.Image{Name: "Lab", ISOID: &iso.ID})
	_, _ = tp.r.Images.Create(ctx, model.Image{Name: "Office", Description: "front desk"})
	_, _ = tp.r.Software.Create(ctx, model.SoftwarePackage{Name: "7-Zip", DetectionJSON: "[]", StepsJSON: "[]"})
	_, _ = tp.r.Software.Create(ctx, model.SoftwarePackage{Name: "Chrome", DetectionJSON: "[]", StepsJSON: "[]"})
	_, _ = tp.r.Updates.Create(ctx, model.WindowsUpdate{KBNumber: "KB5031354", Title: "2023-10 Cumulative", SupersedesJSON: "[]"})
	_, _ = tp.r.Updates.Create(ctx, model.WindowsUpdate{KBNumber: "KB5032190", Title: "2023-11 Cumulative", SupersedesJSON: "[]"})

	cases := []struct {
		url, want, notWant, count string
	}{
		// Case-insensitive, on the image's own name...
		{"/portal/images?q=LAB", ">Lab<", ">Office<", "1 of 2 images match"},
		// ...its description...
		{"/portal/images?q=front", ">Office<", ">Lab<", "1 of 2 images match"},
		// ...and the names it references (ISO name, not ISO id).
		{"/portal/images?q=win11", ">Lab<", ">Office<", "1 of 2 images match"},
		{"/portal/isos?q=server", "Server 2022", "Win11 23H2</strong>", "1 of 2 ISOs match"},
		{"/portal/software?q=zip", "7-Zip", ">Chrome<", "1 of 2 software packages match"},
		{"/portal/updates?q=2023-11", "KB5032190", "KB5031354", "1 of 2 updates match"},
	}
	for _, c := range cases {
		out := tp.get(c.url)
		if !strings.Contains(out, c.want) || strings.Contains(out, c.notWant) {
			t.Errorf("%s: want %q and not %q", c.url, c.want, c.notWant)
		}
		if !strings.Contains(out, c.count) {
			t.Errorf("%s: want count line %q", c.url, c.count)
		}
		// The box is rendered from the URL, inside a GET form with a
		// Search button and a Clear link; no filter-as-you-type hooks.
		q, _ := url.Parse(c.url)
		if !strings.Contains(out, `value="`+q.Query().Get("q")+`"`) || !strings.Contains(out, `>Search</button>`) ||
			!strings.Contains(out, `class="search-clear small" href="`+q.Path+`"`) {
			t.Errorf("%s: want search form with value, button and Clear link", c.url)
		}
		if strings.Contains(out, "data-filter=") {
			t.Errorf("%s: client-side data-filter still present", c.url)
		}
	}

	// No match: the search empty state, not the "nothing yet" one.
	out := tp.get("/portal/images?q=zzz")
	if !strings.Contains(out, "No matches for “zzz”") || !strings.Contains(out, "Clear search") || strings.Contains(out, "No images yet") {
		t.Errorf("want the no-matches state with a Clear search link:\n%s", out)
	}
	// IDs aren't searchable: "1" matches nothing by id alone.
	if out := tp.get("/portal/software?q=1"); !strings.Contains(out, "No matches") {
		t.Error("software search must not match on IDs")
	}
}

func TestWithQueryKeepsSearch(t *testing.T) {
	req := httptest.NewRequest("GET", "/portal/machines?q=dell&page=3&sort=name&dir=asc&group=2", nil)
	got := withQuery(req, "/portal/machines", "group", "5")
	u, _ := url.Parse(got)
	if u.Query().Get("q") != "dell" || u.Query().Get("group") != "5" || u.Query().Get("sort") != "name" || u.Query().Get("page") != "" {
		t.Errorf("withQuery group switch = %s", got)
	}
	if got := withQuery(req, "/portal/machines", "group", ""); strings.Contains(got, "group=") || !strings.Contains(got, "q=dell") {
		t.Errorf("withQuery dropping group = %s", got)
	}
	if got := withQuery(httptest.NewRequest("GET", "/portal/images?q=x", nil), "/portal/images", "q", ""); got != "/portal/images" {
		t.Errorf("clear URL = %s, want /portal/images", got)
	}
	sb := newSearchBox(req, "Search", "Search")
	if sb.Query != "dell" || len(sb.Hidden) != 3 { // dir, group, sort (q and page dropped)
		t.Errorf("search box hidden params = %+v", sb.Hidden)
	}
}

func TestMachineSearchServerSideKeepsQueryInGroupLinks(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	for _, m := range []match.Identity{
		{SystemUUID: "aaaaaaaa-0000-0000-0000-000000000001", SystemSerial: "SN-DELL-01", SystemManufacturer: "Dell Inc."},
		{SystemUUID: "aaaaaaaa-0000-0000-0000-000000000002", SystemSerial: "SN-HP-02", SystemManufacturer: "HP"},
	} {
		if _, err := tp.r.Inventory.UpsertFromIdentity(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	g, err := tp.r.Groups.Create(ctx, model.MachineGroup{Name: "Lab-A", Kind: model.MachineGroupManual})
	if err != nil {
		t.Fatal(err)
	}
	out := tp.get("/portal/machines?q=dell")
	if !strings.Contains(out, "SN-DELL-01") || strings.Contains(out, "SN-HP-02") {
		t.Error("machine ?q= must filter server-side")
	}
	if !strings.Contains(out, `href="/portal/machines?group=`+idStr(g.ID)+`&amp;q=dell"`) {
		t.Errorf("group sidebar link should keep q:\n%s", out)
	}
}

// ---- F1: rejected saves re-render the form ----

func TestRejectedSaveRerendersForm(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	if _, err := tp.r.Images.Create(ctx, model.Image{Name: "Lab"}); err != nil {
		t.Fatal(err)
	}

	// Image: duplicate name. Values kept, error under the name, password not echoed.
	rec := tp.do("POST", "/portal/images", url.Values{
		"name": {"Lab"}, "description": {"keep me"}, "dj_enabled": {"1"},
		"dj_domain": {"corp.example.com"}, "dj_password": {"S3cret-pw!"},
	}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("image create status = %d, want 422", rec.Code)
	}
	for _, want := range []string{`value="keep me"`, `value="corp.example.com"`, `aria-invalid="true" aria-describedby="err-name"`,
		"Another image already has this name", "data-form-error", "enter it again"} {
		if !strings.Contains(body, want) {
			t.Errorf("image re-render missing %q", want)
		}
	}
	if strings.Contains(body, "S3cret-pw!") {
		t.Error("the join password must not be echoed back")
	}

	// Software: invalid detection rule — everything else survives.
	rec = tp.do("POST", "/portal/software", url.Values{
		"name": {"MyApp"}, "description": {"desc kept"},
		"rule_index[]": {"0"}, "rule_0_type": {"file"}, // file rule without a path
	}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `value="MyApp"`) ||
		!strings.Contains(rec.Body.String(), "Detection rule 1") {
		t.Errorf("software re-render = %d\n%s", rec.Code, rec.Body.String())
	}

	// Unattend: bad name; passwords in the settings are blanked.
	rec = tp.do("POST", "/portal/unattends", url.Values{
		"name": {"bad/name"}, "description": {"ua kept"},
		"la_name[]": {"Administrator"}, "la_password[]": {"Adm1n-secret"}, "la_group[]": {"Administrators"},
	}, nil)
	body = rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `value="ua kept"`) || !strings.Contains(body, "err-name") {
		t.Errorf("unattend re-render = %d", rec.Code)
	}
	if strings.Contains(body, "Adm1n-secret") {
		t.Error("unattend passwords must not be echoed back")
	}
	if !strings.Contains(body, "Passwords aren") {
		t.Error("want the re-enter passwords note")
	}

	// Webhook: secret never echoed.
	rec = tp.do("POST", "/portal/settings/notifications/webhooks/new", url.Values{
		"name": {""}, "url": {"https://example.invalid/hook"}, "secret": {"hmac-secret"},
	}, nil)
	if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "hmac-secret") ||
		!strings.Contains(rec.Body.String(), `value="https://example.invalid/hook"`) {
		t.Errorf("webhook re-render = %d (secret echoed: %v)", rec.Code, strings.Contains(rec.Body.String(), "hmac-secret"))
	}

	// Edit path too: renaming Office to Lab is rejected in place.
	off, _ := tp.r.Images.Create(ctx, model.Image{Name: "Office"})
	rec = tp.do("POST", "/portal/images/"+idStr(off.ID), url.Values{"name": {"Lab"}, "description": {"edited"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `value="edited"`) {
		t.Errorf("image edit re-render = %d", rec.Code)
	}

	// A good save still redirects (PRG).
	rec = tp.do("POST", "/portal/images", url.Values{"name": {"Kiosk"}}, nil)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "/edit") {
		t.Errorf("successful create = %d %s, want 302 to edit", rec.Code, rec.Header().Get("Location"))
	}
}

// ---- F4: sliding sessions ----

func TestSessionSlidesAndExpiredPostReturnsToForm(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	// Age the session: 2h of idle time left.
	if _, err := tp.r.Users.DB.ExecContext(ctx, `UPDATE user_session SET expires_at=?`, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A page view slides it back to the full idle window and refreshes the cookie.
	rec := tp.do("GET", "/portal/images", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookieName) {
		t.Error("an extended session should refresh the cookie")
	}
	if !strings.Contains(rec.Body.String(), `name="ad-session-expires"`) {
		t.Error("pages should expose the session expiry")
	}
	var exp time.Time
	_ = tp.r.Users.DB.QueryRowContext(ctx, `SELECT expires_at FROM user_session`).Scan(&exp)
	if time.Until(exp) < auth.SessionIdleTimeout-time.Minute {
		t.Errorf("expiry not slid: %v left", time.Until(exp))
	}

	// Background polling doesn't count as activity.
	_, _ = tp.r.Users.DB.ExecContext(ctx, `UPDATE user_session SET expires_at=?`, time.Now().Add(2*time.Hour))
	rec = tp.do("GET", "/portal/images", nil, map[string]string{"Sec-Fetch-Mode": "cors"})
	_ = tp.r.Users.DB.QueryRowContext(ctx, `SELECT expires_at FROM user_session`).Scan(&exp)
	if time.Until(exp) > 3*time.Hour {
		t.Error("a background GET must not slide the session")
	}

	// Keep-alive extends and reports the new expiry.
	rec = tp.do("POST", "/portal/session/keepalive", nil, map[string]string{"X-Requested-With": "fetch"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"expires_at"`) {
		t.Errorf("keepalive = %d %s", rec.Code, rec.Body.String())
	}

	// Expire it, then submit a form: back to sign-in, next = the form page
	// (Referer), with the "changes were not saved" notice.
	_, _ = tp.r.Users.DB.ExecContext(ctx, `UPDATE user_session SET expires_at=?`, time.Now().Add(-time.Minute))
	rec = tp.do("POST", "/portal/images/7", url.Values{"name": {"x"}},
		map[string]string{"Referer": "http://example.com/portal/images/7/edit"})
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Path != "/portal/login" ||
		loc.Query().Get("next") != "/portal/images/7/edit" || loc.Query().Get("expired") != "post" {
		t.Errorf("expired POST redirect = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	// Without a usable Referer it falls back to the section list, never the POST URL.
	rec = tp.do("POST", "/portal/images/7", url.Values{"name": {"x"}}, map[string]string{"Referer": "https://evil.example/portal/x"})
	loc, _ = url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("next") != "/portal/images" {
		t.Errorf("fallback next = %q", loc.Query().Get("next"))
	}
	// Script requests get a 401, not an HTML redirect.
	rec = tp.do("POST", "/portal/session/keepalive", nil, map[string]string{"X-Requested-With": "fetch"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expired keepalive = %d, want 401", rec.Code)
	}
	// The login page explains what happened.
	tp.cookie = nil
	if out := tp.get("/portal/login?expired=post&next=%2Fportal%2Fimages%2F7%2Fedit"); !strings.Contains(out, "your changes were not saved") {
		t.Error("login page should show the expired-post notice")
	}
	// Old bookmark shape still works.
	if rec := tp.do("GET", "/portal/machines?q=x", nil, nil); rec.Code != http.StatusFound {
		t.Errorf("unauthenticated GET = %d", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"":                    "/portal/",
		"/portal/images?q=x":  "/portal/images?q=x",
		"https://evil.com/":   "/portal/",
		"//evil.com/portal/":  "/portal/",
		"/api/v1/auth/logout": "/portal/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
