package portal

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerMachineGroupRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/groups", groupList(r))
		get("/portal/groups/new", groupFormNew(r))
		post("/portal/groups", groupCreate(r))
		get("/portal/groups/{id}/edit", groupEdit(r))
		post("/portal/groups/{id}", groupUpdate(r))
		post("/portal/groups/{id}/delete", groupDelete(r))
	}
}

func groupList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		groups, err := r.Groups.ListWithCounts(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "machine_group_list.html", "Machine groups", map[string]any{
			"Groups": groups,
			"Names":  groupNameMap(groups),
		})
	}
}

func groupFormNew(r Repos) http.HandlerFunc {
	return groupFormFor(r, model.MachineGroup{Kind: model.MachineGroupManual}, true)
}

func groupFormFor(r Repos, g model.MachineGroup, isNew bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		all, _ := r.Groups.List(req.Context())
		// Candidate children are every other group (a group can't nest itself).
		candidates := make([]model.MachineGroup, 0, len(all))
		for _, c := range all {
			if c.ID != g.ID {
				candidates = append(candidates, c)
			}
		}
		childSet := map[model.ID]bool{}
		for _, c := range g.Children {
			childSet[c] = true
		}
		var members []model.MachineRecord
		if !isNew {
			members, _ = r.Groups.MemberRecords(req.Context(), g.ID)
		}
		title := "New machine group"
		if !isNew {
			title = "Edit group: " + g.Name
		}
		render(w, req, r, "machine_group_form.html", title, map[string]any{
			"G": g, "IsNew": isNew,
			"Candidates": candidates, "ChildSet": childSet,
			"Members": members,
		})
	}
}

func groupEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		g, err := r.Groups.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		groupFormFor(r, g, false)(w, req)
	}
}

// buildGroupFromForm reads a machine group from the create/edit form. The
// dynamic filter fields are only read when kind=dynamic; child subgroups come
// from the multi-select.
func buildGroupFromForm(req *http.Request) (model.MachineGroup, error) {
	if err := req.ParseForm(); err != nil {
		return model.MachineGroup{}, err
	}
	g := model.MachineGroup{
		Name:        strings.TrimSpace(req.FormValue("name")),
		Description: strings.TrimSpace(req.FormValue("description")),
		Kind:        strings.TrimSpace(req.FormValue("kind")),
	}
	if g.Kind == model.MachineGroupDynamic {
		g.Filter = model.MachineFilter{
			NameRegex: strings.TrimSpace(req.FormValue("filter_name")),
			OS:        strings.TrimSpace(req.FormValue("filter_os")),
			OU:        strings.TrimSpace(req.FormValue("filter_ou")),
			OUSubtree: req.FormValue("filter_ou_subtree") == "1",
			MemberOf:  strings.TrimSpace(req.FormValue("filter_member_of")),
		}
	}
	for _, raw := range req.Form["child_ids"] {
		if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && n > 0 {
			g.Children = append(g.Children, model.ID(n))
		}
	}
	return g, nil
}

func groupCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		g, err := buildGroupFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/groups/new", http.StatusFound)
			return
		}
		out, err := r.Groups.Create(req.Context(), g)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/groups/new", http.StatusFound)
			return
		}
		logGroupEvent(req, r, "group.create", out)
		flash(w, "ok", "Group created.")
		http.Redirect(w, req, fmt.Sprintf("/portal/groups/%d/edit", out.ID), http.StatusFound)
	}
}

func groupUpdate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		g, err := buildGroupFromForm(req)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/groups/%d/edit", id), http.StatusFound)
			return
		}
		g.ID = id
		if err := r.Groups.Update(req.Context(), g); err != nil {
			flash(w, "err", err.Error())
		} else {
			logGroupEvent(req, r, "group.update", g)
			flash(w, "ok", "Saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/groups/%d/edit", id), http.StatusFound)
	}
}

func groupDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		g, _ := r.Groups.Get(req.Context(), id)
		if err := r.Groups.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
		} else {
			logGroupEvent(req, r, "group.delete", g)
			flash(w, "ok", "Deleted.")
		}
		http.Redirect(w, req, "/portal/groups", http.StatusFound)
	}
}

// logGroupEvent records a group change in the activity log (component
// "groups"). Best-effort; a logging failure never blocks the operation.
func logGroupEvent(req *http.Request, r Repos, action string, g model.MachineGroup) {
	if r.Logs == nil {
		return
	}
	user, _ := sessionUser(req, r)
	fields, _ := json.Marshal(map[string]any{"kind": g.Kind, "children": len(g.Children)})
	_ = r.Logs.Append(req.Context(), model.LogEvent{
		Component: "groups",
		Actor:     user.Username,
		Action:    action,
		Target:    "group:" + g.Name,
		Fields:    string(fields),
	})
}

// groupNameMap indexes groups by ID for templates that render child-group
// names from their IDs.
func groupNameMap(groups []model.MachineGroup) map[model.ID]string {
	out := make(map[model.ID]string, len(groups))
	for _, g := range groups {
		out[g.ID] = g.Name
	}
	return out
}
