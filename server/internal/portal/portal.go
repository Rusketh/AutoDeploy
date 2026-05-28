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
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
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
	Loadouts  *model.SoftwareLoadoutRepo
	Images    *model.ImageRepo
	Inventory *model.InventoryRepo
	BitLocker *model.BitLockerRepo
	Bulk      *model.BulkRepo
	Logs      *model.LogRepo
	Users     *auth.Repo
	Settings  *auth.SettingsRepo
	Branding  *branding.Repo
	Mirrors   *model.PayloadMirrorRepo
	Resolver  *resolve.Resolver
	Blobs     *storage.BlobStore
	AD        *addomain.Service
	// SecretsBox is unused at the portal layer but kept here so the
	// bundle matches the api one-for-one if we ever want to swap.
	SecretsBox *secrets.Box
}

const (
	sessionCookieName = "autodeploy_session"
	flashCookieName   = "autodeploy_flash"
)

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
	get("/portal/", dashboardPage(r))

	registerISORoutes(get, post, r)
	registerUnattendRoutes(get, post, r)
	registerDriverRoutes(get, post, r)
	registerSoftwareRoutes(get, post, r)
	registerLoadoutRoutes(get, post, r)
	registerImageRoutes(get, post, r)
	registerInventoryRoutes(get, post, r)
	registerBulkRoutes(get, post, r)
	registerLogsRoutes(get, post, r)
	registerSettingsRoutes(get, post, r)
	mirrorRoutes(get, post, r)

	return nil
}

// requireSession redirects to /portal/login if the request has no valid
// session cookie. The "next" parameter remembers where the user was
// trying to go.
func requireSession(r Repos, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		_, ok := sessionUser(req, r)
		if !ok {
			to := url.Values{}
			to.Set("next", req.URL.RequestURI())
			http.Redirect(w, req, "/portal/login?"+to.Encode(), http.StatusFound)
			return
		}
		h(w, req)
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
			Secure:   req.TLS != nil,
		})
		next := req.FormValue("next")
		if next == "" || !strings.HasPrefix(next, "/portal") {
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

// dashboardPage renders the index with a few useful counters.
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
		if l, err := r.Inventory.List(req.Context()); err == nil {
			c.Machines = len(l)
		}
		render(w, req, r, "index.html", "Dashboard", map[string]any{"Counts": c})
	}
}

// Template helper funcs available to every page.
func funcsFor(req *http.Request, r Repos) template.FuncMap {
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
		"join":  strings.Join,
		"hasItems": func(s any) bool {
			switch v := s.(type) {
			case nil:
				return false
			case []any:
				return len(v) > 0
			}
			return true
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
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
	}
}

// render parses _layout + the named page, executes the layout, and
// passes a standard envelope that includes Title, the page Data, the
// current user, the brand, and any flash message.
func render(w http.ResponseWriter, req *http.Request, r Repos, page, title string, data any) {
	tmpl, err := template.New("").Funcs(funcsFor(req, r)).ParseFS(
		assetsFS, "templates/_layout.html", "templates/"+page)
	if err != nil {
		http.Error(w, fmt.Sprintf("template parse: %v", err), http.StatusInternalServerError)
		return
	}
	user, _ := sessionUser(req, r)
	brand, _ := r.Branding.Get(req.Context())
	kind, msg := readFlash(w, req)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	envelope := map[string]any{
		"Title":      title,
		"Data":       data,
		"User":       user,
		"Brand":      brand,
		"FlashKind":  kind,
		"FlashMsg":   msg,
		"Path":       req.URL.Path,
	}
	if err := tmpl.ExecuteTemplate(w, "layout", envelope); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
