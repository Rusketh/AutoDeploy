package portal

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerLoadoutRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/loadouts", loadoutList(r))
		get("/portal/loadouts/new", loadoutFormNew(r))
		post("/portal/loadouts", loadoutCreate(r))
		get("/portal/loadouts/{id}/edit", loadoutEdit(r))
		post("/portal/loadouts/{id}", loadoutUpdate(r))
		post("/portal/loadouts/{id}/delete", loadoutDelete(r))
	}
}

func loadoutList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Loadouts.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		refs := map[model.ID]int{}
		for _, l := range v {
			n, _ := r.Loadouts.RefCount(req.Context(), l.ID)
			refs[l.ID] = n
		}
		render(w, req, r, "loadout_list.html", "Software loadouts", map[string]any{
			"Loadouts": v, "Refs": refs,
		})
	}
}

func loadoutFormNew(r Repos) http.HandlerFunc {
	return loadoutFormFor(r, model.SoftwareLoadout{}, true)
}

func loadoutFormFor(r Repos, l model.SoftwareLoadout, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		packages, _ := r.Software.List(req.Context())
		parents, _ := r.Loadouts.List(req.Context())
		title := "New loadout"
		if !isNew {
			title = "Edit loadout: " + l.Name
		}
		render(w, req, r, "loadout_form.html", title, map[string]any{
			"L": l, "IsNew": isNew, "Packages": packages, "Parents": parents,
		})
	}
}

func loadoutEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		l, err := r.Loadouts.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		loadoutFormFor(r, l, false)(w, req)
	}
}

func buildLoadoutFromForm(req *http.Request) (model.SoftwareLoadout, error) {
	if err := req.ParseForm(); err != nil {
		return model.SoftwareLoadout{}, err
	}
	l := model.SoftwareLoadout{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
	}
	if v := strings.TrimSpace(req.FormValue("parent_id")); v != "" && v != "0" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			id := model.ID(n)
			l.ParentID = &id
		}
	}
	for _, raw := range req.Form["pkg_id[]"] {
		pid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		// Each row submits pkg_id[], pkg_order[], pkg_optout[] — matched
		// positionally on order_index.
	}
	// We use a parallel-arrays approach: pkg_id[], pkg_order[], pkg_optout[]
	ids := req.Form["pkg_id[]"]
	orders := req.Form["pkg_order[]"]
	optouts := req.Form["pkg_optout[]"]
	for i, idStr := range ids {
		pid, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
		if err != nil || pid == 0 {
			continue
		}
		ord := int64(100)
		if i < len(orders) {
			if n, err := strconv.ParseInt(strings.TrimSpace(orders[i]), 10, 64); err == nil {
				ord = n
			}
		}
		opt := false
		if i < len(optouts) {
			opt = optouts[i] == "1"
		}
		l.Packages = append(l.Packages, model.SoftwareLoadoutPackage{
			PackageID: model.ID(pid), OrderValue: ord, OptOut: opt,
		})
	}
	return l, nil
}

func loadoutCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		l, err := buildLoadoutFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/loadouts/new", http.StatusFound)
			return
		}
		out, err := r.Loadouts.Create(req.Context(), l)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/loadouts/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Loadout created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/loadouts/%d/edit", out.ID), http.StatusFound)
	}
}

func loadoutUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		l, err := buildLoadoutFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/loadouts/%d/edit", id), http.StatusFound)
			return
		}
		l.ID = id
		if err := r.Loadouts.Update(req.Context(), l); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/loadouts/%d/edit", id), http.StatusFound)
	}
}

func loadoutDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := r.Loadouts.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/loadouts", http.StatusFound)
	}
}
