package portal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

func init() {
	registerInventoryRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		get("/portal/machines", machineList(r))
		get("/portal/machines.csv", machineCSV(r))
		get("/portal/machines/bulk-edit", machineBulkEditForm(r))
		post("/portal/machines/bulk-edit", machineBulkEditSubmit(r))
		get("/portal/machines/{id}", machineDetail(r))
		get("/portal/machines/{id}/deploy-status", machineDeployStatus(r))
		post("/portal/machines/{id}/binding", machineBindingSubmit(r))
		post("/portal/machines/{id}/action", machineAction(r))
		post("/portal/machines/{id}/reimage/cancel", machineReimageCancel(r))
		post("/portal/machines/{id}/delete", machineDelete(r))
		post("/portal/machines/delete", machineBulkDelete(r))
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

// machineCSV streams the machine inventory as a CSV download. It honours
// the list page's ?q= filter and ?sort=/?dir= ordering (the export link
// carries them), so the download matches what the operator is looking at;
// without them it exports the whole fleet.
func machineCSV(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		machines, err := r.Inventory.List(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bindings, imageNames := loadMachineBindings(req, r, machines)
		osCaptions, _ := r.Inventory.ListOSCaptions(req.Context())

		if query := strings.TrimSpace(req.URL.Query().Get("q")); query != "" {
			needle := strings.ToLower(query)
			filtered := make([]model.MachineRecord, 0, len(machines))
			for _, m := range machines {
				if machineMatchesQuery(m, bindings[m.ID], imageNames, osCaptions[m.ID], needle) {
					filtered = append(filtered, m)
				}
			}
			machines = filtered
		}

		sortKey, sortDir := machineSort(req)
		sortMachineRecords(machines, sortKey, sortDir, bindings, imageNames, osCaptions)

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
		op, _, err := r.Bulk.CreateOperation(req.Context(), model.BulkOperation{
			Action:         action,
			Payload:        payload,
			Target:         model.BulkTarget{MachineIDs: []model.ID{id}},
			CreatedBy:      user.Username,
			ReimageImageID: reimageImageIDFromForm(req),
		})
		switch {
		case err != nil:
			flash(w, "err", err.Error())
		case action == model.BulkActionReimage:
			r.Emitter.Emit(req.Context(), notify.BulkCreatedEvent(op))
			flash(w, "ok", "Re-image queued. The machine will reboot and re-image on its next check-in.")
		default:
			r.Emitter.Emit(req.Context(), notify.BulkCreatedEvent(op))
			flash(w, "ok", "Action queued for this machine.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}

// machineReimageCancel clears a pending re-image on a single machine: it drops
// the reimage_pending flag (so the boot client won't auto-deploy on the next
// network boot) and neutralises the queued re-image job (so the agent won't
// reboot the box). Used when a remote re-image is stuck — e.g. the agent is
// offline/outdated and hasn't picked it up — or was queued by mistake.
func machineReimageCancel(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := r.Inventory.ClearReimagePending(req.Context(), id); err != nil {
			flash(w, "err", err.Error())
			http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
			return
		}
		if r.Bulk != nil {
			if _, err := r.Bulk.CancelReimageJobsForMachine(req.Context(), id); err != nil {
				flash(w, "err", "Flag cleared, but cancelling the queued re-image job failed: "+err.Error())
				http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
				return
			}
		}
		flash(w, "ok", "Pending re-image cancelled. The machine will not re-image on its next boot.")
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
			Machine        model.MachineRecord
			Name           string
			ImageName      string
			OS             string
			ReimagePending bool
		}

		query := strings.TrimSpace(req.URL.Query().Get("q"))
		totalAll := len(v)

		// One query each for the whole fleet: bindings, image names, OS
		// captions and pending-re-image flags. Loading the lot up front
		// (rather than per page) is what lets ?q= search and ?sort= order
		// the entire inventory, and it replaces the old per-visible-row
		// hardware Get — an N+1 that cost up to a query per row at render time.
		bindings, imageNames := loadMachineBindings(req, r, v)
		osCaptions, _ := r.Inventory.ListOSCaptions(req.Context())
		allIDs := make([]model.ID, len(v))
		for i, m := range v {
			allIDs[i] = m.ID
		}
		reimaging, _ := r.Inventory.ListReimagePending(req.Context(), allIDs)

		// Server-side filter: ?q= narrows the whole inventory BEFORE
		// pagination, so the page count, numbered links and totals all
		// describe the filtered set (filtering only the rows of the
		// current page made the pager lie). Keeping the filter in the
		// URL is also what lets browser back/forward restore it until
		// the operator clears it.
		if query != "" {
			needle := strings.ToLower(query)
			filtered := make([]model.MachineRecord, 0, len(v))
			for _, m := range v {
				if machineMatchesQuery(m, bindings[m.ID], imageNames, osCaptions[m.ID], needle) {
					filtered = append(filtered, m)
				}
			}
			v = filtered
		}

		// Server-side sort: ?sort=&dir= orders the whole (filtered) set,
		// not just the visible page the old client-side sort reordered.
		sortKey, sortDir := machineSort(req)
		sortMachineRecords(v, sortKey, sortDir, bindings, imageNames, osCaptions)

		// Page size: an explicit ?size= wins and is remembered in a
		// cookie, so the choice sticks on later visits that don't carry
		// the parameter; without either the list defaults to 50.
		defSize := 50
		if c, cerr := req.Cookie(machinePageSizeCookie); cerr == nil {
			if n, aerr := strconv.Atoi(c.Value); aerr == nil && n >= 10 && n <= 500 {
				defSize = n
			}
		}
		if s := req.URL.Query().Get("size"); s != "" {
			if n, aerr := strconv.Atoi(s); aerr == nil && n >= 10 && n <= 500 {
				http.SetCookie(w, &http.Cookie{
					Name: machinePageSizeCookie, Value: strconv.Itoa(n),
					Path: "/portal/machines", MaxAge: 365 * 24 * 60 * 60,
					SameSite: http.SameSiteLaxMode,
				})
			}
		}
		page := paginate(req, len(v), defSize)
		slice := v[page.Offset:page.End]

		rows := make([]row, 0, len(slice))
		for _, m := range slice {
			b := bindings[m.ID]
			imgName := ""
			if b.ImageID != nil {
				imgName = imageNames[*b.ImageID]
			}
			name := m.ReportedName
			if name == "" {
				name = b.MachineName
			}
			rows = append(rows, row{
				Machine: m, Name: name, ImageName: imgName,
				OS:             osCaptions[m.ID],
				ReimagePending: reimaging[m.ID],
			})
		}
		render(w, req, r, "machine_list.html", "Machines", map[string]any{
			"Rows": rows, "Page": page, "Query": query, "TotalAll": totalAll,
			"Sort":   machineSortColumns(req, sortKey, sortDir),
			"CSVURL": machineCSVURL(req),
		})
	}
}

// loadMachineBindings batch-loads bindings plus bound image names for the
// given machines (one query each, never N+1).
func loadMachineBindings(req *http.Request, r Repos, ms []model.MachineRecord) (map[model.ID]model.MachineBinding, map[model.ID]string) {
	ids := make([]model.ID, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	bindings, _ := r.Inventory.ListBindingsForMachines(req.Context(), ids)
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
	return bindings, imageNames
}

// machineSortKeys are the columns the machines list can be ordered by.
var machineSortKeys = map[string]bool{
	"uuid": true, "name": true, "model": true, "serial": true,
	"os": true, "image": true, "last_seen": true,
}

// machineSort reads ?sort=&dir=, defaulting to last_seen desc (the repo's
// natural order: most recently seen first).
func machineSort(req *http.Request) (key, dir string) {
	key = req.URL.Query().Get("sort")
	if !machineSortKeys[key] {
		key = "last_seen"
	}
	dir = req.URL.Query().Get("dir")
	if dir != "asc" && dir != "desc" {
		if key == "last_seen" {
			dir = "desc"
		} else {
			dir = "asc"
		}
	}
	return key, dir
}

// sortMachineRecords orders the full machine set by the chosen column.
// Display-derived columns (name, image, OS) sort by the same values the
// rows show. Ties break by ID so paging is stable.
func sortMachineRecords(v []model.MachineRecord, key, dir string,
	bindings map[model.ID]model.MachineBinding, imageNames, osCaptions map[model.ID]string) {
	if key == "last_seen" && dir == "desc" {
		return // the repo already returns last_seen DESC
	}
	val := func(m model.MachineRecord) string {
		b := bindings[m.ID]
		switch key {
		case "uuid":
			return m.SystemUUID
		case "name":
			if m.ReportedName != "" {
				return m.ReportedName
			}
			return b.MachineName
		case "model":
			return m.SystemManufacturer + " " + m.SystemProduct
		case "serial":
			return m.SystemSerial
		case "os":
			return osCaptions[m.ID]
		case "image":
			if b.ImageID != nil {
				return imageNames[*b.ImageID]
			}
			return ""
		}
		return ""
	}
	asc := func(i, j int) bool {
		if key == "last_seen" {
			if v[i].LastSeen.Equal(v[j].LastSeen) {
				return v[i].ID < v[j].ID
			}
			return v[i].LastSeen.Before(v[j].LastSeen)
		}
		a, b := strings.ToLower(val(v[i])), strings.ToLower(val(v[j]))
		if a == b {
			return v[i].ID < v[j].ID
		}
		return a < b
	}
	sort.SliceStable(v, func(i, j int) bool {
		if dir == "desc" {
			return asc(j, i)
		}
		return asc(i, j)
	})
}

// machineSortCol is one sortable header: the link to click and the state
// class ("asc"/"desc"/"") that drives the header's arrow indicator.
type machineSortCol struct {
	URL   string
	State string
}

// machineSortColumns builds a header link per sortable column. Clicking
// the active column flips its direction; any other column starts on its
// natural direction (text asc, last_seen desc). Links keep the filter and
// page size but reset to page 1.
func machineSortColumns(req *http.Request, activeKey, activeDir string) map[string]machineSortCol {
	out := make(map[string]machineSortCol, len(machineSortKeys))
	for key := range machineSortKeys {
		dir := "asc"
		if key == "last_seen" {
			dir = "desc"
		}
		state := ""
		if key == activeKey {
			state = activeDir
			if activeDir == "asc" {
				dir = "desc"
			} else {
				dir = "asc"
			}
		}
		q := req.URL.Query()
		q.Set("sort", key)
		q.Set("dir", dir)
		q.Del("page")
		out[key] = machineSortCol{URL: req.URL.Path + "?" + q.Encode(), State: state}
	}
	return out
}

// machineCSVURL carries the active filter and ordering onto the CSV
// export link, so the download matches what's on screen.
func machineCSVURL(req *http.Request) string {
	q := url.Values{}
	for _, k := range []string{"q", "sort", "dir"} {
		if v := req.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return "/portal/machines.csv"
	}
	return "/portal/machines.csv?" + q.Encode()
}

// machinePageSizeCookie remembers the operator's items-per-page choice on
// the machines list across visits.
const machinePageSizeCookie = "ad_machines_size"

// machineMatchesQuery is the machines list's ?q= predicate: a case-
// insensitive substring test across the fields an operator can see or
// paste — names, system AND base-board SMBIOS identity, serials, UUID,
// BIOS, the reported OS and the bound image's name. needle must already
// be lower-cased.
func machineMatchesQuery(m model.MachineRecord, b model.MachineBinding, imageNames map[model.ID]string, os, needle string) bool {
	hay := []string{
		m.SystemUUID, m.ReportedName, b.MachineName,
		m.SystemManufacturer, m.SystemProduct, m.SystemSerial,
		m.BoardManufacturer, m.BoardProduct, m.BoardSerial,
		m.BIOSVendor, m.BIOSVersion, os,
	}
	if b.ImageID != nil {
		hay = append(hay, imageNames[*b.ImageID])
	}
	for _, h := range hay {
		if h != "" && strings.Contains(strings.ToLower(h), needle) {
			return true
		}
	}
	return false
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
			deployLabel, deployPercent, deployIndeterminate = model.DeployRecordProgress(open)
		}
		detected, _ := r.Inventory.DetectedStateFor(req.Context(), id)
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
		// Pending re-image: surface a flagged-but-not-yet-started re-image so an
		// operator can see it's queued and waiting for the agent to reboot the
		// box (and cancel it if it's stuck). "Since"/"Claimed" come from the
		// re-image bulk job; the target image name resolves the flag's image id
		// (0 => the machine's current binding).
		reimagePending := map[string]any{"Pending": false}
		if pending, imgID, perr := r.Inventory.ReimagePendingByID(req.Context(), id); perr == nil && pending {
			target := "its bound image"
			if imgID != 0 {
				for _, im := range images {
					if im.ID == imgID {
						target = im.Name
						break
					}
				}
			}
			info := map[string]any{"Pending": true, "Target": target, "Claimed": false, "Failed": false}
			if r.Bulk != nil {
				if job, jerr := r.Bulk.LatestReimageJob(req.Context(), id); jerr == nil && job != nil {
					info["Since"] = job.QueuedAt
					info["Claimed"] = job.Status == "running"
					// A failed job while the flag is still set means the agent
					// claimed the re-image but couldn't act on it (most often an
					// outdated agent that doesn't know the action) — a useful
					// signal that the box won't reboot on its own.
					info["Failed"] = job.Status == "failed"
				}
			}
			reimagePending = info
		}
		render(w, req, r, "machine_detail.html", "Machine "+m.SystemUUID, map[string]any{
			"ReimagePending": reimagePending,
			"M":              m, "Binding": binding, "History": historyRows,
			"Reimages": reimages,
			"Detected": detected, "Packages": pkgs,
			"Images": images, "KBStatuses": kbStatuses,
			"SWDetected": swDetected, "SWTotal": swTotal,
			"KBInstalled": kbInstalled, "KBTotal": len(kbStatuses),
			"LatestDeploy": latestDeploy, "DeployCount": len(history),
			"LastReimage": lastReimage, "MachineStalled": machineStalled,
			"DeployActive": deployActive, "DeployLabel": deployLabel,
			"DeployPercent": deployPercent, "DeployIndeterminate": deployIndeterminate,
			"AP": formPrefill(nil),
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
		label, percent, indeterminate := model.DeployRecordProgress(open)
		stalled := false
		if m, merr := r.Inventory.Get(req.Context(), id); merr == nil {
			stalled = time.Since(m.LastSeen) > deployStallAfter
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "finished": false,
			"phase": open.Phase, "label": label, "percent": percent,
			"indeterminate": indeterminate, "outcome": open.Outcome,
			"done": open.ProgressDone, "total": open.ProgressTotal,
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
