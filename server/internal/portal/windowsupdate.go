package portal

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

func registerWindowsUpdateRoutes(get, post func(string, http.HandlerFunc), r Repos) {
	if r.Updates == nil {
		return
	}
	get("/portal/updates", wuList(r))
	get("/portal/updates/new", wuFormNew(r))
	post("/portal/updates", wuCreate(r))
	get("/portal/updates/deploy", wuDeployForm(r))
	post("/portal/updates/deploy", wuDeployCreate(r))
	get("/portal/updates/deployments/{id}", wuDeploymentDetail(r))
	get("/portal/updates/{id}/edit", wuFormEdit(r))
	post("/portal/updates/{id}", wuSave(r))
	post("/portal/updates/{id}/delete", wuDelete(r))
	get("/portal/updates/{id}/compliance", wuCompliance(r))
}

func wuList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		updates, err := r.Updates.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render(w, req, r, "windowsupdate_list.html", "Windows Updates", map[string]any{
			"Updates": updates,
		})
	}
}

func wuFormNew(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "windowsupdate_form.html", "New Windows Update", map[string]any{
			"Update": model.WindowsUpdate{SupersedesJSON: "[]"},
			"IsNew":  true,
		})
	}
}

func wuCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u := model.WindowsUpdate{
			KBNumber:       strings.TrimSpace(req.FormValue("kb_number")),
			Title:          strings.TrimSpace(req.FormValue("title")),
			Description:    strings.TrimSpace(req.FormValue("description")),
			OSFilter:       strings.TrimSpace(req.FormValue("os_filter")),
			Severity:       strings.TrimSpace(req.FormValue("severity")),
			SupersedesJSON: strings.TrimSpace(req.FormValue("supersedes_json")),
			RebootAfter:    req.FormValue("reboot_after") == "1",
		}
		if u.SupersedesJSON == "" {
			u.SupersedesJSON = "[]"
		}
		created, err := r.Updates.Create(req.Context(), u)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		flash(w, "ok", "Update "+created.KBNumber+" created")
		http.Redirect(w, req, "/portal/updates/"+idStr(created.ID)+"/edit", http.StatusFound)
	}
}

func wuFormEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		render(w, req, r, "windowsupdate_form.html", "Edit "+u.KBNumber, map[string]any{
			"Update": u,
			"IsNew":  false,
		})
	}
}

func wuSave(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		u.KBNumber = strings.TrimSpace(req.FormValue("kb_number"))
		u.Title = strings.TrimSpace(req.FormValue("title"))
		u.Description = strings.TrimSpace(req.FormValue("description"))
		u.OSFilter = strings.TrimSpace(req.FormValue("os_filter"))
		u.Severity = strings.TrimSpace(req.FormValue("severity"))
		u.SupersedesJSON = strings.TrimSpace(req.FormValue("supersedes_json"))
		u.RebootAfter = req.FormValue("reboot_after") == "1"
		if u.SupersedesJSON == "" {
			u.SupersedesJSON = "[]"
		}
		if err := r.Updates.Update(req.Context(), u); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		flash(w, "ok", "Update "+u.KBNumber+" saved")
		http.Redirect(w, req, "/portal/updates/"+idStr(u.ID)+"/edit", http.StatusFound)
	}
}

func wuDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.Updates.Delete(req.Context(), id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		flash(w, "ok", "Update deleted")
		http.Redirect(w, req, "/portal/updates", http.StatusFound)
	}
}

func wuCompliance(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		summary, err := r.Updates.ComplianceForUpdate(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Per-machine detail for the compliance view.
		machines, _ := r.Updates.PreviewTargets(req.Context(), model.BulkTarget{}, u.OSFilter)
		type machineStatus struct {
			Machine model.MachineRecord
			Name    string
			Status  string
		}
		var details []machineStatus
		for _, m := range machines {
			statuses, _ := r.Updates.ListMachineStatuses(req.Context(), m.ID)
			st := "unknown"
			for _, s := range statuses {
				if s.KBNumber == u.KBNumber {
					st = s.Status
					break
				}
			}
			bind, _ := r.Inventory.GetBinding(req.Context(), m.ID)
			details = append(details, machineStatus{
				Machine: m,
				Name:    bind.MachineName,
				Status:  st,
			})
		}
		render(w, req, r, "windowsupdate_compliance.html", u.KBNumber+" compliance", map[string]any{
			"Update":   u,
			"Summary":  summary,
			"Machines": details,
		})
	}
}

func wuDeployForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		updates, _ := r.Updates.List(req.Context())
		render(w, req, r, "windowsupdate_deploy.html", "Deploy Updates", map[string]any{
			"Updates": updates,
		})
	}
}

func wuDeployCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var updateIDs []model.ID
		for _, s := range req.Form["update_ids"] {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				continue
			}
			updateIDs = append(updateIDs, model.ID(n))
		}
		var machineIDs []model.ID
		for _, s := range req.Form["machine_ids"] {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				continue
			}
			machineIDs = append(machineIDs, model.ID(n))
		}
		user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		d := model.UpdateDeployment{
			UpdateIDs: updateIDs,
			Target: model.BulkTarget{
				NameRegex:  strings.TrimSpace(req.FormValue("name_regex")),
				OU:         strings.TrimSpace(req.FormValue("ou")),
				Group:      strings.TrimSpace(req.FormValue("group")),
				MachineIDs: machineIDs,
			},
			OSFilter:  strings.TrimSpace(req.FormValue("os_filter")),
			CreatedBy: user.Username,
		}
		dep, _, err := r.Updates.CreateDeployment(req.Context(), d)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		flash(w, "ok", "Deployment created")
		http.Redirect(w, req, "/portal/updates/deployments/"+idStr(dep.ID), http.StatusFound)
	}
}

func wuDeploymentDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dep, jobs, err := r.Updates.GetDeployment(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		// Build lookup maps for display.
		type enrichedJob struct {
			model.UpdateDeploymentJob
			MachineName string
			KBNumber    string
		}
		enriched := make([]enrichedJob, len(jobs))
		for i, j := range jobs {
			enriched[i].UpdateDeploymentJob = j
			bind, _ := r.Inventory.GetBinding(req.Context(), j.MachineID)
			enriched[i].MachineName = bind.MachineName
			if u, uerr := r.Updates.Get(req.Context(), j.UpdateID); uerr == nil {
				enriched[i].KBNumber = u.KBNumber
			}
		}
		// Summarize job statuses.
		counts := map[string]int{}
		for _, j := range jobs {
			counts[j.Status]++
		}
		targetJSON, _ := json.MarshalIndent(dep.Target, "", "  ")
		render(w, req, r, "windowsupdate_deployment_detail.html", "Deployment #"+idStr(dep.ID), map[string]any{
			"Deployment": dep,
			"Jobs":       enriched,
			"Counts":     counts,
			"TargetJSON": string(targetJSON),
		})
	}
}

func idStr(id model.ID) string {
	return strconv.FormatInt(int64(id), 10)
}
