package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// RegisterBulk mounts the bulk-operations and agent-checkin endpoints.
func RegisterBulk(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/bulk/preview", handleBulkPreview(r))
	mux.HandleFunc("POST /api/v1/bulk/operations", handleBulkCreate(r))
	mux.HandleFunc("GET /api/v1/bulk/operations", handleBulkList(r))
	mux.HandleFunc("GET /api/v1/bulk/operations/{id}", handleBulkGet(r))

	// Agent check-in.
	mux.HandleFunc("POST /api/v1/agent/checkin", handleAgentCheckin(r))
	mux.HandleFunc("POST /api/v1/agent/jobs/{id}/result", handleAgentJobResult(r))
}

func handleBulkPreview(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var t model.BulkTarget
		if err := decodeJSON(req, &t); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		machines, err := r.Bulk.PreviewTargets(req.Context(), t)
		if err != nil {
			writeError(w, err)
			return
		}
		if machines == nil {
			machines = []model.MachineRecord{}
		}
		writeJSON(w, http.StatusOK, machines)
	}
}

func handleBulkCreate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromRequest(req, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var in model.BulkOperation
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		in.CreatedBy = u.Username
		op, jobs, err := r.Bulk.CreateOperation(req.Context(), in)
		if err != nil {
			writeError(w, err)
			return
		}
		r.Emitter.Emit(req.Context(), notify.BulkCreatedEvent(op))
		// An immediate run queued its jobs above; scheduled/recurring runs
		// are woken by the scheduler when they materialise.
		if op.WakeOnLAN && r.Waker != nil && len(jobs) > 0 {
			r.Waker.WakeForJobs(req.Context(), jobs)
		}
		// AD coordination for bulk rename (§13.2). The design sequences a
		// rename as: rename -> directory update -> reboot. The directory
		// update happens here, before the per-machine job is claimed by
		// the agent, so the AD object is already at the new name when
		// the local OS rename runs and rebooting completes a successful
		// secure-channel handshake.
		//
		// Partial AD failure does NOT block queuing the local rename --
		// some operators have AD-unjoined targets in the same selection.
		// Failures are recorded as warnings on the response and as
		// secret.access-style log lines for audit.
		if op.Action == model.BulkActionRename && r.AD != nil {
			adWarnings := coordinateRenameAD(req, r, op, jobs)
			if len(adWarnings) > 0 {
				writeJSON(w, http.StatusCreated, map[string]any{
					"operation":   op,
					"jobs":        jobs,
					"ad_warnings": adWarnings,
				})
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"operation": op,
			"jobs":      jobs,
		})
	}
}

// coordinateRenameAD walks the jobs the bulk operation just produced,
// looks each machine up in inventory, and asks the Domain Integration
// Service to rename the AD object from its current binding name to
// the new name supplied in the operation payload. Returns a list of
// per-machine warnings the response can surface; the operator can
// look at the audit log for the failed cases and re-run on the
// remainder.
func coordinateRenameAD(req *http.Request, r Repos, op model.BulkOperation, jobs []model.BulkJob) []string {
	var payload struct {
		NewName string `json:"new_name"`
		Find    string `json:"rename_find"`
		Replace string `json:"rename_replace"`
	}
	if err := json.Unmarshal([]byte(op.Payload), &payload); err != nil {
		return []string{"rename payload invalid; AD rename skipped"}
	}

	var findRe *regexp.Regexp
	if payload.Find != "" {
		var err error
		findRe, err = regexp.Compile(payload.Find)
		if err != nil {
			return []string{"rename_find regex invalid: " + err.Error()}
		}
	}

	if findRe == nil && payload.NewName == "" {
		return []string{"rename payload missing new_name or rename_find; AD rename skipped"}
	}

	var warns []string
	for _, j := range jobs {
		m, err := r.Inventory.Get(req.Context(), j.MachineID)
		if err != nil {
			warns = append(warns, "machine "+itoa64(int64(j.MachineID))+": inventory lookup failed: "+err.Error())
			continue
		}
		binding, err := r.Inventory.GetBinding(req.Context(), m.ID)
		if err != nil || binding.MachineName == "" {
			// No binding (or no current name) -- nothing to rename in
			// AD. The local OS rename still runs. Common for ad-hoc
			// machines and lab targets.
			continue
		}

		// Compute per-machine name: use regex find/replace when
		// provided, otherwise fall back to the static new_name.
		newName := payload.NewName
		if findRe != nil {
			newName = findRe.ReplaceAllString(binding.MachineName, payload.Replace)
			if newName == "" || newName == binding.MachineName {
				continue // no rename needed
			}
		}
		if newName == "" {
			continue
		}

		newDN, err := r.AD.RenameComputer(req.Context(), binding.MachineName, newName)
		if err != nil {
			warns = append(warns, "machine "+binding.MachineName+": AD rename failed: "+err.Error())
			slog.Default().Error("bulk.rename.ad.fail",
				slog.String("actor", "server"),
				slog.String("target", binding.MachineName),
				slog.String("new_name", newName),
				slog.String("error", err.Error()),
			)
			continue
		}
		// Update the binding so subsequent operations see the new
		// name. Group memberships travel with the DN inside the
		// directory; nothing to do for them here.
		binding.MachineName = newName
		_ = r.Inventory.UpsertBinding(req.Context(), binding)
		slog.Default().Info("bulk.rename.ad.ok",
			slog.String("actor", "server"),
			slog.String("target", newName),
			slog.String("new_dn", newDN),
		)
	}
	return warns
}

func handleBulkList(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ops, err := r.Bulk.ListOperations(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if ops == nil {
			ops = []model.BulkOperation{}
		}
		writeJSON(w, http.StatusOK, ops)
	}
}

func handleBulkGet(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		op, jobs, err := r.Bulk.GetOperation(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"operation": op, "jobs": jobs})
	}
}

type checkinReq struct {
	Identity match.Identity `json:"identity"`
}

func handleAgentCheckin(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in checkinReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		emitFirstSeen(req, r, m)
		jobs, err := r.Bulk.ClaimJobsFor(req.Context(), m.ID, 8)
		if err != nil {
			writeError(w, err)
			return
		}
		if jobs == nil {
			jobs = []model.BulkJob{}
		}
		resp := map[string]any{
			"machine_id": m.ID,
			"jobs":       jobs,
		}
		// BitLocker reconcile info for the resident agent: whether a PIN is
		// configured and its version (updated_at), so the agent can (re)apply
		// or decrypt after deploy when the operator changes it. When a PIN is
		// set, mint a fresh short-lived token so the agent can fetch the
		// cleartext PIN from the (audited) config endpoint -- the resident
		// agent holds no live deploy token. Issued only for machines that
		// actually have a PIN to manage; the config endpoint still requires
		// and audits the token.
		if r.BitLocker != nil {
			if st, berr := r.BitLocker.PINStatus(req.Context(), m.ID); berr == nil {
				bl := map[string]any{"pin_set": st.PINSet, "version": int64(0)}
				if st.PINSet {
					bl["version"] = st.UpdatedAt.Unix()
					if tok, terr := r.Inventory.IssueDeployToken(req.Context(), m.ID, deployTokenTTL); terr == nil {
						resp["deploy_token"] = tok
					}
				}
				resp["bitlocker"] = bl
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

type jobResultReq struct {
	Status string `json:"status"`
	Result string `json:"result_json,omitempty"`
}

func handleAgentJobResult(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in jobResultReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		completedOp, err := r.Bulk.CompleteJob(req.Context(), id, in.Status, in.Result)
		if err != nil {
			writeError(w, err)
			return
		}
		emitSoftwarePushResult(req, r, id, in.Status)
		emitBulkFinished(req, r, completedOp)
		w.WriteHeader(http.StatusNoContent)
	}
}

// emitSoftwarePushResult fires software.install_ok/failed for a finished
// software_push job, naming the package and machine.
func emitSoftwarePushResult(req *http.Request, r Repos, jobID model.ID, status string) {
	if r.Emitter == nil {
		return
	}
	job, err := r.Bulk.GetJob(req.Context(), jobID)
	if err != nil || job.Action != model.BulkActionSoftwarePush {
		return
	}
	pkgName := "software package"
	var payload struct {
		PackageID int64 `json:"package_id"`
	}
	if json.Unmarshal([]byte(job.Payload), &payload) == nil && payload.PackageID > 0 && r.Software != nil {
		if pkg, perr := r.Software.Get(req.Context(), model.ID(payload.PackageID)); perr == nil {
			pkgName = pkg.Name
		}
	}
	machine := fmt.Sprintf("machine #%d", int64(job.MachineID))
	if r.Inventory != nil {
		if m, merr := r.Inventory.Get(req.Context(), job.MachineID); merr == nil {
			machine = machineDisplayName(req.Context(), r, m)
		}
	}
	ev := notify.Event{
		Name:     notify.EventSoftwareInstallOK,
		Severity: notify.SevSuccess,
		Title:    fmt.Sprintf("Installed %s on %s", pkgName, machine),
		Link:     machineLink(job.MachineID),
		Actor:    "system",
	}
	if status == "failed" {
		ev.Name = notify.EventSoftwareInstallFailed
		ev.Severity = notify.SevError
		ev.Title = fmt.Sprintf("Install of %s failed on %s", pkgName, machine)
		ev.Body = "See the bulk job result for the failing step's output."
	}
	r.Emitter.Emit(req.Context(), ev)
}
