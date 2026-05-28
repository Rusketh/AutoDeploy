package api

import (
	"net/http"

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
		// Phase 9 fills in resp.Reimage when in.SystemUUID matches an
		// inventory record.
		writeJSON(w, http.StatusOK, resp)
	}
}
