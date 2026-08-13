// Package portal serves the management portal HTML. Every artifact and
// system setting is configurable through a structured form here — the
// operator never has to hand-craft JSON or XML. The JSON API at /api/v1
// is the same authoritative surface; the portal is its browser front-end.
//
// Layout: each entity has list / new / edit pages plus form-POST
// handlers that follow the post-redirect-get pattern (with flash
// messages carried in a short-lived cookie). Templates are parsed per
// request from the embedded FS so adding a new page is a single file.
package portal

import (
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/runtime"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/wol"
)

//go:embed templates/*.html assets/*
var assetsFS embed.FS

// Repos is the dependency bundle the portal needs. Keeping the same shape
// as api.Repos so main can pass through a single struct.
type Repos struct {
	ISOs      *model.ISORepo
	Unattend  *model.UnattendRepo
	Drivers   *model.DriverPackageRepo
	Software  *model.SoftwarePackageRepo
	Loadouts    *model.SoftwareLoadoutRepo
	Images      *model.ImageRepo
	ImageGroups *model.ImageGroupRepo
	Inventory *model.InventoryRepo
	Bulk      *model.BulkRepo
	Logs      *model.LogRepo
	Users     *auth.Repo
	Settings  *auth.SettingsRepo
	Branding  *branding.Repo
	Mirrors   *model.PayloadMirrorRepo
	Runtime   *runtime.Settings
	Resolver  *resolve.Resolver
	Blobs     *storage.BlobStore
	AD        *addomain.Service
	// DomainJoin stores per-image agent-driven AD join config (the image
	// edit form reads/writes it).
	DomainJoin *model.DomainJoinRepo
	// SetupLock stores the per-image "setup lockout" toggle (the image edit
	// form reads/writes it).
	SetupLock *model.SetupLockRepo
	// Updates manages Windows Update KB patches and deployment jobs.
	Updates *model.WindowsUpdateRepo
	// Notifications handles in-portal notification CRUD and preferences.
	Notifications *model.NotificationRepo
	// WebhookRepo manages webhook endpoints and delivery logs.
	WebhookRepo *model.WebhookRepo
	// Groups manages AutoDeploy machine groups (manual + dynamic, nestable).
	Groups *model.MachineGroupRepo
	// ImportedAssets stores operator-imported assets (serial/model → name/OU)
	// that pre-seed a machine's binding the first time it is seen.
	ImportedAssets *model.ImportedAssetRepo
	// Emitter fans out events to all notification channels.
	Emitter *notify.Emitter
	// Waker sends Wake-on-LAN packets for bulk operations that request it.
	// Optional; nil skips waking.
	Waker *wol.Waker
	// SecretsBox is unused at the portal layer but kept here so the
	// bundle matches the api one-for-one if we ever want to swap.
	SecretsBox *secrets.Box
	// DataDir is the on-disk root used to check for the bootstrap-
	// admin file so the portal can warn while it still exists.
	DataDir string
	// ServerVersion is the build-time version main.go reads from
	// -ldflags. Rendered in the Settings -> Updates page header.
	ServerVersion string
}

const (
	sessionCookieName = "autodeploy_session"
	flashCookieName   = "autodeploy_flash"
)

// deployStallAfter is how long a deployment may sit in_progress with no
// machine check-in before the dashboard treats it as stalled / likely-failed.
// Chosen longer than a realistic Windows install so a slow-but-healthy deploy
// isn't flagged; the state is derived (not persisted), so it clears itself the
// moment the machine checks back in.
const deployStallAfter = 45 * time.Minute

// ctxKeyUser is the private context key for the authenticated portal user.
type ctxKeyUser struct{}

// Register mounts the portal routes. The session middleware protects
// every /portal/* path except the login form, its POST handler and the
// static assets.
func Register(mux *http.ServeMux, r Repos) error {
	assets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return err
	}
	mux.Handle("GET /portal/assets/", http.StripPrefix("/portal/assets/",
		http.FileServer(http.FS(assets))))

	// Public auth routes.
	mux.HandleFunc("GET /portal/login", loginForm(r))
	mux.HandleFunc("POST /portal/login", loginSubmit(r))
	mux.HandleFunc("POST /portal/logout", logoutSubmit(r))

	// Protected pages. We wrap each handler in requireSession because
	// net/http's ServeMux does not have route-level middleware.
	get := func(path string, h http.HandlerFunc) {
		mux.HandleFunc("GET "+path, requireSession(r, h))
	}
	post := func(path string, h http.HandlerFunc) {
		mux.HandleFunc("POST "+path, requireSession(r, h))
	}

	mux.HandleFunc("GET /portal", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/portal/", http.StatusFound)
	})
	// /portal/ is the dashboard for an authenticated session AND the
	// catch-all for any unknown /portal/* path. ServeMux honours
	// longest-match for the registered routes; URLs that genuinely fall
	// through here render the styled 404 instead of Go's default
	// plain-text "404 page not found".
	get("/portal/", func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/portal/" {
			renderPortalError(w, req, r, http.StatusNotFound,
				"Page not found",
				"The URL "+req.URL.Path+" doesn't match anything in the portal. Use the navigation above or jump back to the dashboard.",
				"")
			return
		}
		dashboardPage(r)(w, req)
	})

	registerISORoutes(get, post, r)
	registerUnattendRoutes(get, post, r)
	registerDriverRoutes(get, post, r)
	registerSoftwareRoutes(get, post, r)
	registerLoadoutRoutes(get, post, r)
	registerImageRoutes(get, post, r)
	registerInventoryRoutes(get, post, r)
	registerImportRoutes(get, post, r)
	registerMachineGroupRoutes(get, post, r)
	registerBulkRoutes(get, post, r)
	registerLogsRoutes(get, post, r)
	registerSettingsRoutes(get, post, r)
	registerDownloadRoutes(get, post, r)
	mirrorRoutes(get, post, r)
	registerWindowsUpdateRoutes(get, post, r)
	registerNotificationRoutes(get, post, r)

	return nil
}

// requireSession redirects to /portal/login if the request has no valid
// session cookie. The "next" parameter remembers where the user was
// trying to go.
func requireSession(r Repos, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := sessionUser(req, r)
		if !ok {
			to := url.Values{}
			to.Set("next", req.URL.RequestURI())
			http.Redirect(w, req, "/portal/login?"+to.Encode(), http.StatusFound)
			return
		}
		ctx := context.WithValue(req.Context(), ctxKeyUser{}, u)
		h(w, req.WithContext(ctx))
	}
}

func sessionUser(req *http.Request, r Repos) (auth.User, bool) {
	c, err := req.Cookie(sessionCookieName)
	if err != nil {
		return auth.User{}, false
	}
	uid, err := r.Users.LookupSession(req.Context(), c.Value)
	if err != nil {
		return auth.User{}, false
	}
	u, err := r.Users.GetUser(req.Context(), uid)
	if err != nil {
		return auth.User{}, false
	}
	return u, true
}

func loginForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// If already logged in, bounce to where they were going.
		if _, ok := sessionUser(req, r); ok {
			next := req.URL.Query().Get("next")
			if next == "" {
				next = "/portal/"
			}
			http.Redirect(w, req, next, http.StatusFound)
			return
		}
		render(w, req, r, "login.html", "Sign in", map[string]any{
			"Next":  req.URL.Query().Get("next"),
			"Error": req.URL.Query().Get("error"),
		})
	}
}

func loginSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		u, err := r.Users.Authenticate(req.Context(),
			req.FormValue("username"), req.FormValue("password"))
		if err != nil {
			username := req.FormValue("username")
			if username == "" {
				username = "(blank)"
			}
			r.Emitter.Emit(req.Context(), notify.Event{
				Name:     notify.EventAuthLoginFailed,
				Severity: notify.SevWarning,
				Title:    "Failed login attempt for " + username,
				Body:     "A portal sign-in with this username was rejected.",
				Link:     "/portal/logs",
				Actor:    username,
			})
			q := url.Values{}
			q.Set("error", "invalid credentials")
			if n := req.FormValue("next"); n != "" {
				q.Set("next", n)
			}
			http.Redirect(w, req, "/portal/login?"+q.Encode(), http.StatusFound)
			return
		}
		tok, err := r.Users.CreateSession(req.Context(), u.ID, 12*time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    tok,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Expires:  time.Now().Add(12 * time.Hour),
			Secure:   req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https",
		})
		next := req.FormValue("next")
		if next == "" || !strings.HasPrefix(next, "/portal/") {
			next = "/portal/"
		}
		http.Redirect(w, req, next, http.StatusFound)
	}
}

func logoutSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if c, err := req.Cookie(sessionCookieName); err == nil {
			_ = r.Users.DeleteSession(req.Context(), c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookieName, Value: "", Path: "/",
			Expires: time.Unix(0, 0),
		})
		http.Redirect(w, req, "/portal/login", http.StatusFound)
	}
}

// flash sets a one-shot message cookie that the next page reads and
// clears. Used to confirm "created", "saved", "deleted".
func flash(w http.ResponseWriter, kind, msg string) {
	v := kind + "|" + msg
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(v)),
		Path:     "/",
		HttpOnly: true,
		MaxAge:   30,
	})
}

func readFlash(w http.ResponseWriter, req *http.Request) (kind, msg string) {
	c, err := req.Cookie(flashCookieName)
	if err != nil {
		return "", ""
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", ""
	}
	// Clear.
	http.SetCookie(w, &http.Cookie{
		Name: flashCookieName, Value: "", Path: "/",
		Expires: time.Unix(0, 0),
	})
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// dashboardPage renders the index with a few useful counters and a
// recent-activity feed so the operator lands somewhere informative
// instead of an empty page.
func dashboardPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		type counts struct {
			ISOs, Unattends, Drivers, Software, Loadouts, Images, Machines int
		}
		var c counts
		if l, err := r.ISOs.List(req.Context()); err == nil {
			c.ISOs = len(l)
		}
		if l, err := r.Unattend.List(req.Context()); err == nil {
			c.Unattends = len(l)
		}
		if l, err := r.Drivers.List(req.Context()); err == nil {
			c.Drivers = len(l)
		}
		if l, err := r.Software.List(req.Context()); err == nil {
			c.Software = len(l)
		}
		if l, err := r.Loadouts.List(req.Context()); err == nil {
			c.Loadouts = len(l)
		}
		if l, err := r.Images.List(req.Context()); err == nil {
			c.Images = len(l)
		}
		machines, _ := r.Inventory.List(req.Context())
		c.Machines = len(machines)

		// Recent activity: last 12 log events across all components,
		// for a quick "what just happened" panel.
		events, _ := r.Logs.Search(req.Context(), model.LogSearch{Limit: 12})

		// Top 5 most-recently-seen machines for a "live fleet" panel.
		topMachines := machines
		if len(topMachines) > 5 {
			topMachines = topMachines[:5]
		}
		// Deploy outcome rollup for the last 24h. Batch-load history for
		// all machines in a single query instead of N+1.
		allIDs := make([]model.ID, len(machines))
		for i, m := range machines {
			allIDs[i] = m.ID
		}
		allHistory, _ := r.Inventory.ListHistoryForMachines(req.Context(), allIDs)
		var ok, failed, inProgress, stalled int
		for _, m := range machines {
			// A machine that hasn't checked in for longer than deployStallAfter
			// while a deploy is still in_progress has most likely failed Setup
			// (or is powered off) — Windows Setup can't always self-report a
			// catastrophic failure, so we derive it here from last_seen rather
			// than persist a possibly-premature "failed".
			machineStalled := time.Since(m.LastSeen) > deployStallAfter
			for _, h := range allHistory[m.ID] {
				if time.Since(h.StartedAt) > 24*time.Hour {
					break
				}
				switch h.Outcome {
				case "ok":
					ok++
				case "failed":
					failed++
				case "in_progress":
					if machineStalled {
						stalled++
					} else {
						inProgress++
					}
				}
			}
		}
		osBreakdown, _ := r.Inventory.OSBreakdown(req.Context())
		swCompliance, _ := r.Software.AllComplianceSummaries(req.Context())
		swPkgs, _ := r.Software.List(req.Context())
		swFull, swGap := 0, 0
		for _, p := range swPkgs {
			if sc, ok := swCompliance[p.ID]; ok && sc.TargetCount > 0 {
				if sc.InstalledCount == sc.TargetCount {
					swFull++
				} else {
					swGap++
				}
			}
		}
		var updateSummary model.FleetUpdateSummary
		if r.Updates != nil {
			updateSummary, _ = r.Updates.FleetUpdateSummary(req.Context())
		}
		render(w, req, r, "index.html", "Dashboard", map[string]any{
			"Counts":      c,
			"Events":      events,
			"TopMachines": topMachines,
			"Deploys": map[string]int{
				"ok":          ok,
				"failed":      failed,
				"in_progress": inProgress,
				"stalled":     stalled,
			},
			"OSBreakdown":   osBreakdown,
			"SWFull":        swFull,
			"SWGap":         swGap,
			"SWTotal":       len(swPkgs),
			"UpdateSummary": updateSummary,
		})
	}
}

// displayLoc resolves the operator-configured display/scheduling time zone,
// falling back to UTC when runtime settings are unavailable or unset. Used by
// handlers that parse schedule input or render times outside a template.
func displayLoc(r Repos) *time.Location {
	if r.Runtime != nil {
		if l := r.Runtime.DisplayLocation(); l != nil {
			return l
		}
	}
	return time.UTC
}

// Template helper funcs available to every page.
func funcsFor(req *http.Request, r Repos) template.FuncMap {
	// Resolve the operator-configured display time zone once per render.
	// Absolute timestamps (formatTime) are shown in this zone so "Last
	// seen" / check-in times read in the operator's locale rather than UTC.
	// Falls back to UTC when settings are unavailable or unset.
	loc := time.UTC
	if r.Runtime != nil {
		loc = r.Runtime.DisplayLocation()
	}
	return template.FuncMap{
		"derefID": func(p *model.ID) string {
			if p == nil {
				return ""
			}
			return strconv.FormatInt(int64(*p), 10)
		},
		"idEq": func(p *model.ID, v int64) bool {
			return p != nil && int64(*p) == v
		},
		"int64": func(id model.ID) int64 { return int64(id) },
		"derefIDtoID": func(p *model.ID) model.ID {
			if p == nil {
				return 0
			}
			return *p
		},
		"deref": func(t *time.Time) time.Time {
			if t == nil {
				return time.Time{}
			}
			return *t
		},
		// brandLogo emits a validated logo reference as a trusted URL so an
		// <img src> can carry a data:image/... URL. html/template otherwise
		// rewrites any non-http(s)/mailto scheme (including data:) to
		// "#ZgotmplZ", which is why a pasted/uploaded logo never displayed.
		// branding.SafeLogoURL gates the value to image data URLs / http(s),
		// so this cannot smuggle a javascript: or other unsafe scheme.
		"brandLogo": func(s string) template.URL {
			if v, ok := branding.SafeLogoURL(s); ok {
				return template.URL(v)
			}
			return template.URL("")
		},
		"join": strings.Join,
		"hasItems": func(s any) bool {
			switch v := s.(type) {
			case nil:
				return false
			case []any:
				return len(v) > 0
			}
			return true
		},
		// formatTime renders an absolute timestamp in the operator's
		// configured display time zone, in a human-friendly form
		// ("11 Jun 2026, 14:47:21"). The zone itself is shown once in the
		// page footer rather than on every stamp. Defaults to UTC when unset.
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(loc).Format("2 Jan 2006, 15:04:05")
		},
		// displayTZName is the configured display zone's name (e.g.
		// "Europe/London" or "UTC"). Shown once in the footer and handed to
		// the live-tail JS so client-rendered times match the server.
		"displayTZName": func() string { return loc.String() },
		// adJoinLabel / adJoinBadgeClass render a machine's AD join status as a
		// human label and a badge colour class. An empty (unknown / N/A) status
		// yields an empty label so the caller can show a muted dash instead.
		"adJoinLabel": func(status string) string {
			switch status {
			case model.ADJoinJoined:
				return "Joined"
			case model.ADJoinFailed:
				return "Join failed"
			case model.ADTrustBroken:
				return "Trust broken"
			default:
				return ""
			}
		},
		"adJoinBadgeClass": func(status string) string {
			switch status {
			case model.ADJoinJoined:
				return "ok"
			case model.ADJoinFailed, model.ADTrustBroken:
				return "bad"
			default:
				return "muted"
			}
		},
		"list": func(args ...any) []any { return args },
		"dict": func(args ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(args); i += 2 {
				k, _ := args[i].(string)
				m[k] = args[i+1]
			}
			return m
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"min": func(a, b int) int {
			if a < b {
				return a
			}
			return b
		},
		"derefInt": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		"lt":      func(a, b int) bool { return a < b },
		"toFloat": func(n int) float64 { return float64(n) },
		"div": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"mul": func(a, b float64) float64 { return a * b },
		"pct": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return int(float64(a) / float64(b) * 100)
		},
		// humanBytes formats a byte count as a short suffixed string.
		// Mirrors the JS upload-progress formatter so values match
		// between live progress and the saved record.
		"humanBytes": func(n int64) string {
			switch {
			case n < 1024:
				return fmt.Sprintf("%d B", n)
			case n < 1024*1024:
				return fmt.Sprintf("%.1f KiB", float64(n)/1024)
			case n < 1024*1024*1024:
				return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
			default:
				return fmt.Sprintf("%.2f GiB", float64(n)/(1024*1024*1024))
			}
		},
		// relTime formats a past timestamp as "just now", "5m ago",
		// "3h ago", etc. The "uploaded X ago" line in the payload card
		// uses it so operators see the recency at a glance without
		// parsing a full ISO timestamp.
		"relTime": func(t time.Time) string {
			if t.IsZero() {
				return "(unknown)"
			}
			d := time.Since(t)
			switch {
			case d < 0:
				return "in the future"
			case d < 90*time.Second:
				return "just now"
			case d < time.Hour:
				return fmt.Sprintf("%dm ago", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%dh ago", int(d.Hours()))
			case d < 30*24*time.Hour:
				return fmt.Sprintf("%dd ago", int(d.Hours())/24)
			default:
				return t.Format("2006-01-02")
			}
		},
	}
}

// render parses _layout + the named page, executes the layout, and
// passes a standard envelope that includes Title, the page Data, the
// current user, the brand, and any flash message.
func render(w http.ResponseWriter, req *http.Request, r Repos, page, title string, data any) {
	tmpl, err := template.New("").Funcs(funcsFor(req, r)).ParseFS(
		assetsFS, "templates/_layout.html", "templates/_icons.html",
		"templates/_action_picker.html", "templates/_pagination.html",
		"templates/"+page)
	if err != nil {
		http.Error(w, fmt.Sprintf("template parse: %v", err), http.StatusInternalServerError)
		return
	}
	user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
	brand, _ := r.Branding.Get(req.Context())
	kind, msg := readFlash(w, req)
	// Bootstrap-admin warning: the file holds a cleartext password
	// and is meant to be read once then deleted. If it is still
	// present after an operator has logged in we surface a banner
	// asking them to remove it. Cheap stat per page render -- the
	// file disappears the moment they comply.
	bootstrapPresent := false
	if r.DataDir != "" {
		if _, err := os.Stat(filepath.Join(r.DataDir, "admin-bootstrap.txt")); err == nil {
			bootstrapPresent = true
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	envelope := map[string]any{
		"Title":            title,
		"Data":             data,
		"User":             user,
		"Brand":            brand,
		"FlashKind":        kind,
		"FlashMsg":         msg,
		"Path":             req.URL.Path,
		"BootstrapPresent": bootstrapPresent,
	}
	if err := tmpl.ExecuteTemplate(w, "layout", envelope); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// PageInfo is the slice of pagination state a list template needs to
// render a "page N of M" footer with prev/next links.
type PageInfo struct {
	Current  int    // 1-based page number
	Total    int    // total number of pages (>= 1)
	Size     int    // page size
	TotalRow int    // total row count across all pages
	Offset   int    // 0-based row offset of this page
	End      int    // exclusive end offset (Offset + len(thisPage))
	PrevURL  string // empty if no previous page
	NextURL  string // empty if no next page
	Path     string // base path so the template can build other links
	// Links is the numbered page strip: first and last page plus a
	// window around the current one, with Gap entries where pages are
	// elided. Every URL preserves the request's other query parameters
	// (filter, size, …) so jumping pages never drops the active filter.
	Links []PageLink
	// SizeOptions are the selectable items-per-page choices, always
	// including the currently active size so the select shows it.
	SizeOptions []int
}

// PageLink is one entry in the numbered page strip; Gap entries render
// as an ellipsis between non-adjacent page numbers.
type PageLink struct {
	Num     int
	URL     string
	Current bool
	Gap     bool
}

// pageSizeChoices are the standard items-per-page options; paginate
// clamps any explicit ?size= to the same 10–500 envelope.
var pageSizeChoices = []int{10, 25, 50, 100, 250, 500}

// pageAndSize reads ?page= (1-based) and ?size= (clamped 10–500) with the
// given default size. Shared by paginate and by handlers that must compute
// a row offset before they know the total (SQL LIMIT/OFFSET lists, e.g.
// notifications).
func pageAndSize(req *http.Request, defaultSize int) (page, size int) {
	size = defaultSize
	if s := req.URL.Query().Get("size"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			size = v
		}
	}
	if size < 10 {
		size = 10
	}
	if size > 500 {
		size = 500
	}
	page = 1
	if p := req.URL.Query().Get("page"); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 {
			page = v
		}
	}
	return page, size
}

// paginate computes the slice bounds for the requested page given the
// total row count and a default page size. ?page=N (1-based) and
// ?size=N override the defaults; size is clamped to a reasonable range
// so a single client cannot ask for a million-row payload.
func paginate(req *http.Request, total, defaultSize int) PageInfo {
	page, size := pageAndSize(req, defaultSize)
	pages := (total + size - 1) / size
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		page = pages
	}
	offset := (page - 1) * size
	end := offset + size
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	q := req.URL.Query()
	pageURL := func(n int) string {
		q.Set("page", strconv.Itoa(n))
		return req.URL.Path + "?" + q.Encode()
	}
	prev := ""
	if page > 1 {
		prev = pageURL(page - 1)
	}
	next := ""
	if page < pages {
		next = pageURL(page + 1)
	}
	// Numbered strip: 1 … (page-2 .. page+2) … last.
	var links []PageLink
	last := 0
	for n := 1; n <= pages; n++ {
		if n != 1 && n != pages && (n < page-2 || n > page+2) {
			continue
		}
		if last != 0 && n != last+1 {
			links = append(links, PageLink{Gap: true})
		}
		links = append(links, PageLink{Num: n, URL: pageURL(n), Current: n == page})
		last = n
	}
	sizes := make([]int, 0, len(pageSizeChoices)+1)
	seenCur := false
	for _, s := range pageSizeChoices {
		if !seenCur && size < s {
			sizes = append(sizes, size)
			seenCur = true
		}
		if s == size {
			seenCur = true
		}
		sizes = append(sizes, s)
	}
	if !seenCur {
		sizes = append(sizes, size)
	}
	return PageInfo{
		Current: page, Total: pages, Size: size, TotalRow: total,
		Offset: offset, End: end,
		PrevURL: prev, NextURL: next,
		Path:  req.URL.Path,
		Links: links, SizeOptions: sizes,
	}
}

// renderPortalError writes a styled portal page for a HTTP error. Use
// for any 4xx/5xx the operator might see (404 catch-all, 500 from a
// busted handler, etc.) so the response stays inside the portal shell
// rather than Go's default plain-text http.Error output.
//
// Status code is written via a tiny wrapper that buffers WriteHeader
// until the first Write, because the underlying render() sets
// Content-Type before the body; setting WriteHeader first would
// commit headers and drop the Content-Type change.
func renderPortalError(w http.ResponseWriter, req *http.Request, r Repos, code int, heading, message, detail string) {
	sw := &statusWriter{ResponseWriter: w, status: code}
	render(sw, req, r, "error.html", heading, map[string]any{
		"Code":    code,
		"Heading": heading,
		"Message": message,
		"Detail":  detail,
	})
}

// statusWriter overrides the status code passed to the underlying
// ResponseWriter's first WriteHeader call. If no WriteHeader is
// called explicitly before a Write, the first Write commits our
// chosen status. Existing handlers that write 200 silently get
// upgraded to the configured status.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusWriter) WriteHeader(int) {
	if s.written {
		return
	}
	s.ResponseWriter.WriteHeader(s.status)
	s.written = true
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if !s.written {
		s.ResponseWriter.WriteHeader(s.status)
		s.written = true
	}
	return s.ResponseWriter.Write(p)
}

// pathID parses the {id} segment from req.PathValue.
func pathID(req *http.Request) (model.ID, error) {
	raw := req.PathValue("id")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid id %q", raw)
	}
	return model.ID(n), nil
}
