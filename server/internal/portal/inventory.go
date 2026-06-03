package portal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerInventoryRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/machines", machineList(r))
		get("/portal/machines.csv", machineCSV(r))
		get("/portal/machines/{id}", machineDetail(r))
		get("/portal/machines/{id}/deploy-status", machineDeployStatus(r))
		post("/portal/machines/{id}/binding", machineBindingSubmit(r))
		post("/portal/machines/{id}/action", machineAction(r))
		post("/portal/machines/{id}/delete", machineDelete(r))
		post("/portal/machines/delete", machineBulkDelete(r))
		post("/portal/machines/{id}/bitlocker/pin", machineBLPin(r))
	}
}

// machineDelete removes a single machine from inventory.
func machineDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.Inventory.Delete(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
			return
		}
		flash(w, "ok", "Machine deleted from inventory.")
		http.Redirect(w, req, "/portal/machines", http.StatusFound)
	}
}

// machineBulkDelete removes every checked machine from the list page.
func machineBulkDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		n := 0
		for _, s := range req.Form["machine_ids"] {
			id, err := strconv.ParseInt(s, 10, 64)
			if err != nil || id <= 0 {
				continue
			}
			if err := r.Inventory.Delete(req.Context(), model.ID(id)); err == nil {
				n++
			}
		}
		if n == 0 {
			flash(w, "err", "No machines selected.")
		} else {
			flash(w, "ok", fmt.Sprintf("Deleted %d machine%s.", n, plural(n)))
		}
		http.Redirect(w, req, "/portal/machines", http.StatusFound)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// machineCSV streams the full machine inventory as a CSV download.
func machineCSV(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		machines, err := r.Inventory.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Batch-load bindings for all machines.
		machineIDs := make([]model.ID, len(machines))
		for i, m := range machines {
			machineIDs[i] = m.ID
		}
		bindings, _ := r.Inventory.ListBindingsForMachines(req.Context(), machineIDs)

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="autodeploy-inventory.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "name", "agent_id", "manufacturer", "product", "serial",
			"system_uuid", "bios_vendor", "bios_version", "board_manufacturer", "board_product",
			"image_id", "first_seen", "last_seen"})
		for _, m := range machines {
			b := bindings[m.ID]
			imageID := ""
			if b.ImageID != nil {
				imageID = strconv.FormatInt(int64(*b.ImageID), 10)
			}
			_ = cw.Write([]string{
				strconv.FormatInt(int64(m.ID), 10), b.MachineName, m.AgentID,
				m.SystemManufacturer, m.SystemProduct, m.SystemSerial, m.SystemUUID,
				m.BIOSVendor, m.BIOSVersion, m.BoardManufacturer, m.BoardProduct,
				imageID, m.FirstSeen.Format("2006-01-02 15:04:05"), m.LastSeen.Format("2006-01-02 15:04:05"),
			})
		}
		cw.Flush()
	}
}

// machineAction runs a single-machine bulk action (rename / script /
// software push) straight from the machine's page, reusing the bulk
// machinery with a machine-id target.
func machineAction(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		action := req.FormValue("action")
		payload, perr := actionPayloadFromForm(req)
		if perr != nil {
			flash(w, "err", perr.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
			return
		}
		user, _ := sessionUser(req, r)
		if _, _, err := r.Bulk.CreateOperation(req.Context(), model.BulkOperation{
			Action:         action,
			Payload:        payload,
			Target:         model.BulkTarget{MachineIDs: []model.ID{id}},
			CreatedBy:      user.Username,
			ReimageImageID: reimageImageIDFromForm(req),
		}); err != nil {
			flash(w, "err", err.Error())
		} else if action == model.BulkActionReimage {
			flash(w, "ok", "Re-image queued. The machine will reboot and re-image on its next check-in.")
		} else {
			flash(w, "ok", "Action queued for this machine.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}

// actionPayloadFromForm builds a bulk-op payload from the shared action
// form fields (used by both the bulk page and per-machine actions).
func actionPayloadFromForm(req *http.Request) (string, error) {
	switch req.FormValue("action") {
	case model.BulkActionRename:
		name := strings.TrimSpace(req.FormValue("rename_new_name"))
		find := strings.TrimSpace(req.FormValue("rename_find"))
		switch {
		case name != "":
			b, _ := json.Marshal(map[string]string{"new_name": name})
			return string(b), nil
		case find != "":
			b, _ := json.Marshal(map[string]string{"rename_find": find, "rename_replace": req.FormValue("rename_replace")})
			return string(b), nil
		}
		return "", fmt.Errorf("rename requires a new name or a find pattern")
	case model.BulkActionScript:
		body := req.FormValue("script_body")
		if strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("script body required")
		}
		b, _ := json.Marshal(map[string]string{"shell": req.FormValue("script_shell"), "body": body})
		return string(b), nil
	case model.BulkActionSoftwarePush:
		pid, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64)
		if err != nil || pid <= 0 {
			return "", fmt.Errorf("choose a software package")
		}
		b, _ := json.Marshal(map[string]int64{"package_id": pid})
		return string(b), nil
	case model.BulkActionReimage:
		// The image to deploy is carried on the operation (ReimageImageID),
		// not the job payload; the job just tells the agent to reboot.
		return "{}", nil
	}
	return "", fmt.Errorf("unknown action")
}

// reimageImageIDFromForm reads the optional target image for a reimage
// action; 0 (or absent) means "use each machine's existing binding".
func reimageImageIDFromForm(req *http.Request) model.ID {
	if v := strings.TrimSpace(req.FormValue("reimage_image_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return model.ID(n)
		}
	}
	return 0
}

func machineList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v, err := r.Inventory.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// For each machine show its name + image (if any). Name prefers the
		// agent-reported current computer name (reality), falling back to the
		// binding's desired name for machines that haven't reported yet.
		type row struct {
			Machine   model.MachineRecord
			Name      string
			ImageName string
			BLSet     bool
			OS        string
		}
		// Pagination. Client-side filtering still works within the
		// page; pagination is a guard against a multi-thousand-row
		// payload, not a substitute for search.
		page := paginate(req, len(v), 50)
		slice := v[page.Offset:page.End]

		// Batch-load bindings, images, and BitLocker status for the
		// page slice instead of N+1 per-machine queries.
		machineIDs := make([]model.ID, len(slice))
		for i, m := range slice {
			machineIDs[i] = m.ID
		}
		bindings, _ := r.Inventory.ListBindingsForMachines(req.Context(), machineIDs)
		blStatuses, _ := r.BitLocker.ListPINStatuses(req.Context(), machineIDs)

		// Collect unique image IDs from bindings so we can batch-load names.
		imageIDSet := map[model.ID]struct{}{}
		for _, b := range bindings {
			if b.ImageID != nil {
				imageIDSet[*b.ImageID] = struct{}{}
			}
		}
		imageIDs := make([]model.ID, 0, len(imageIDSet))
		for id := range imageIDSet {
			imageIDs = append(imageIDs, id)
		}
		imageNames, _ := r.Images.ListNamesByIDs(req.Context(), imageIDs)

		rows := make([]row, 0, len(slice))
		for _, m := range slice {
			b := bindings[m.ID]
			imgName := ""
			if b.ImageID != nil {
				imgName = imageNames[*b.ImageID]
			}
			bl := blStatuses[m.ID]
			name := m.ReportedName
			if name == "" {
				name = b.MachineName
			}
			osCaption := ""
			if full, err := r.Inventory.Get(req.Context(), m.ID); err == nil && full.Hardware != nil {
				osCaption = full.Hardware.OSCaption
			}
			rows = append(rows, row{Machine: m, Name: name, ImageName: imgName, BLSet: bl.PINSet, OS: osCaption})
		}
		render(w, req, r, "machine_list.html", "Machines", map[string]any{
			"Rows": rows, "Page": page,
		})
	}
}

func machineDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.Get(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		binding, _ := r.Inventory.GetBinding(req.Context(), id)
		history, _ := r.Inventory.HistoryFor(req.Context(), id)
		// Derive a per-row "stalled" flag for display: an in_progress row
		// whose machine hasn't checked in for longer than deployStallAfter has
		// most likely failed Setup (or is powered off). Computed here rather
		// than persisted so it self-corrects if the machine checks back in.
		type historyRow struct {
			model.DeploymentRecord
			Stalled bool
		}
		machineStalled := time.Since(m.LastSeen) > deployStallAfter
		historyRows := make([]historyRow, len(history))
		for i, h := range history {
			historyRows[i] = historyRow{DeploymentRecord: h, Stalled: h.Outcome == "in_progress" && machineStalled}
		}
		reimages, _ := r.Inventory.ListReimageEvents(req.Context(), id)

		// At-a-glance summary scalars. History is newest-first, so [0] is the
		// latest deployment and reimages[0] (newest-first) the last re-image.
		var latestDeploy *model.DeploymentRecord
		if len(history) > 0 {
			latestDeploy = &history[0]
		}
		var lastReimage *model.ReimageEvent
		if len(reimages) > 0 {
			lastReimage = &reimages[0]
		}
		// Live deploy progress for the initial paint (the JS poller takes over
		// after load). Active only when there's an open in_progress row.
		deployActive := false
		deployLabel, deployPercent, deployIndeterminate := "", 0, false
		if open, oerr := r.Inventory.LatestOpenDeployment(req.Context(), id); oerr == nil {
			deployActive = true
			deployLabel, deployPercent, deployIndeterminate = model.DeployPhaseProgress(open.Phase, open.Outcome)
		}
		detected, _ := r.Inventory.DetectedStateFor(req.Context(), id)
		bl, _ := r.BitLocker.PINStatus(req.Context(), id)
		recovery, _ := r.BitLocker.ListRecoveryKeys(req.Context(), id)
		images, _ := r.Images.List(req.Context())
		var pkgs []model.SoftwarePackage
		pkgs, _ = r.Software.List(req.Context())
		var kbStatuses []model.MachineUpdateStatus
		if r.Updates != nil {
			kbStatuses, _ = r.Updates.ListMachineStatuses(req.Context(), id)
		}
		swDetected, swTotal := 0, len(detected)
		for _, d := range detected {
			if d.Detected {
				swDetected++
			}
		}
		kbInstalled := 0
		for _, s := range kbStatuses {
			if s.Status == "installed" {
				kbInstalled++
			}
		}
		render(w, req, r, "machine_detail.html", "Machine "+m.SystemUUID, map[string]any{
			"M": m, "Binding": binding, "History": historyRows,
			"Reimages": reimages,
			"Detected": detected, "Packages": pkgs,
			"BL": bl, "Recovery": recovery,
			"Images": images, "KBStatuses": kbStatuses,
			"SWDetected": swDetected, "SWTotal": swTotal,
			"KBInstalled": kbInstalled, "KBTotal": len(kbStatuses),
			"LatestDeploy": latestDeploy, "DeployCount": len(history),
			"LastReimage": lastReimage, "MachineStalled": machineStalled,
			"DeployActive": deployActive, "DeployLabel": deployLabel,
			"DeployPercent": deployPercent, "DeployIndeterminate": deployIndeterminate,
		})
	}
}

// machineDeployStatus serves the live deploy progress as JSON for the machine
// detail page's progress poller, mirroring isoPrepStatus. When there's an open
// in_progress deployment it returns its phase/label/percent (and a stall flag);
// otherwise it reports the latest row's final outcome with finished=true so the
// poller knows to reload into the settled at-a-glance view.
func machineDeployStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		open, oerr := r.Inventory.LatestOpenDeployment(req.Context(), id)
		if oerr != nil {
			// No open deployment: report the latest outcome (if any) as finished.
			outcome := "none"
			if hist, herr := r.Inventory.HistoryFor(req.Context(), id); herr == nil && len(hist) > 0 {
				outcome = hist[0].Outcome
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"active": false, "finished": true, "outcome": outcome,
			})
			return
		}
		label, percent, indeterminate := model.DeployPhaseProgress(open.Phase, open.Outcome)
		stalled := false
		if m, merr := r.Inventory.Get(req.Context(), id); merr == nil {
			stalled = time.Since(m.LastSeen) > deployStallAfter
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "finished": false,
			"phase": open.Phase, "label": label, "percent": percent,
			"indeterminate": indeterminate, "outcome": open.Outcome,
			"stalled": stalled,
		})
	}
}

func machineBindingSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := req.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b := model.MachineBinding{
			MachineID:   id,
			MachineName: strings.TrimSpace(req.FormValue("machine_name")),
			TargetOU:    strings.TrimSpace(req.FormValue("target_ou")),
		}
		if v := strings.TrimSpace(req.FormValue("image_id")); v != "" && v != "0" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				iid := model.ID(n)
				b.ImageID = &iid
			}
		}
		if g := strings.TrimSpace(req.FormValue("groups")); g != "" {
			for _, line := range strings.Split(g, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					b.GroupMemberships = append(b.GroupMemberships, line)
				}
			}
		}
		if err := r.Inventory.UpsertBinding(req.Context(), b); err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", "Binding saved.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}

func machineBLPin(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		pin := req.FormValue("pin")
		if err := r.BitLocker.SetPIN(req.Context(), id, pin); err != nil {
			flash(w, "err", err.Error())
		} else if pin == "" {
			flash(w, "ok", "BitLocker PIN cleared. Machine will not be re-encrypted on next deploy.")
		} else {
			flash(w, "ok", "BitLocker PIN saved. Will apply on next deploy or re-image.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}
