package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// machineTimelineRuns is how many of a machine's most recent deployments get
// a log timeline on its detail page.
const machineTimelineRuns = 5

// machineDetailForm carries a rejected binding save or action back into the
// detail page, so the operator's input and the error show in place (422)
// instead of a flash over a reset form.
type machineDetailForm struct {
	Binding      *model.MachineBinding // the submitted binding values
	BindingError *FormError
	ActionError  *FormError
	AP           map[string]any // the submitted action fields
}

func machineDetail(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		machineDetailPage(w, req, r, id, machineDetailForm{})
	}
}

// machineDetailPage renders the machine page; with a FormError in form the
// response is 422 and the submitted values are shown instead of the stored
// ones.
func machineDetailPage(w http.ResponseWriter, req *http.Request, r Repos, id model.ID, form machineDetailForm) {
	m, err := r.Inventory.Get(req.Context(), id)
	if err != nil {
		failPage(w, req, r, http.StatusNotFound, err)
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
		// Timeline is the run's key log events (most recent runs only).
		Timeline *model.DeployTimeline
	}
	now := time.Now()
	machineStalled := now.Sub(m.LastSeen) > deployStallAfter
	historyRows := make([]historyRow, len(history))
	for i, h := range history {
		historyRows[i] = historyRow{DeploymentRecord: h, Stalled: h.Outcome == "in_progress" && machineStalled}
		if i < machineTimelineRuns && r.Logs != nil {
			if tl, terr := r.Logs.DeploymentTimeline(req.Context(), h, m, namesFor(req, r)); terr == nil && len(tl.Events) > 0 {
				historyRows[i].Timeline = &tl
			}
		}
	}
	reimages, _ := r.Inventory.ListReimageEvents(req.Context(), id)

	// Current state. History is newest-first, so [0] is the latest
	// deployment and reimages[0] (newest-first) the last re-image. The
	// latest run's first error (with its hint) is the headline.
	var latestDeploy *model.DeploymentRecord
	var latestHeadline *model.LogEvent
	latestStalled := false
	if len(history) > 0 {
		latestDeploy = &history[0]
		latestStalled = historyRows[0].Stalled
		if tl := historyRows[0].Timeline; tl != nil && latestDeploy.Outcome != "ok" {
			latestHeadline = tl.Headline
		}
	}
	var lastReimage *model.ReimageEvent
	if len(reimages) > 0 {
		lastReimage = &reimages[0]
	}
	// Live deploy progress for the initial paint (the JS poller takes over
	// after load). Only a genuinely active run gets the bar: the open row
	// must be the latest run (an older run left open behind a newer failed
	// one is history, not progress) and the machine must still be checking
	// in (a stalled run says so in the Current state card instead).
	deployActive := false
	deployLabel, deployPercent, deployIndeterminate := "", 0, false
	if open, oerr := r.Inventory.LatestOpenDeployment(req.Context(), id); oerr == nil &&
		latestDeploy != nil && open.ID == latestDeploy.ID && !machineStalled {
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
	pendingFlag := false
	if pending, imgID, perr := r.Inventory.ReimagePendingByID(req.Context(), id); perr == nil && pending {
		pendingFlag = true
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
	// The page title always uses the stored binding; a rejected save shows
	// the submitted values in the form only.
	display := model.MachineDisplayName(m, binding)
	if form.Binding != nil {
		binding = *form.Binding
	}
	ap := form.AP
	if ap == nil {
		ap = actionPrefill()
	}
	data := map[string]any{
		"ReimagePending": reimagePending,
		"Display":        display,
		"Status":         deriveMachineStatus(now, m.LastSeen, latestDeploy, pendingFlag),
		"M":              m, "Binding": binding, "History": historyRows,
		"Reimages": reimages,
		"Detected": detected, "Packages": pkgs,
		"Images": images, "KBStatuses": kbStatuses,
		"SWDetected": swDetected, "SWTotal": swTotal,
		"KBInstalled": kbInstalled, "KBTotal": len(kbStatuses),
		"LatestDeploy": latestDeploy, "DeployCount": len(history),
		"LatestHeadline": latestHeadline, "LatestStalled": latestStalled,
		"StallAfter":  humanDuration(deployStallAfter),
		"LastReimage": lastReimage, "MachineStalled": machineStalled,
		"DeployActive": deployActive, "DeployLabel": deployLabel,
		"DeployPercent": deployPercent, "DeployIndeterminate": deployIndeterminate,
		"AP":           ap,
		"BindingError": form.BindingError,
		"ActionError":  form.ActionError,
	}
	fe := form.BindingError
	if fe == nil {
		fe = form.ActionError
	}
	renderForm(w, req, r, "machine_detail.html", display.Name, fe, data)
}

// actionPrefill is the single-machine action form's empty state: no action
// chosen (the operator must pick one), plus the fields only the
// single-machine picker has.
func actionPrefill() map[string]any {
	ap := formPrefill(nil, time.UTC)
	ap["Action"] = ""
	ap["RenameNewName"] = ""
	ap["ReimageConfirm"] = ""
	return ap
}

// actionPrefillFromForm re-fills the action picker from a rejected submit.
func actionPrefillFromForm(req *http.Request) map[string]any {
	ap := actionPrefill()
	ap["Action"] = req.FormValue("action")
	ap["RenameNewName"] = req.FormValue("rename_new_name")
	ap["ScriptShell"] = req.FormValue("script_shell")
	if ap["ScriptShell"] == "" {
		ap["ScriptShell"] = "powershell"
	}
	ap["ScriptBody"] = req.FormValue("script_body")
	if n, err := strconv.ParseInt(req.FormValue("software_package_id"), 10, 64); err == nil {
		ap["SoftwarePackageID"] = n
	}
	ap["ReimageImageID"] = int64(reimageImageIDFromForm(req))
	ap["ReimageConfirm"] = req.FormValue("reimage_confirm")
	return ap
}

// reimageConfirmMatches reports whether typed is the machine's name, as
// the type-to-confirm field asks: the shown display name, or the reported
// or desired name, ignoring case and surrounding space.
func reimageConfirmMatches(typed string, m model.MachineRecord, b model.MachineBinding) bool {
	typed = strings.TrimSpace(typed)
	if typed == "" {
		return false
	}
	for _, n := range []string{model.MachineDisplayName(m, b).Name, m.ReportedName, b.MachineName} {
		if n != "" && strings.EqualFold(typed, strings.TrimSpace(n)) {
			return true
		}
	}
	return false
}

// machineAction runs a single-machine bulk action (rename / script /
// software push / re-image) straight from the machine's page, reusing the
// bulk machinery with a machine-id target. A rejected submit re-renders
// the page with the error beside the field.
//
// Re-image asks the operator to type the machine's name. The portal form
// says so with reimage_confirm_required=1 and the server checks it; a
// caller that posts the older shape without that marker (scripts written
// against earlier versions) is not asked.
func machineAction(r Repos) http.HandlerFunc {
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
		reject := func(fe *FormError) {
			machineDetailPage(w, req, r, id, machineDetailForm{ActionError: fe, AP: actionPrefillFromForm(req)})
		}
		action := req.FormValue("action")
		if action == "" {
			reject(&FormError{Message: "Choose an action first. Nothing was queued."})
			return
		}
		if action == model.BulkActionReimage && req.FormValue("reimage_confirm_required") != "" {
			m, merr := r.Inventory.Get(req.Context(), id)
			if merr != nil {
				failPage(w, req, r, http.StatusNotFound, merr)
				return
			}
			b, _ := r.Inventory.GetBinding(req.Context(), id)
			if !reimageConfirmMatches(req.FormValue("reimage_confirm"), m, b) {
				fe := fieldFormError("reimage_confirm", "Type the machine name exactly as shown to confirm the re-image.")
				fe.Message = "Re-image not queued — confirm it by typing the machine name."
				reject(fe)
				return
			}
		}
		payload, perr := actionPayloadFromForm(req)
		if perr != nil {
			field := map[string]string{
				model.BulkActionRename: "rename_new_name", model.BulkActionScript: "script_body",
				model.BulkActionSoftwarePush: "software_package_id",
			}[action]
			msg := strings.ToUpper(perr.Error()[:1]) + perr.Error()[1:] + "."
			if field == "" {
				reject(&FormError{Message: "Couldn't queue the action: " + msg})
				return
			}
			fe := fieldFormError(field, msg)
			fe.Message = "Action not queued — fix the highlighted field below."
			reject(fe)
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
		if err != nil {
			reject(&FormError{Message: "Couldn't queue the action: " + friendlyError("machine", http.StatusUnprocessableEntity, err)})
			return
		}
		r.Emitter.Emit(req.Context(), notify.BulkCreatedEvent(op))
		if action == model.BulkActionReimage {
			flash(w, "ok", "Re-image queued. The machine will reboot and re-image on its next check-in.")
		} else {
			flash(w, "ok", "Action queued for this machine.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}

// machineDeployStatus serves the live deploy progress as JSON for the machine
// detail page's progress poller, mirroring isoPrepStatus. While the latest
// deployment is open and the machine is still checking in it returns its
// phase/label/percent; otherwise it reports active=false with the latest
// row's outcome (finished=true once that run is closed, stalled=true while
// it is open but the machine went quiet) so the poller reloads into the
// settled Current state view.
func machineDeployStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := pathID(req)
		if err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		latest := model.DeploymentRecord{Outcome: "none"}
		if hist, herr := r.Inventory.HistoryFor(req.Context(), id); herr == nil && len(hist) > 0 {
			latest = hist[0]
		}
		stalled := false
		if m, merr := r.Inventory.Get(req.Context(), id); merr == nil {
			stalled = time.Since(m.LastSeen) > deployStallAfter
		}
		open, oerr := r.Inventory.LatestOpenDeployment(req.Context(), id)
		if oerr != nil || open.ID != latest.ID || stalled {
			// Nothing genuinely running: report the latest outcome.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"active": false, "finished": latest.Outcome != "in_progress",
				"outcome": latest.Outcome, "stalled": stalled && latest.Outcome == "in_progress",
			})
			return
		}
		label, percent, indeterminate := model.DeployRecordProgress(open)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true, "finished": false,
			"phase": open.Phase, "label": label, "percent": percent,
			"indeterminate": indeterminate, "outcome": open.Outcome,
			"done": open.ProgressDone, "total": open.ProgressTotal,
			"stalled": false,
		})
	}
}

// Binding validation. Rules are deliberately the ones Windows and AD
// enforce, so a save never passes here only to fail at deploy time.
var (
	// netbiosBad are characters Windows rejects in a computer name.
	netbiosBad = `\/:*?"<>|,~!@#$^&'.(){}[];=+` + "`"
	// dnPartRe is one RDN of a distinguished name: OU=Lab, DC=corp …
	dnPartRe = regexp.MustCompile(`(?i)^\s*(OU|CN|DC|O|L|ST|C)\s*=\s*\S.*$`)
)

// validateBinding checks a submitted binding and returns the field errors
// (nil when it is fine).
func validateBinding(b model.MachineBinding) *FormError {
	fe := &FormError{Fields: map[string]string{}}
	if n := b.MachineName; n != "" && !strings.Contains(n, "%") {
		// A literal name (templates with %placeholder% are expanded and
		// sanitised at deploy time, see unattend.ExpandName).
		switch {
		case len(n) > 15:
			fe.Fields["machine_name"] = fmt.Sprintf("Windows computer names can be at most 15 characters; this one has %d.", len(n))
		case strings.ContainsAny(n, netbiosBad+" \t"):
			fe.Fields["machine_name"] = "Use only letters, numbers and hyphens (no spaces, dots or other symbols)."
		case strings.Trim(n, "0123456789") == "":
			fe.Fields["machine_name"] = "A computer name can't be only numbers."
		}
	}
	if ou := b.TargetOU; ou != "" {
		for _, part := range splitDN(ou) {
			if !dnPartRe.MatchString(part) {
				fe.Fields["target_ou"] = "Enter a distinguished name such as OU=Lab,DC=corp,DC=example (comma-separated KEY=value parts)."
				break
			}
		}
	}
	if len(fe.Fields) == 0 {
		return nil
	}
	fe.Message = "Couldn't save — fix the highlighted field below. Nothing was changed."
	if len(fe.Fields) > 1 {
		fe.Message = "Couldn't save — fix the highlighted fields below. Nothing was changed."
	}
	return fe
}

// splitDN splits a DN on its unescaped commas.
func splitDN(dn string) []string {
	var parts []string
	var cur strings.Builder
	esc := false
	for _, c := range dn {
		switch {
		case esc:
			cur.WriteRune(c)
			esc = false
		case c == '\\':
			cur.WriteRune(c)
			esc = true
		case c == ',':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	return append(parts, cur.String())
}

func machineBindingSubmit(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if err := req.ParseForm(); err != nil {
			failPage(w, req, r, http.StatusBadRequest, err)
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
		// Rejected saves re-render the page with what was typed and the
		// error under the field (422), instead of a flash over the old values.
		fe := validateBinding(b)
		if fe == nil && b.ImageID != nil {
			if _, err := r.Images.Get(req.Context(), *b.ImageID); errors.Is(err, model.ErrNotFound) {
				fe = fieldFormError("image_id", "That image no longer exists. Choose another one.")
			}
		}
		if fe == nil {
			if err := r.Inventory.UpsertBinding(req.Context(), b); err != nil {
				fe = newFormError(req, "machine", err)
			}
		}
		if fe != nil {
			machineDetailPage(w, req, r, id, machineDetailForm{Binding: &b, BindingError: fe})
			return
		}
		flash(w, "ok", "Binding saved.")
		http.Redirect(w, req, fmt.Sprintf("/portal/machines/%d", id), http.StatusFound)
	}
}
