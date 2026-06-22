package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// BootMenuRequest is the body a Boot Client posts to /api/v1/clients/menu.
// Identity comes from SMBIOS read pre-boot; the server is the sole authority
// for which configurations the operator is offered. The full identity is
// accepted (not just make/model/serial/UUID) so the inventory record carries
// the base-board and BIOS facts from the machine's very first menu boot —
// an operator building a driver filter on e.g. board_product needs those
// values visible in the portal before the machine has ever been deployed.
// Older boot clients that post only the original four fields still decode
// cleanly; the missing fields stay empty and never overwrite stored values.
type BootMenuRequest struct {
	match.Identity
}

// BootMenuResponse is what the client receives back. Items list deployable
// images. Reimage (Phase 9) will be set on a future revision when the
// machine matches an inventory record.
type BootMenuResponse struct {
	Items   []BootMenuItem `json:"items"`
	Reimage *BootMenuItem  `json:"reimage,omitempty"`
	// AutoDeployImageID is set (non-zero) when the machine has been flagged
	// for remote re-image. The boot client deploys this image immediately,
	// skipping the interactive menu. Zero means show the menu as usual.
	AutoDeployImageID model.ID        `json:"auto_deploy_image_id,omitempty"`
	Identity          BootMenuRequest `json:"identity_echo"`
}

// BootMenuItem is one deployable configuration.
type BootMenuItem struct {
	ImageID     model.ID `json:"image_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
}

// RegisterBoot mounts the Boot Client endpoints. Wired here so that the
// boot path is colocated with the rest of the JSON API.
func RegisterBoot(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/clients/menu", handleBootMenu(r))
	mux.HandleFunc("POST /api/v1/clients/deploy-status", handleDeployStatus(r))
}

// DeployStatusRequest is what the Boot Client posts during a deploy so the
// dashboard sees a machine before its OS (and agent) ever come up: at
// staging start, on a staging failure, and when the media is staged and
// it's about to reboot into Setup. During Windows Setup the unattend's
// install-status callbacks post milestone updates too. The agent reports the
// final "ok" once the installed OS boots.
type DeployStatusRequest struct {
	Identity match.Identity `json:"identity"`
	ImageID  *model.ID      `json:"image_id,omitempty"`
	// Status:
	//   "staging"     — open an in_progress row (Boot Client).
	//   "staged"      — media ready, rebooting into Setup (Boot Client).
	//   "specialize"  — Windows Setup reached specialize (unattend callback).
	//   "first-logon" — OOBE reached, handing to agent (unattend callback).
	//   "failed"      — staging failed (Boot Client).
	// staging opens a row; staged/specialize/first-logon update the open
	// row's note (still in_progress); failed closes it failed.
	Status string `json:"status"`
	Notes  string `json:"notes,omitempty"`
}

func handleDeployStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in DeployStatusRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		emitFirstSeen(req, r, m)
		switch in.Status {
		case "staging":
			// Classify the deploy as a re-image (and how it was triggered)
			// BEFORE opening the new history row or clearing the remote flag,
			// both of which would otherwise mask the signals.
			reimageSource := reimageSourceFor(req.Context(), r, m.ID, in.Identity.SystemUUID)
			// Open an in-progress deployment row the dashboard can show.
			id, err := r.Inventory.RecordDeployment(req.Context(), m.ID, in.ImageID)
			if err != nil {
				writeError(w, err)
				return
			}
			emitDeployStarted(req, r, m, in.ImageID, reimageSource)
			// Clear any remote-reimage flag now the deploy has begun, so it
			// fires exactly once and the machine doesn't re-image on every
			// subsequent network boot.
			_ = r.Inventory.ClearReimagePending(req.Context(), m.ID)
			// Record one concise re-image history entry (only when this deploy
			// actually rebuilds an existing machine, never on a first install).
			if reimageSource != "" {
				_ = r.Inventory.RecordReimageEvent(req.Context(), m.ID, in.ImageID, reimageSource)
			}
			// A (re)deploy rebuilds the OS, so the machine's previously
			// reported runtime state is now stale. Reset it — software
			// detection results, Windows-update compliance, and the agent
			// job queue/results — so the portal shows the freshly-imaged
			// machine accurately instead of carrying over data from its old
			// OS. Deployment history is kept (audit). Best-effort: a cleanup
			// hiccup must not block the deploy from starting.
			clearReimagedState(req.Context(), r, m.ID)
			writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID, "deployment_id": id})
			return
		case "staged", "failed":
			// Close the latest open row for this machine. "staged" stays
			// in_progress conceptually (the OS install continues), but we
			// record a note; "failed" marks it failed.
			outcome := "in_progress"
			if in.Status == "failed" {
				outcome = "failed"
			}
			notes := in.Notes
			if notes == "" && in.Status == "staged" {
				notes = "media staged; rebooting into Setup"
			}
			if err := r.Inventory.UpdateLatestDeployment(req.Context(), m.ID, outcome, notes, in.Status); err != nil {
				writeError(w, err)
				return
			}
			if outcome == "failed" {
				emitDeployOutcome(req, r, m, "failed", notes)
			}
			writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID})
			return
		case "specialize", "first-logon":
			// Milestone callbacks from the unattend during Windows Setup. They
			// advance the open in_progress row's note only — never close it and
			// never overwrite a row the agent already marked ok/failed (the
			// repo guards the race). UpsertFromIdentity above also bumped
			// last_seen, which feeds the dashboard's stall detection.
			notes := in.Notes
			if notes == "" {
				notes = "Windows Setup: " + in.Status
			}
			if err := r.Inventory.UpdateLatestInProgressNote(req.Context(), m.ID, notes, in.Status); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID})
			return
		default:
			http.Error(w, "status must be staging|staged|specialize|first-logon|failed", http.StatusBadRequest)
		}
	}
}

// reimageSourceFor classifies a staging deploy: "" for a first-time install,
// or the trigger source when the machine is being re-imaged. A remote-flagged
// machine is a remote re-image; otherwise a machine that already reached a
// terminal deploy outcome is being rebuilt from the boot menu. Must be called
// before the remote flag is cleared and before the new in_progress row is
// opened.
func reimageSourceFor(ctx context.Context, r Repos, machineID model.ID, uuid string) string {
	if r.Inventory == nil {
		return ""
	}
	if uuid != "" {
		if pending, _, err := r.Inventory.ReimagePending(ctx, uuid); err == nil && pending {
			return model.ReimageSourceRemoteFlag
		}
	}
	if prior, err := r.Inventory.HasPriorDeployment(ctx, machineID); err == nil && prior {
		return model.ReimageSourceBootMenu
	}
	return ""
}

// clearReimagedState resets the per-machine runtime state a (re)deploy
// invalidates: software detection results, Windows-update compliance and
// jobs, and bulk-job queue/results. Each step is best-effort and independent
// so one repo being absent (nil) or erroring doesn't abort the others — the
// deploy must proceed regardless. Deployment history is intentionally NOT
// touched.
func clearReimagedState(ctx context.Context, r Repos, machineID model.ID) {
	if r.Inventory != nil {
		_ = r.Inventory.ClearDetectedState(ctx, machineID)
	}
	if r.Bulk != nil {
		_ = r.Bulk.DeleteJobsForMachine(ctx, machineID)
	}
	if r.Updates != nil {
		_ = r.Updates.ClearMachineState(ctx, machineID)
	}
}

func handleBootMenu(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in BootMenuRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		// Upsert the machine record from reported identity so the
		// inventory tracks every machine that boots into AutoDeploy —
		// even ones that have no binding yet.
		if r.Inventory != nil && in.SystemUUID != "" {
			if m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity); err == nil {
				emitFirstSeen(req, r, m)
			}
		}
		images, err := r.Images.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		resp := BootMenuResponse{Identity: in}
		for _, im := range images {
			resp.Items = append(resp.Items, BootMenuItem{
				ImageID:     im.ID,
				Name:        im.Name,
				Description: im.Description,
			})
		}
		// Re-image option: present when the machine is in inventory AND
		// has a binding with an image.
		if r.Inventory != nil && in.SystemUUID != "" {
			m, err := r.Inventory.GetByUUID(req.Context(), in.SystemUUID)
			if err == nil {
				b, err := r.Inventory.GetBinding(req.Context(), m.ID)
				if err == nil && b.ImageID != nil {
					im, ierr := r.Images.Get(req.Context(), *b.ImageID)
					if ierr == nil {
						resp.Reimage = &BootMenuItem{
							ImageID:     im.ID,
							Name:        "Re-image: " + im.Name,
							Description: "Rebuild this machine to the latest definition of " + im.Name,
						}
					}
				} else if !errors.Is(err, model.ErrNotFound) && err != nil {
					// Other errors are intentionally swallowed — a menu
					// without a re-image option is still a useful menu.
					_ = err
				}
				// Remote re-image: if the machine is flagged, tell the boot
				// client to auto-deploy without waiting at the menu. The
				// flagged image (0 => the binding's image) wins.
				if pending, imgID, perr := r.Inventory.ReimagePending(req.Context(), in.SystemUUID); perr == nil && pending {
					if imgID == 0 && b.ImageID != nil {
						imgID = *b.ImageID
					}
					if imgID != 0 {
						resp.AutoDeployImageID = imgID
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
