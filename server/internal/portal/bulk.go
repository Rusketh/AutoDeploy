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
	registerBulkRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/bulk", bulkList(r))
		get("/portal/bulk/new", bulkFormNew(r))
		post("/portal/bulk/preview", bulkPreview(r))
		post("/portal/bulk", bulkCreate(r))
		get("/portal/bulk/{id}", bulkDetail(r))
	}
}

func bulkList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ops, err := r.Bulk.ListOperations(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "bulk_list.html", "Bulk operations", map[string]any{"Ops": ops})
	}
}

func bulkFormNew(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		pkgs, _ := r.Software.List(req.Context())
		render(w, req, r, "bulk_form.html", "New bulk operation", map[string]any{
			"Target": model.BulkTarget{}, "Preview": nil, "Packages": pkgs,
		})
	}
}

// bulkPreview resolves the target selection from the form and re-renders
// the form with the resolved machines listed.
func bulkPreview(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t := model.BulkTarget{
			NameRegex: strings.TrimSpace(req.FormValue("name_regex")),
			OU:        strings.TrimSpace(req.FormValue("ou")),
			Group:     strings.TrimSpace(req.FormValue("group")),
		}
		machines, err := r.Bulk.PreviewTargets(req.Context(), t)
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		pkgs, _ := r.Software.List(req.Context())
		render(w, req, r, "bulk_form.html", "New bulk operation", map[string]any{
			"Target":   t,
			"Action":   req.FormValue("action"),
			"Preview":  machines,
			"Packages": pkgs,
		})
	}
}

func bulkCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		t := model.BulkTarget{
			NameRegex: strings.TrimSpace(req.FormValue("name_regex")),
			OU:        strings.TrimSpace(req.FormValue("ou")),
			Group:     strings.TrimSpace(req.FormValue("group")),
		}
		action := req.FormValue("action")
		var payload string
		switch action {
		case model.BulkActionRename:
			name := strings.TrimSpace(req.FormValue("rename_new_name"))
			find := strings.TrimSpace(req.FormValue("rename_find"))
			switch {
			case name != "":
				b, _ := json.Marshal(map[string]string{"new_name": name})
				payload = string(b)
			case find != "":
				b, _ := json.Marshal(map[string]string{
					"rename_find":    find,
					"rename_replace": req.FormValue("rename_replace"),
				})
				payload = string(b)
			default:
				flash(w, "err", "Rename requires either a new name or a find pattern.")
				http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
				return
			}
		case model.BulkActionScript:
			shell := req.FormValue("script_shell")
			body := req.FormValue("script_body")
			if body == "" {
				flash(w, "err", "Script body required.")
				http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
				return
			}
			b, _ := json.Marshal(map[string]string{"shell": shell, "body": body})
			payload = string(b)
		case model.BulkActionSoftwarePush:
			// Operator picks a software package; the agent fetches its
			// install items (plus dependencies) and runs them.
			pid, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64)
			if err != nil || pid <= 0 {
				flash(w, "err", "Choose a software package to push.")
				http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
				return
			}
			b, _ := json.Marshal(map[string]int64{"package_id": pid})
			payload = string(b)
		default:
			flash(w, "err", "Unknown action.")
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		user, _ := sessionUser(req, r)
		op, _, err := r.Bulk.CreateOperation(req.Context(), model.BulkOperation{
			Action: action, Payload: payload, Target: t, CreatedBy: user.Username,
		})
		if err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		flash(w, "ok", "Operation queued.")
		http.Redirect(w, req, fmt.Sprintf("/portal/bulk/%d", op.ID), http.StatusFound)
	}
}

func bulkDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		op, jobs, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		render(w, req, r, "bulk_detail.html", "Bulk operation #"+pathStr(req), map[string]any{
			"Op": op, "Jobs": jobs,
		})
	}
}

func pathStr(req *http.Request) string { return req.PathValue("id") }
