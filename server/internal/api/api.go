// Package api wires the JSON HTTP API for portal and client use. Routes are
// registered on the supplied mux. Domain errors map to HTTP status codes in
// one place via writeError.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/branding"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/mscatalog"
	"github.com/rusketh/autodeploy/server/internal/notify"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/runtime"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/wol"
)

// Repos is the bundle of repositories the API depends on. Pass one
// constructed by the caller so this package owns no state.
type Repos struct {
	ISOs      *model.ISORepo
	Unattend  *model.UnattendRepo
	Drivers   *model.DriverPackageRepo
	Software  *model.SoftwarePackageRepo
	Loadouts    *model.SoftwareLoadoutRepo
	Images      *model.ImageRepo
	ImageGroups *model.ImageGroupRepo
	Inventory   *model.InventoryRepo
	Resolver  *resolve.Resolver
	// Phase 11.
	Users    *auth.Repo
	Settings *auth.SettingsRepo
	// Phase 13.
	Bulk *model.BulkRepo
	// Phase 14.
	Logs *model.LogRepo
	// Phase 15.
	Branding *branding.Repo
	// Mass-scale: payload mirrors.
	Mirrors *model.PayloadMirrorRepo
	// Runtime settings (AD, retention, throttle) — operator-managed
	// via the portal at runtime.
	Runtime *runtime.Settings
	// AD is the Domain Integration Service. Optional; nil disables
	// server-side AD coordination (the local OS rename still runs).
	AD *addomain.Service
	// Blobs is the configurable storage layer. Used by the version
	// handler to scan the downloads category for a newer agent
	// binary to advertise to checkin-mode agents.
	Blobs *storage.BlobStore
	// DomainJoin holds per-image agent-driven AD join config. Optional;
	// nil disables the agent domain-join endpoint (the agent then never
	// joins, and the legacy unattend join is used if configured).
	DomainJoin *model.DomainJoinRepo
	// SetupLock holds the per-image "setup lockout" toggle. Optional; nil
	// means /self never advertises the lock (machines deploy without the
	// branded setup screen).
	SetupLock *model.SetupLockRepo
	// Updates manages Windows Update KB patches and deployment jobs.
	Updates *model.WindowsUpdateRepo
	// MSCatalog searches the Microsoft Update Catalog for the
	// operator-driven import flow. Optional; nil disables the import
	// endpoints (manual upload always works).
	MSCatalog mscatalog.Searcher
	// Notifications handles in-portal notification CRUD and preferences.
	Notifications *model.NotificationRepo
	// WebhookRepo manages webhook endpoints and delivery logs.
	WebhookRepo *model.WebhookRepo
	// Groups manages AutoDeploy machine groups (manual + dynamic, nestable).
	Groups *model.MachineGroupRepo
	// Emitter fans out events to all notification channels.
	Emitter *notify.Emitter
	// Waker sends Wake-on-LAN packets for bulk operations that request it.
	// Optional; nil skips waking.
	Waker *wol.Waker
}

// Register mounts /api/v1/* routes on mux.
func Register(mux *http.ServeMux, r Repos) {
	RegisterBoot(mux, r)
	RegisterAgent(mux, r)
	RegisterInventory(mux, r)
	RegisterAuth(mux, r)
	RegisterBulk(mux, r)
	RegisterLogs(mux, r)
	RegisterBranding(mux, r)
	RegisterMirrors(mux, r)
	RegisterVersion(mux, r)
	RegisterDomainJoin(mux, r)
	RegisterWindowsUpdates(mux, r)
	RegisterMSCatalog(mux, r)
	RegisterNotifications(mux, r)
	RegisterWebhooks(mux, r)

	// ISO
	mux.HandleFunc("GET /api/v1/isos", requireAuth(r, handleListISOs(r)))
	mux.HandleFunc("POST /api/v1/isos", requireAuth(r, handleCreateISO(r)))
	mux.HandleFunc("GET /api/v1/isos/{id}", requireAuth(r, handleGetISO(r)))
	mux.HandleFunc("PUT /api/v1/isos/{id}", requireAuth(r, handleUpdateISO(r)))
	mux.HandleFunc("DELETE /api/v1/isos/{id}", requireAuth(r, handleDeleteISO(r)))

	// Unattend
	mux.HandleFunc("GET /api/v1/unattends", requireAuth(r, handleListUnattend(r)))
	mux.HandleFunc("POST /api/v1/unattends", requireAuth(r, handleCreateUnattend(r)))
	mux.HandleFunc("GET /api/v1/unattends/{id}", requireAuth(r, handleGetUnattend(r)))
	mux.HandleFunc("PUT /api/v1/unattends/{id}", requireAuth(r, handleUpdateUnattend(r)))
	mux.HandleFunc("DELETE /api/v1/unattends/{id}", requireAuth(r, handleDeleteUnattend(r)))

	// Driver packages
	mux.HandleFunc("GET /api/v1/drivers", requireAuth(r, handleListDrivers(r)))
	mux.HandleFunc("POST /api/v1/drivers", requireAuth(r, handleCreateDriver(r)))
	mux.HandleFunc("GET /api/v1/drivers/{id}", requireAuth(r, handleGetDriver(r)))
	mux.HandleFunc("PUT /api/v1/drivers/{id}", requireAuth(r, handleUpdateDriver(r)))
	mux.HandleFunc("DELETE /api/v1/drivers/{id}", requireAuth(r, handleDeleteDriver(r)))
	mux.HandleFunc("POST /api/v1/drivers/{id}/preview", requireAuth(r, handleDriverPreview(r)))

	// Software packages
	mux.HandleFunc("GET /api/v1/software", requireAuth(r, handleListSoftware(r)))
	mux.HandleFunc("POST /api/v1/software", requireAuth(r, handleCreateSoftware(r)))
	mux.HandleFunc("GET /api/v1/software/{id}", requireAuth(r, handleGetSoftware(r)))
	mux.HandleFunc("PUT /api/v1/software/{id}", requireAuth(r, handleUpdateSoftware(r)))
	mux.HandleFunc("DELETE /api/v1/software/{id}", requireAuth(r, handleDeleteSoftware(r)))

	// Images
	mux.HandleFunc("GET /api/v1/images", requireAuth(r, handleListImages(r)))
	mux.HandleFunc("POST /api/v1/images", requireAuth(r, handleCreateImage(r)))
	mux.HandleFunc("GET /api/v1/images/{id}", requireAuth(r, handleGetImage(r)))
	mux.HandleFunc("PUT /api/v1/images/{id}", requireAuth(r, handleUpdateImage(r)))
	mux.HandleFunc("DELETE /api/v1/images/{id}", requireAuth(r, handleDeleteImage(r)))
	mux.HandleFunc("GET /api/v1/images/{id}/resolved", requireAuth(r, handleResolveImage(r)))

	// Software loadouts (Phase 7).
	mux.HandleFunc("GET /api/v1/loadouts", requireAuth(r, handleListLoadouts(r)))
	mux.HandleFunc("POST /api/v1/loadouts", requireAuth(r, handleCreateLoadout(r)))
	mux.HandleFunc("GET /api/v1/loadouts/{id}", requireAuth(r, handleGetLoadout(r)))
	mux.HandleFunc("PUT /api/v1/loadouts/{id}", requireAuth(r, handleUpdateLoadout(r)))
	mux.HandleFunc("DELETE /api/v1/loadouts/{id}", requireAuth(r, handleDeleteLoadout(r)))

	// Machine groups (manual + dynamic, nestable).
	mux.HandleFunc("GET /api/v1/machine-groups", requireAuth(r, handleListGroups(r)))
	mux.HandleFunc("POST /api/v1/machine-groups", requireAuth(r, handleCreateGroup(r)))
	mux.HandleFunc("GET /api/v1/machine-groups/{id}", requireAuth(r, handleGetGroup(r)))
	mux.HandleFunc("PUT /api/v1/machine-groups/{id}", requireAuth(r, handleUpdateGroup(r)))
	mux.HandleFunc("DELETE /api/v1/machine-groups/{id}", requireAuth(r, handleDeleteGroup(r)))
	mux.HandleFunc("GET /api/v1/machine-groups/{id}/members", requireAuth(r, handleListGroupMembers(r)))
	mux.HandleFunc("POST /api/v1/machine-groups/{id}/members", requireAuth(r, handleAddGroupMembers(r)))
	mux.HandleFunc("DELETE /api/v1/machine-groups/{id}/members", requireAuth(r, handleRemoveGroupMembers(r)))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// itoa64 formats an int64 as a base-10 string. A small shared helper used
// when building human-readable target strings and test request bodies.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

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
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "internal server error"})
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

const maxJSONBodySize = 10 << 20 // 10 MB

func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBodySize)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// requireAuth wraps an http.HandlerFunc with a session-cookie check.
// If UserFromRequest finds no valid session, a 401 Unauthorized
// response is written and the inner handler is never called. This is
// the single guard for all operator-facing CRUD routes; agents and
// boot clients use separate, unauthenticated endpoints.
//
// For state-mutating methods (POST, PUT, DELETE) it also enforces a
// CSRF defence-in-depth check: the request must carry an
// X-Requested-With header (any non-empty value). Browsers will not
// send custom headers in a cross-origin request without a CORS
// preflight, so this blocks forged form submissions even if
// SameSite=Lax is somehow bypassed.
func requireAuth(r Repos, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// CSRF: require X-Requested-With on state-mutating methods.
		switch req.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete:
			if req.Header.Get("X-Requested-With") == "" {
				http.Error(w, "missing X-Requested-With header", http.StatusForbidden)
				return
			}
		}
		h(w, req)
	}
}

// validateName checks that an entity name is between 1 and 200
// characters and does not contain characters that could enable XSS
// when rendered in HTML templates (<, >, ", or null bytes). Returns a
// model.ErrValidation-wrapped error so writeError maps it to 400.
func validateName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("%w: name must not be empty", model.ErrValidation)
	}
	if len(name) > 200 {
		return fmt.Errorf("%w: name must be at most 200 characters", model.ErrValidation)
	}
	if strings.ContainsAny(name, "<>\"") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("%w: name contains forbidden characters", model.ErrValidation)
	}
	return nil
}
