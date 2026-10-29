package portal

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	mirrorRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		if r.Mirrors == nil {
			return
		}
		get("/portal/mirrors", mirrorList(r))
		get("/portal/mirrors/new", mirrorForm(r, model.PayloadMirror{Healthy: true}, true))
		post("/portal/mirrors", mirrorCreate(r))
		get("/portal/mirrors/{id}/edit", mirrorEdit(r))
		post("/portal/mirrors/{id}", mirrorUpdate(r))
		post("/portal/mirrors/{id}/delete", mirrorDelete(r))
		post("/portal/mirrors/{id}/health", mirrorSetHealth(r))
	}
}

// mirrorRoutes is registered from portal.Register via the same hook
// pattern the rest of the portal entities use. Adding the var to the
// stubs file gives every entity its placeholder until a real file
// overrides it.
var mirrorRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {}

func mirrorList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Mirrors.List(req.Context())
		if err != nil {
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		search := newSearchBox(req, "Search mirrors", "Search payload mirrors")
		shown := filterByQuery(v, search.Query, func(m model.PayloadMirror) []string {
			return []string{m.Name, m.Description, m.Site, m.BaseURL}
		})
		render(w, req, r, "mirror_list.html", "Payload mirrors", map[string]any{
			"Mirrors": shown,
			"Search":  search.counted(len(shown), len(v), "mirrors"),
		})
	}
}

func mirrorForm(r Repos, m model.PayloadMirror, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		mirrorFormPage(w, req, r, m, isNew, nil)
	}
}

// mirrorFormPage renders the mirror form; with fe set it is a rejected
// save re-rendered with the submitted values (422).
func mirrorFormPage(w http.ResponseWriter, req *http.Request, r Repos, m model.PayloadMirror, isNew bool, fe *FormError) {
	title := "New payload mirror"
	if !isNew {
		title = "Edit mirror: " + m.Name
	}
	renderForm(w, req, r, "mirror_form.html", title, fe, map[string]any{
		"M": m, "IsNew": isNew,
	})
}

func mirrorEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		m, err := r.Mirrors.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		mirrorForm(r, m, false)(w, req)
	}
}

func buildMirror(req *http.Request) (model.PayloadMirror, error) {
	if err := req.ParseForm(); err != nil {
		return model.PayloadMirror{}, err
	}
	prio, _ := strconv.Atoi(strings.TrimSpace(req.FormValue("priority")))
	if prio == 0 {
		prio = 100
	}
	return model.PayloadMirror{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
		BaseURL:     strings.TrimSpace(req.FormValue("base_url")),
		Site:        strings.TrimSpace(req.FormValue("site")),
		Priority:    prio,
		Healthy:     req.FormValue("healthy") == "1",
	}, nil
}

func mirrorCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		m, err := buildMirror(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		m.Healthy = true // a freshly created mirror starts healthy
		out, err := r.Mirrors.Create(req.Context(), m)
		if err != nil {
			mirrorFormPage(w, req, r, m, true, newFormError(req, model.KindMirror, err))
			return
		}
		flash(w, "ok", "Mirror added.")
		http.Redirect(w, req, fmt.Sprintf("/portal/mirrors/%d/edit", out.ID), http.StatusFound)
	}
}

func mirrorUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		m, err := buildMirror(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		m.ID = id
		if err := r.Mirrors.Update(req.Context(), m); err != nil {
			mirrorFormPage(w, req, r, m, false, newFormError(req, model.KindMirror, err))
			return
		}
		flash(w, "ok", "Saved.")
		http.Redirect(w, req, fmt.Sprintf("/portal/mirrors/%d/edit", id), http.StatusFound)
	}
}

func mirrorDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		name := nameBeforeDelete(req, r, model.KindMirror, id)
		deleteFlash(w, req, model.KindMirror, name, r.Mirrors.Delete(req.Context(), id))
		http.Redirect(w, req, "/portal/mirrors", http.StatusFound)
	}
}

func mirrorSetHealth(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		healthy := req.FormValue("healthy") == "1"
		if err := r.Mirrors.SetHealth(req.Context(), id, healthy); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Mirror state updated.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/mirrors/%d/edit", id), http.StatusFound)
	}
}
