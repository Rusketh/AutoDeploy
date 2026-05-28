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
	Items    []BootMenuItem `json:"items"`
	Reimage  *BootMenuItem  `json:"reimage,omitempty"`
	Identity BootMenuRequest `json:"identity_echo"`
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
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
