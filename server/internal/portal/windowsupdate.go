package portal

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

func registerWindowsUpdateRoutes(get, post func(string, http.HandlerFunc), r Repos) {
	if r.Updates == nil {
		return
	}
	get("/portal/updates", wuList(r))
	get("/portal/updates/new", wuFormNew(r))
	get("/portal/updates/import", wuImportPage(r))
	post("/portal/updates", wuCreate(r))
	get("/portal/update-deployments/new", wuDeployForm(r))
	post("/portal/update-deployments", wuDeployCreate(r))
	get("/portal/update-deployments/{id}", wuDeploymentDetail(r))
	get("/portal/updates/{id}/edit", wuFormEdit(r))
	post("/portal/updates/{id}", wuSave(r))
	post("/portal/updates/{id}/delete", wuDelete(r))
	get("/portal/updates/{id}/compliance", wuCompliance(r))
}

func wuList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		updates, err := r.Updates.List(req.Context())
		if err != nil {
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		compliance, _ := r.Updates.AllComplianceSummaries(req.Context())
		search := newSearchBox(req, "Search updates", "Search Windows Updates")
		shown := filterByQuery(updates, search.Query, func(u model.WindowsUpdate) []string {
			return []string{u.KBNumber, u.Title, u.OSFilter, u.Severity}
		})
		render(w, req, r, "windowsupdate_list.html", "Windows Updates", map[string]any{
			"Updates": shown, "Compliance": compliance,
			"Search": search.counted(len(shown), len(updates), "updates"),
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
			failPage(w, req, r, http.StatusBadRequest, err)
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
			AutoDeploy:     req.FormValue("auto_deploy") == "1",
		}
		if u.SupersedesJSON == "" {
			u.SupersedesJSON = "[]"
		}
		created, err := r.Updates.Create(req.Context(), u)
		if err != nil {
			wuFormRejected(w, req, r, u, true, err)
			return
		}
		flash(w, "ok", "Update "+created.KBNumber+" created")
		http.Redirect(w, req, "/portal/updates/"+idStr(created.ID)+"/edit", http.StatusFound)
	}
}

// wuImportPage renders the Microsoft Update Catalog import tool. All the
// work (search, import kickoff, progress) runs client-side against the
// authenticated /api/v1/mscatalog and /api/v1/updates/import endpoints —
// nothing is downloaded without an explicit operator click.
func wuImportPage(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		render(w, req, r, "windowsupdate_import.html", "Import from Microsoft", map[string]any{"Query": searchQuery(req)})
	}
}

// wuFormRejected re-renders the update form with the submitted values and
// the save error (422). An update is identified by its KB number, so a
// uniqueness conflict lands on that field.
func wuFormRejected(w http.ResponseWriter, req *http.Request, r Repos, u model.WindowsUpdate, isNew bool, err error) {
	fe := newFormError(req, model.KindUpdate, err)
	if msg := fe.Fields["name"]; msg != "" {
		delete(fe.Fields, "name")
		fe.Fields["kb_number"] = "Another update already has this KB number."
	}
	title := "New Windows Update"
	if !isNew {
		title = "Edit " + u.KBNumber
	}
	renderForm(w, req, r, "windowsupdate_form.html", title, fe, map[string]any{
		"Update": u,
		"IsNew":  isNew,
	})
}

func wuFormEdit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
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
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		u.KBNumber = strings.TrimSpace(req.FormValue("kb_number"))
		u.Title = strings.TrimSpace(req.FormValue("title"))
		u.Description = strings.TrimSpace(req.FormValue("description"))
		u.OSFilter = strings.TrimSpace(req.FormValue("os_filter"))
		u.Severity = strings.TrimSpace(req.FormValue("severity"))
		u.SupersedesJSON = strings.TrimSpace(req.FormValue("supersedes_json"))
		u.RebootAfter = req.FormValue("reboot_after") == "1"
		u.AutoDeploy = req.FormValue("auto_deploy") == "1"
		if u.SupersedesJSON == "" {
			u.SupersedesJSON = "[]"
		}
		if err := r.Updates.Update(req.Context(), u); err != nil {
			wuFormRejected(w, req, r, u, false, err)
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
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		name := nameBeforeDelete(req, r, model.KindUpdate, id)
		if err := r.Updates.Delete(req.Context(), id); err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		deleteFlash(w, req, model.KindUpdate, name, nil)
		http.Redirect(w, req, "/portal/updates", http.StatusFound)
	}
}

func wuCompliance(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		u, err := r.Updates.Get(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		summary, err := r.Updates.ComplianceForUpdate(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		// Per-machine detail for the compliance view.
		machines, _ := r.Updates.PreviewTargets(req.Context(), model.BulkTarget{}, u.OSFilter)
		type machineStatus struct {
			Machine model.MachineRecord
			Name    string
			Status  string
		}
		// One query for every machine's status on this KB and one for names,
		// instead of two queries per machine.
		byMachine, _ := r.Updates.StatusesForKB(req.Context(), u.KBNumber)
		names := namesFor(req, r)
		var details []machineStatus
		for _, m := range machines {
			st := "unknown"
			if s, ok := byMachine[m.ID]; ok {
				st = s
			}
			details = append(details, machineStatus{
				Machine: m,
				Name:    names.Lookup(model.KindMachine, m.ID).Label(m.ID),
				Status:  st,
			})
		}
		search := newSearchBox(req, "Search machines", "Search machines")
		shown := filterByQuery(details, search.Query, func(d machineStatus) []string {
			return []string{d.Name, d.Machine.SystemUUID, d.Status}
		})
		render(w, req, r, "windowsupdate_compliance.html", u.KBNumber+" compliance", map[string]any{
			"Update":   u,
			"Summary":  summary,
			"Machines": shown,
			"Search":   search.counted(len(shown), len(details), "machines"),
		})
	}
}

func wuDeployForm(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		updates, _ := r.Updates.List(req.Context())
		var groups []model.MachineGroup
		if r.Groups != nil {
			groups, _ = r.Groups.List(req.Context())
		}
		render(w, req, r, "windowsupdate_deploy.html", "Deploy Updates", map[string]any{
			"Updates": updates,
			"Groups":  groups,
		})
	}
}

// formIDs collects positive IDs from a repeated form field. Each value may
// itself be a comma/space-separated list, so both the picker's repeated
// machine_ids inputs and the old single "1,2,3" box keep working.
func formIDs(values []string) []model.ID {
	var out []model.ID
	seen := map[model.ID]bool{}
	for _, v := range values {
		for _, s := range strings.FieldsFunc(v, func(c rune) bool { return c == ',' || c == ' ' || c == ';' }) {
			n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			if err != nil || n <= 0 || seen[model.ID(n)] {
				continue
			}
			seen[model.ID(n)] = true
			out = append(out, model.ID(n))
		}
	}
	return out
}

func wuDeployCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		updateIDs := formIDs(req.Form["update_ids"])
		// machine_ids: repeated hidden inputs from the picker basket, or the
		// legacy comma-separated "1,2,3" value — formIDs accepts both.
		machineIDs := formIDs(req.Form["machine_ids"])
		var groupID model.ID
		if ids := formIDs(req.Form["group_id"]); len(ids) > 0 {
			groupID = ids[0]
		}
		user, _ := req.Context().Value(ctxKeyUser{}).(auth.User)
		d := model.UpdateDeployment{
			UpdateIDs: updateIDs,
			Target: model.BulkTarget{
				NameRegex:  strings.TrimSpace(req.FormValue("name_regex")),
				OU:         strings.TrimSpace(req.FormValue("ou")),
				Group:      strings.TrimSpace(req.FormValue("group")),
				GroupID:    groupID,
				MachineIDs: machineIDs,
			},
			OSFilter:  strings.TrimSpace(req.FormValue("os_filter")),
			CreatedBy: user.Username,
		}
		dep, jobs, err := r.Updates.CreateDeployment(req.Context(), d)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		kbs := notify.UpdateKBNumbers(req.Context(), r.Updates, dep.UpdateIDs)
		r.Emitter.Emit(req.Context(), notify.UpdateDeployedEvent(dep, len(jobs), kbs...))
		if len(kbs) > 0 {
			flash(w, "ok", "Deployment created: "+strings.Join(kbs, ", ")+" to "+strconv.Itoa(len(jobs))+" machine job(s).")
		} else {
			flash(w, "ok", "Deployment created")
		}
		http.Redirect(w, req, "/portal/update-deployments/"+idStr(dep.ID), http.StatusFound)
	}
}

func wuDeploymentDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		dep, jobs, err := r.Updates.GetDeployment(req.Context(), id)
		if err != nil {
			failPage(w, req, r, http.StatusNotFound, err)
			return
		}
		// Label jobs from the request's name maps: one query for machine
		// names and one for KB numbers, however many jobs (was two per job).
		names := namesFor(req, r)
		type enrichedJob struct {
			model.UpdateDeploymentJob
			MachineName string
			KBNumber    string
		}
		enriched := make([]enrichedJob, len(jobs))
		for i, j := range jobs {
			enriched[i].UpdateDeploymentJob = j
			enriched[i].MachineName = names.Lookup(model.KindMachine, j.MachineID).Label(j.MachineID)
			enriched[i].KBNumber = names.Lookup(model.KindUpdate, j.UpdateID).Label(j.UpdateID)
		}
		var kbs []string
		for _, id := range dep.UpdateIDs {
			kbs = append(kbs, names.Lookup(model.KindUpdate, id).Label(id))
		}
		// Summarize job statuses.
		counts := map[string]int{}
		for _, j := range jobs {
			counts[j.Status]++
		}
		targetJSON, _ := json.MarshalIndent(dep.Target, "", "  ")
		title := "Update deployment #" + idStr(dep.ID)
		if len(kbs) > 0 {
			title = "Deploy " + strings.Join(kbs, ", ")
		}
		search := newSearchBox(req, "Search jobs", "Search jobs")
		shown := filterByQuery(enriched, search.Query, func(j enrichedJob) []string {
			return []string{j.MachineName, j.KBNumber, j.Status}
		})
		render(w, req, r, "windowsupdate_deployment_detail.html", title, map[string]any{
			"Deployment": dep,
			"Jobs":       shown,
			"Search":     search.counted(len(shown), len(enriched), "jobs"),
			"Counts":     counts,
			"TargetJSON": string(targetJSON),
			"KBs":        kbs,
		})
	}
}

func idStr(id model.ID) string {
	return strconv.FormatInt(int64(id), 10)
}
