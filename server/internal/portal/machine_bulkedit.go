package portal

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// machineBulkEditForm renders the bulk binding editor: pick machines with
// the same search/basket the bulk-operations form uses, then apply image /
// name template / OU / group changes to all of them in one go.
func machineBulkEditForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		images, _ := r.Images.List(req.Context())
		render(w, req, r, "machine_bulk_edit.html", "Bulk edit machines", map[string]any{
			"Images": images,
		})
	}
}

// machineBulkEditSubmit applies the edit. Each field is opt-in ("change
// this") so untouched fields are left exactly as they are per machine.
func machineBulkEditSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var machineIDs []model.ID
		for _, s := range req.Form["machine_ids"] {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				continue
			}
			machineIDs = append(machineIDs, model.ID(n))
		}

		var edit model.BindingEdit
		switch req.FormValue("image_mode") {
		case "set":
			n, err := strconv.ParseInt(req.FormValue("image_id"), 10, 64)
			if err != nil || n <= 0 {
				http.Error(w, "choose an image to set", http.StatusBadRequest)
				return
			}
			if _, err := r.Images.Get(req.Context(), model.ID(n)); err != nil {
				http.Error(w, "image not found", http.StatusBadRequest)
				return
			}
			id := model.ID(n)
			edit.ImageID = &id
		case "clear":
			zero := model.ID(0)
			edit.ImageID = &zero
		}
		if req.FormValue("set_name") == "1" {
			name := strings.TrimSpace(req.FormValue("machine_name"))
			edit.MachineName = &name
		}
		if req.FormValue("set_ou") == "1" {
			ou := strings.TrimSpace(req.FormValue("target_ou"))
			edit.TargetOU = &ou
		}
		edit.AddGroups = splitGroups(req.FormValue("add_groups"))
		edit.RemoveGroups = splitGroups(req.FormValue("remove_groups"))

		updated, err := r.Inventory.BulkEditBindings(req.Context(), machineIDs, edit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Logs != nil {
			user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
			fields, _ := json.Marshal(map[string]any{
				"machines": len(machineIDs), "updated": updated, "set": edit,
			})
			_ = r.Logs.Append(req.Context(), model.LogEvent{
				Component: "inventory",
				Actor:     user.Username,
				Action:    "binding.bulk_edit",
				Target:    "machines:" + strconv.Itoa(len(machineIDs)),
				Fields:    string(fields),
			})
		}
		flash(w, "ok", "Updated "+strconv.Itoa(updated)+" of "+strconv.Itoa(len(machineIDs))+" machine bindings")
		http.Redirect(w, req, "/portal/machines", http.StatusFound)
	}
}

// splitGroups parses a comma-separated group list, trimming blanks.
func splitGroups(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}
