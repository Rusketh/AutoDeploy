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
		get("/portal/bulk/search", bulkSearch(r))
		post("/portal/bulk", bulkCreate(r))
		get("/portal/bulk/{id}", bulkDetail(r))
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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
		imgs, _ := r.Images.List(req.Context())
		render(w, req, r, "bulk_form.html", "New bulk operation", map[string]any{
			"Packages": pkgs,
			"Images":   imgs,
		})
	}
}

// bulkSearch returns machines matching the filter as JSON, for the
// "build a selection" UI. The filter is a SEARCH TOOL -- the operator adds
// individual results to a selection basket; the basket (machine_ids) is
// what the action actually runs against, not the filter itself.
func bulkSearch(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		t := model.BulkTarget{
			NameRegex: strings.TrimSpace(req.URL.Query().Get("name_regex")),
			OU:        strings.TrimSpace(req.URL.Query().Get("ou")),
			Group:     strings.TrimSpace(req.URL.Query().Get("group")),
		}
		machines, err := r.Bulk.PreviewTargets(req.Context(), t)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, err.Error())
			return
		}
		type item struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			UUID    string `json:"uuid"`
			Make    string `json:"make"`
			Product string `json:"product"`
		}
		out := make([]item, 0, len(machines))
		for _, m := range machines {
			b, _ := r.Inventory.GetBinding(req.Context(), m.ID)
			out = append(out, item{
				ID: int64(m.ID), Name: b.MachineName, UUID: m.SystemUUID,
				Make: m.SystemManufacturer, Product: m.SystemProduct,
			})
		}
		writeJSONResp(w, out)
	}
}

func bulkCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The operator built an explicit selection basket (machine_ids);
		// that -- not the search filter -- is what the action runs on.
		var ids []model.ID
		for _, s := range req.Form["machine_ids"] {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
				ids = append(ids, model.ID(n))
			}
		}
		if len(ids) == 0 {
			flash(w, "err", "Add at least one machine to the selection.")
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		t := model.BulkTarget{MachineIDs: ids}
		action := req.FormValue("action")
		var payload string
		switch action {
		case model.BulkActionRename:
			// Bulk rename is fleet-only: a regex find/replace applied to
			// each machine's current name. Renaming one machine to a
			// literal name is done from that machine's own page.
			find := strings.TrimSpace(req.FormValue("rename_find"))
			if find == "" {
				flash(w, "err", "Bulk rename requires a find pattern. To rename one machine to a literal name, use its machine page.")
				http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
				return
			}
			b, _ := json.Marshal(map[string]string{
				"rename_find":    find,
				"rename_replace": req.FormValue("rename_replace"),
			})
			payload = string(b)
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
		case model.BulkActionReimage:
			// Image to deploy is carried on the operation; job payload is "{}".
			payload = "{}"
		default:
			flash(w, "err", "Unknown action.")
			http.Redirect(w, req, "/portal/bulk/new", http.StatusFound)
			return
		}
		user, _ := sessionUser(req, r)
		op, _, err := r.Bulk.CreateOperation(req.Context(), model.BulkOperation{
			Action: action, Payload: payload, Target: t, CreatedBy: user.Username,
			ReimageImageID: reimageImageIDFromForm(req),
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
