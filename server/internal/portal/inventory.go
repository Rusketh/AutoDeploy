package portal

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

func init() {
	registerInventoryRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/machines", machineList(r))
		get("/portal/machines/{id}", machineDetail(r))
		post("/portal/machines/{id}/binding", machineBindingSubmit(r))
		post("/portal/machines/{id}/bitlocker/pin", machineBLPin(r))
	}
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
