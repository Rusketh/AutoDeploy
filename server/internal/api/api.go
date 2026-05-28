// Package api wires the JSON HTTP API for portal and client use. Routes are
// registered on the supplied mux. Domain errors map to HTTP status codes in
// one place via writeError.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
)

// Repos is the bundle of repositories the API depends on. Pass one
// constructed by the caller so this package owns no state.
type Repos struct {
	ISOs      *model.ISORepo
	Unattend  *model.UnattendRepo
	Drivers   *model.DriverPackageRepo
	Software  *model.SoftwarePackageRepo
	Loadouts  *model.SoftwareLoadoutRepo
	Images    *model.ImageRepo
	Inventory *model.InventoryRepo
	Resolver  *resolve.Resolver
	// Phase 11.
	Users    *auth.Repo
	Settings *auth.SettingsRepo
}

// Register mounts /api/v1/* routes on mux.
func Register(mux *http.ServeMux, r Repos) {
	RegisterBoot(mux, r)
	RegisterAgent(mux, r)
	RegisterInventory(mux, r)
	RegisterAuth(mux, r)

	// ISO
	mux.HandleFunc("GET /api/v1/isos", handleListISOs(r))
	mux.HandleFunc("POST /api/v1/isos", handleCreateISO(r))
	mux.HandleFunc("GET /api/v1/isos/{id}", handleGetISO(r))
	mux.HandleFunc("PUT /api/v1/isos/{id}", handleUpdateISO(r))
	mux.HandleFunc("DELETE /api/v1/isos/{id}", handleDeleteISO(r))

	// Unattend
	mux.HandleFunc("GET /api/v1/unattends", handleListUnattend(r))
	mux.HandleFunc("POST /api/v1/unattends", handleCreateUnattend(r))
	mux.HandleFunc("GET /api/v1/unattends/{id}", handleGetUnattend(r))
	mux.HandleFunc("PUT /api/v1/unattends/{id}", handleUpdateUnattend(r))
	mux.HandleFunc("DELETE /api/v1/unattends/{id}", handleDeleteUnattend(r))

	// Driver packages
	mux.HandleFunc("GET /api/v1/drivers", handleListDrivers(r))
	mux.HandleFunc("POST /api/v1/drivers", handleCreateDriver(r))
	mux.HandleFunc("GET /api/v1/drivers/{id}", handleGetDriver(r))
	mux.HandleFunc("PUT /api/v1/drivers/{id}", handleUpdateDriver(r))
	mux.HandleFunc("DELETE /api/v1/drivers/{id}", handleDeleteDriver(r))
	mux.HandleFunc("POST /api/v1/drivers/{id}/preview", handleDriverPreview(r))

	// Software packages
	mux.HandleFunc("GET /api/v1/software", handleListSoftware(r))
	mux.HandleFunc("POST /api/v1/software", handleCreateSoftware(r))
	mux.HandleFunc("GET /api/v1/software/{id}", handleGetSoftware(r))
	mux.HandleFunc("PUT /api/v1/software/{id}", handleUpdateSoftware(r))
	mux.HandleFunc("DELETE /api/v1/software/{id}", handleDeleteSoftware(r))

	// Images
	mux.HandleFunc("GET /api/v1/images", handleListImages(r))
	mux.HandleFunc("POST /api/v1/images", handleCreateImage(r))
	mux.HandleFunc("GET /api/v1/images/{id}", handleGetImage(r))
	mux.HandleFunc("PUT /api/v1/images/{id}", handleUpdateImage(r))
	mux.HandleFunc("DELETE /api/v1/images/{id}", handleDeleteImage(r))
	mux.HandleFunc("GET /api/v1/images/{id}/resolved", handleResolveImage(r))

	// Software loadouts (Phase 7).
	mux.HandleFunc("GET /api/v1/loadouts", handleListLoadouts(r))
	mux.HandleFunc("POST /api/v1/loadouts", handleCreateLoadout(r))
	mux.HandleFunc("GET /api/v1/loadouts/{id}", handleGetLoadout(r))
	mux.HandleFunc("PUT /api/v1/loadouts/{id}", handleUpdateLoadout(r))
	mux.HandleFunc("DELETE /api/v1/loadouts/{id}", handleDeleteLoadout(r))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type apiError struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		writeJSON(w, http.StatusNotFound, apiError{Error: err.Error()})
	case errors.Is(err, model.ErrConflict):
		writeJSON(w, http.StatusConflict, apiError{Error: err.Error()})
	case errors.Is(err, model.ErrInUse):
		writeJSON(w, http.StatusConflict, apiError{Error: err.Error()})
	case errors.Is(err, model.ErrCycle):
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Error: err.Error()})
	case errors.Is(err, model.ErrValidation):
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
	}
}

func parseID(r *http.Request) (model.ID, error) {
	raw := r.PathValue("id")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("invalid id")
	}
	return model.ID(n), nil
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
