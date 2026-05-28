// Package portal serves the management portal HTML. Phase 1 is a minimal
// server-rendered UI that lists artifacts and images and lets an operator
// view an image's resolved configuration. CRUD forms wire to the JSON API
// at /api/v1/* (see internal/api).
//
// The portal is intentionally simple — html/template plus a small bit of
// HTMX-style fetch behaviour. A richer SPA can replace it later if needed.
package portal

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
)

//go:embed templates/*.html assets/*
var assetsFS embed.FS

// Repos mirrors the dependency bundle the API takes. The portal renders
// data directly via repositories rather than going through HTTP to itself,
// to keep the dev experience snappy and the call graph simple.
type Repos struct {
	ISOs     *model.ISORepo
	Unattend *model.UnattendRepo
	Drivers  *model.DriverPackageRepo
	Software *model.SoftwarePackageRepo
	Images   *model.ImageRepo
	Resolver *resolve.Resolver
}

var funcs = template.FuncMap{
	"derefID": func(p *model.ID) string {
		if p == nil {
			return "—"
		}
		return strconv.FormatInt(int64(*p), 10)
	},
}

// Register mounts the portal on mux under /portal/* and serves an index
// redirect at /portal.
func Register(mux *http.ServeMux, r Repos) error {
	assets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return err
	}
	mux.Handle("GET /portal/assets/", http.StripPrefix("/portal/assets/", http.FileServer(http.FS(assets))))

	mux.HandleFunc("GET /portal", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/portal/", http.StatusFound)
	})
	mux.HandleFunc("GET /portal/", page("index.html", "AutoDeploy", func(*http.Request) (any, error) {
		return nil, nil
	}))

	mux.HandleFunc("GET /portal/isos", page("isos.html", "ISOs", func(req *http.Request) (any, error) {
		return r.ISOs.List(req.Context())
	}))
	mux.HandleFunc("GET /portal/unattends", page("unattends.html", "Unattends", func(req *http.Request) (any, error) {
		return r.Unattend.List(req.Context())
	}))
	mux.HandleFunc("GET /portal/drivers", page("drivers.html", "Driver packages", func(req *http.Request) (any, error) {
		return r.Drivers.List(req.Context())
	}))
	mux.HandleFunc("GET /portal/software", page("software.html", "Software packages", func(req *http.Request) (any, error) {
		return r.Software.List(req.Context())
	}))
	mux.HandleFunc("GET /portal/images", page("images.html", "Images", func(req *http.Request) (any, error) {
		return r.Images.List(req.Context())
	}))

	mux.HandleFunc("GET /portal/images/{id}/resolved", func(w http.ResponseWriter, req *http.Request) {
		id, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		resolved, err := r.Resolver.Resolve(req.Context(), model.ID(id))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page("resolved.html", "Resolved configuration", func(*http.Request) (any, error) {
			return resolved, nil
		})(w, req)
	})

	return nil
}

// page returns an http.HandlerFunc that loads templates/_layout.html and
// templates/<file> for each request, executes the layout, and passes
// {Title, Items} to the template. Per-request parse keeps each page
// independent (so two pages can both define a "body" block).
func page(file, title string, load func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		tmpl, err := template.New("").Funcs(funcs).ParseFS(
			assetsFS, "templates/_layout.html", "templates/"+file)
		if err != nil {
			http.Error(w, fmt.Sprintf("template parse: %v", err), http.StatusInternalServerError)
			return
		}
		var items any
		if load != nil {
			items, err = load(req)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.ExecuteTemplate(w, "layout", map[string]any{
			"Title": title,
			"Items": items,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
