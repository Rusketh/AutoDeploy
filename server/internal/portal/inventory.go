package portal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerInventoryRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/machines", machineList(r))
		get("/portal/machines.csv", machineCSV(r))
		get("/portal/machines/{id}", machineDetail(r))
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
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="autodeploy-inventory.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "name", "agent_id", "manufacturer", "product", "serial",
			"system_uuid", "bios_vendor", "bios_version", "board_manufacturer", "board_product",
			"image_id", "first_seen", "last_seen"})
		for _, m := range machines {
			b, _ := r.Inventory.GetBinding(req.Context(), m.ID)
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
		// For each machine show its binding's machine_name + image (if any).
		type row struct {
			Machine     model.MachineRecord
			BindingName string
			ImageName   string
			BLSet       bool
		}
		// Pagination. Client-side filtering still works within the
		// page; pagination is a guard against a multi-thousand-row
		// payload, not a substitute for search.
		page := paginate(req, len(v), 50)
		slice := v[page.Offset:page.End]
		rows := make([]row, 0, len(slice))
		for _, m := range slice {
			b, _ := r.Inventory.GetBinding(req.Context(), m.ID)
			imgName := ""
			if b.ImageID != nil {
				im, _ := r.Images.Get(req.Context(), *b.ImageID)
				imgName = im.Name
			}
			bl, _ := r.BitLocker.PINStatus(req.Context(), m.ID)
			rows = append(rows, row{Machine: m, BindingName: b.MachineName, ImageName: imgName, BLSet: bl.PINSet})
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
		detected, _ := r.Inventory.DetectedStateFor(req.Context(), id)
		bl, _ := r.BitLocker.PINStatus(req.Context(), id)
		recovery, _ := r.BitLocker.ListRecoveryKeys(req.Context(), id)
		images, _ := r.Images.List(req.Context())
		var pkgs []model.SoftwarePackage
		pkgs, _ = r.Software.List(req.Context())
		render(w, req, r, "machine_detail.html", "Machine "+m.SystemUUID, map[string]any{
			"M": m, "Binding": binding, "History": history,
			"Detected": detected, "Packages": pkgs,
			"BL": bl, "Recovery": recovery,
			"Images": images,
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
