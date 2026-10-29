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
		post("/portal/machines/group-membership", machineGroupMembership(r))
	}
}

// machineGroupMembership adds or removes the checked machines to/from a manual
// group, straight from the inventory selection. It reuses the same machine_ids[]
// the bulk-delete bar submits, with a group_id and an op (add|remove).
func machineGroupMembership(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		gid, _ := strconv.ParseInt(req.FormValue("group_id"), 10, 64)
		op := req.FormValue("op")
		var ids []model.ID
		for _, s := range req.Form["machine_ids"] {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
				ids = append(ids, model.ID(n))
			}
		}
		back := "/portal/machines"
		if gid > 0 {
			back = fmt.Sprintf("/portal/machines?group=%d", gid)
		}
		if gid <= 0 || len(ids) == 0 {
			flash(w, "err", "Select a group and at least one machine.")
			http.Redirect(w, req, back, http.StatusFound)
			return
		}
		var err error
		verb := "Added"
		if op == "remove" {
			err = r.Groups.RemoveMembers(req.Context(), model.ID(gid), ids)
			verb = "Removed"
		} else {
			err = r.Groups.AddMembers(req.Context(), model.ID(gid), ids)
		}
		if err != nil {
			flash(w, "err", err.Error())
		} else {
			flash(w, "ok", fmt.Sprintf("%s %d machine%s.", verb, len(ids), plural(len(ids))))
		}
		http.Redirect(w, req, back, http.StatusFound)
	}
}

// machineDelete removes a single machine from inventory.
func machineDelete(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
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
			failPage(w, req, r, http.StatusBadRequest, err)
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
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		// Honour the list page's ?group= filter so the export matches what the
		// operator is looking at.
		if gs := strings.TrimSpace(req.URL.Query().Get("group")); gs != "" {
			if gid, perr := strconv.ParseInt(gs, 10, 64); perr == nil && gid > 0 {
				memberIDs, _ := r.Groups.Members(req.Context(), model.ID(gid))
				set := make(map[model.ID]bool, len(memberIDs))
				for _, id := range memberIDs {
					set[id] = true
				}
				filtered := make([]model.MachineRecord, 0, len(set))
				for _, m := range machines {
					if set[m.ID] {
						filtered = append(filtered, m)
					}
				}
				machines = filtered
			}
		}

		now := time.Now()
		facts := loadFleetFacts(req, r, machines, now)
		bindings, imageNames, osCaptions := facts.Bindings, facts.ImageNames, facts.OS
		machines = parseMachineFilters(req).apply(machines, facts, now)

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
		sortMachines(machines, sortKey, sortDir, facts)

		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="autodeploy-inventory.csv"`)
		cw := csv.NewWriter(w)
		// Columns are append-only so spreadsheets and scripts built on the
		// old layout keep working: image_name, reported_name,
		// display_name and status were added after last_seen.
		_ = cw.Write([]string{"id", "name", "agent_id", "manufacturer", "product", "serial",
			"system_uuid", "bios_vendor", "bios_version", "board_manufacturer", "board_product",
			"image_id", "first_seen", "last_seen", "image_name", "reported_name", "display_name", "status"})
		for _, m := range machines {
			b := bindings[m.ID]
			imageID, imageName := "", ""
			if b.ImageID != nil {
				imageID = strconv.FormatInt(int64(*b.ImageID), 10)
				imageName = imageNames[*b.ImageID]
			}
			_ = cw.Write([]string{
				strconv.FormatInt(int64(m.ID), 10), b.MachineName, m.AgentID,
				m.SystemManufacturer, m.SystemProduct, m.SystemSerial, m.SystemUUID,
				m.BIOSVendor, m.BIOSVersion, m.BoardManufacturer, m.BoardProduct,
				imageID, m.FirstSeen.Format("2006-01-02 15:04:05"), m.LastSeen.Format("2006-01-02 15:04:05"),
				imageName, m.ReportedName, model.MachineDisplayName(m, b).Name,
				facts.Status[m.ID].Label,
			})
		}
		cw.Flush()
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
			failPage(w, req, r, http.StatusBadRequest, err)
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
			failPage(w, req, r, http.StatusInternalServerError, err)
			return
		}
		// For each machine show its name + image (if any). Name follows the
		// shared model.MachineDisplayName precedence (desired name, then the
		// agent-reported name, then the short UUID); Display also carries the
		// pending-rename hint.
		type row struct {
			Machine        model.MachineRecord
			Name           string
			Display        model.MachineName
			ImageName      string
			OS             string
			ReimagePending bool
			Status         machineStatus
		}

		query := strings.TrimSpace(req.URL.Query().Get("q"))
		totalAll := len(v)

		// Left sidebar: every group with its resolved member count. The manual
		// subset feeds the selection bar's "add ticked machines to a group"
		// control, which works from any view (including All machines) so an
		// empty manual group can still be filled.
		groups, _ := r.Groups.ListWithCounts(req.Context())
		manualGroups := make([]model.MachineGroup, 0, len(groups))
		for _, g := range groups {
			if g.Kind == model.MachineGroupManual {
				manualGroups = append(manualGroups, g)
			}
		}

		// Group filter: ?group=<id> restricts the inventory to that group's
		// resolved members BEFORE the text filter, sort and pagination run, so
		// the pager and totals describe the in-group set. Manual groups also
		// surface "add/remove selected" actions (membership is editable);
		// dynamic groups don't (membership is computed from their filter).
		var activeGroupID int64
		var activeGroupName string
		var activeGroupManual bool
		if gs := strings.TrimSpace(req.URL.Query().Get("group")); gs != "" {
			if gid, perr := strconv.ParseInt(gs, 10, 64); perr == nil && gid > 0 {
				if g, gerr := r.Groups.Get(req.Context(), model.ID(gid)); gerr == nil {
					activeGroupID = int64(g.ID)
					activeGroupName = g.Name
					activeGroupManual = g.Kind == model.MachineGroupManual
					memberIDs, _ := r.Groups.Members(req.Context(), g.ID)
					set := make(map[model.ID]bool, len(memberIDs))
					for _, id := range memberIDs {
						set[id] = true
					}
					filtered := make([]model.MachineRecord, 0, len(set))
					for _, m := range v {
						if set[m.ID] {
							filtered = append(filtered, m)
						}
					}
					v = filtered
				}
			}
		}

		// One query each for the whole fleet: bindings, image names, OS
		// captions, latest deployments and re-image flags, from which every
		// machine's status is derived (pending-re-image badges only matter
		// for the visible page, so those are fetched after paging). Loading
		// the lot up front (rather than per page) is what lets ?q= search,
		// the filters and ?sort= cover the entire inventory, and it replaces
		// the old per-visible-row hardware Get — an N+1 that cost up to a
		// query per row at render time.
		now := time.Now()
		facts := loadFleetFacts(req, r, v, now)
		bindings, imageNames, osCaptions := facts.Bindings, facts.ImageNames, facts.OS

		// Filters (status chips, image / AD / model / last-deployed
		// selects) narrow the group before the text search. The chips
		// count over the whole group so each shows what it would select.
		filters := parseMachineFilters(req)
		chips, selects := machineFilterView(req, filters, v, facts)
		inView := len(v) // the group (or whole fleet) before filters and search
		v = filters.apply(v, facts, now)

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
		sortMachines(v, sortKey, sortDir, facts)

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
			disp := model.MachineDisplayName(m, b)
			name := disp.Name
			if disp.Unnamed {
				name = ""
			}
			rows = append(rows, row{
				Machine: m, Name: name, Display: disp, ImageName: imgName,
				OS:             osCaptions[m.ID],
				ReimagePending: facts.Reimage[m.ID],
				Status:         facts.Status[m.ID],
			})
		}
		search := newSearchBox(req, "Search machines", "Search machines").
			counted(len(v), inView, "machines").
			withFilters(req, "machine-search", filters.Active(), "image", "ad", "model", "since")
		// The status chip isn't a form control, so it rides along as a
		// hidden field; Clear drops it with the rest.
		search.ClearURL = withQuery(req, req.URL.Path, "q", "", "status", "", "image", "", "ad", "", "model", "", "since", "")
		render(w, req, r, "machine_list.html", "Machines", map[string]any{
			"Rows": rows, "Page": page, "Query": query, "TotalAll": totalAll,
			"Search":            search,
			"Filters":           filters,
			"StatusChips":       chips,
			"FilterSelects":     selects,
			"Sort":              machineSortColumns(req, sortKey, sortDir),
			"CSVURL":            machineCSVURL(req),
			"Groups":            groups,
			"ManualGroups":      manualGroups,
			"ActiveGroupID":     activeGroupID,
			"ActiveGroupName":   activeGroupName,
			"ActiveGroupManual": activeGroupManual,
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
	"os": true, "image": true, "last_seen": true, "status": true,
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

// sortMachines orders the machine set by the chosen column: status by
// urgency (most urgent first ascending, then most recently seen), every
// other column via sortMachineRecords.
func sortMachines(v []model.MachineRecord, key, dir string, f fleetFacts) {
	if key != "status" {
		sortMachineRecords(v, key, dir, f.Bindings, f.ImageNames, f.OS)
		return
	}
	sort.SliceStable(v, func(i, j int) bool {
		a, b := f.Status[v[i].ID].Rank, f.Status[v[j].ID].Rank
		if a != b {
			if dir == "desc" {
				return a > b
			}
			return a < b
		}
		if !v[i].LastSeen.Equal(v[j].LastSeen) {
			return v[i].LastSeen.After(v[j].LastSeen)
		}
		return v[i].ID < v[j].ID
	})
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
			return model.MachineDisplayName(m, b).Name
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
		out[key] = machineSortCol{URL: withQuery(req, req.URL.Path, "sort", key, "dir", dir), State: state}
	}
	return out
}

// machineCSVURL carries the active filter and ordering onto the CSV
// export link, so the download matches what's on screen.
func machineCSVURL(req *http.Request) string {
	q := url.Values{}
	for _, k := range append([]string{"q", "sort", "dir", "group"}, machineFilterNames...) {
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
