package api

import (
	"errors"
	"net/http"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// BootMenuRequest is the body a Boot Client posts to /api/v1/clients/menu.
// Identity comes from SMBIOS read pre-boot; the server is the sole authority
// for which configurations the operator is offered.
type BootMenuRequest struct {
	SystemUUID         string `json:"system_uuid"`
	SystemManufacturer string `json:"system_manufacturer"`
	SystemProduct      string `json:"system_product"`
	SystemSerial       string `json:"system_serial"`
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
// it's about to reboot into Setup. The agent reports the final "ok" once
// the installed OS boots.
type DeployStatusRequest struct {
	Identity match.Identity `json:"identity"`
	ImageID  *model.ID      `json:"image_id,omitempty"`
	// Status: "staging" (open a row), "staged" (media ready, rebooting),
	// or "failed". Mapped to deployment_history outcomes.
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
		switch in.Status {
		case "staging":
			// Open an in-progress deployment row the dashboard can show.
			id, err := r.Inventory.RecordDeployment(req.Context(), m.ID, in.ImageID)
			if err != nil {
				writeError(w, err)
				return
			}
			// Clear any remote-reimage flag now the deploy has begun, so it
			// fires exactly once and the machine doesn't re-image on every
			// subsequent network boot.
			_ = r.Inventory.ClearReimagePending(req.Context(), m.ID)
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
			if err := r.Inventory.UpdateLatestDeployment(req.Context(), m.ID, outcome, notes); err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID})
			return
		default:
			http.Error(w, "status must be staging|staged|failed", http.StatusBadRequest)
		}
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
			_, _ = r.Inventory.UpsertFromIdentity(req.Context(), match.Identity{
				SystemUUID:         in.SystemUUID,
				SystemManufacturer: in.SystemManufacturer,
				SystemProduct:      in.SystemProduct,
				SystemSerial:       in.SystemSerial,
			})
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
