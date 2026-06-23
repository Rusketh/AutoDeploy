package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterInventory mounts the machine inventory endpoints. The
// operator-facing /api/v1/machines/* routes require a session (the
// portal itself renders machines through its own /portal/machines/*
// handlers; these JSON routes are for API scripting). The agent-facing
// report endpoints below are intentionally unauthenticated — clients
// authenticate by SMBIOS identity, not a session.
func RegisterInventory(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/machines", requireAuth(r, handleListMachines(r)))
	mux.HandleFunc("GET /api/v1/machines/{id}", requireAuth(r, handleGetMachine(r)))
	mux.HandleFunc("GET /api/v1/machines/{id}/history", requireAuth(r, handleMachineHistory(r)))
	mux.HandleFunc("GET /api/v1/machines/{id}/binding", requireAuth(r, handleGetBinding(r)))
	mux.HandleFunc("PUT /api/v1/machines/{id}/binding", requireAuth(r, handlePutBinding(r)))
	mux.HandleFunc("POST /api/v1/machines/bulk-binding", requireAuth(r, handleBulkBinding(r)))
	mux.HandleFunc("GET /api/v1/machines/{id}/detected", requireAuth(r, handleDetectedState(r)))
	mux.HandleFunc("DELETE /api/v1/machines/{id}", requireAuth(r, handleDeleteMachine(r)))

	// Agent-facing report endpoint: open a deployment row, record final
	// outcome, replace detected state for the packages it just ran.
	mux.HandleFunc("POST /api/v1/agent/report", handleAgentReport(r))

	// Agent-facing enrollment: register (or refresh) a machine from its
	// SMBIOS identity and return its server-minted agent_id. Manually
	// installed agents call this once on first contact so they can persist
	// the same registry config the PXE flow provisions, after which they
	// report hardware/identity and poll /agent/self like any machine.
	mux.HandleFunc("POST /api/v1/agent/enroll", handleAgentEnroll(r))

	// Agent-facing hardware report: store the collected spec set.
	mux.HandleFunc("POST /api/v1/agent/hardware", handleAgentHardware(r))

	// Agent-facing observed-identity report: the machine's current computer
	// name and AD distinguished name. Stored on the record and synced into
	// the binding so the inventory tracks manual renames and AD moves.
	mux.HandleFunc("POST /api/v1/agent/identity", handleAgentIdentity(r))

	// Agent-facing deploy-progress report: advance the live progress bar
	// through the agent-run phases (agent-online, then installing done/total)
	// without closing the deployment row. The final ok/failed still goes via
	// /api/v1/agent/report.
	mux.HandleFunc("POST /api/v1/agent/deploy-progress", handleAgentDeployProgress(r))
}

// AgentDeployProgressRequest is posted by the running agent to advance a
// still-open deployment through the software phase. It never closes the row;
// the terminal ok/failed report (AgentReportRequest) does that.
type AgentDeployProgressRequest struct {
	Identity     match.Identity `json:"identity"`
	DeploymentID model.ID       `json:"deployment_id"`
	// Phase is one of the agent-driven tokens: "agent-online" (the agent is
	// alive, so Windows is installed) or "installing" (running the software
	// set). Other values are rejected so a bad token can't park the bar.
	Phase string `json:"phase"`
	Done  int    `json:"done"`  // packages installed so far (installing phase)
	Total int    `json:"total"` // packages to install for this deploy
	Notes string `json:"notes,omitempty"`
}

func handleAgentDeployProgress(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentDeployProgressRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		if in.Phase != "agent-online" && in.Phase != "installing" {
			http.Error(w, "phase must be agent-online|installing", http.StatusBadRequest)
			return
		}
		if in.DeploymentID == 0 {
			http.Error(w, "deployment_id required", http.StatusBadRequest)
			return
		}
		// Touch last_seen so the dashboard's stall detection stays fresh through
		// a long software install. Best-effort: a missing/blank identity must
		// not block the progress update, which is keyed by deployment id.
		if in.Identity.SystemUUID != "" {
			_, _ = r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		}
		if err := r.Inventory.SetDeploymentProgress(req.Context(), in.DeploymentID, in.Phase, in.Notes, in.Done, in.Total); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// AgentIdentityRequest is the body the running agent POSTs each poll with its
// OBSERVED identity: the current Windows computer name and AD distinguished
// name. The server stores them and syncs the binding so a manual rename or AD
// move is reflected in inventory without operator action.
type AgentIdentityRequest struct {
	AgentID             string `json:"agent_id"`
	ComputerName        string `json:"computer_name"`
	ADDistinguishedName string `json:"ad_distinguished_name"`
}

func handleAgentIdentity(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentIdentityRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		name := strings.TrimSpace(in.ComputerName)
		dn := strings.TrimSpace(in.ADDistinguishedName)
		m, err := r.Inventory.UpdateObservedIdentity(req.Context(), in.AgentID, name, dn)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Inventory.SyncBindingFromObserved(req.Context(), m.ID, name, ouFromDN(dn)); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID})
	}
}

// ouFromDN returns the parent-container DN of a computer's distinguished name
// -- the DN with its leading CN=<name> RDN stripped -- which is the AD OU the
// machine currently lives in. Literal commas in an RDN are escaped as "\,";
// the split skips those. Returns "" when the DN is empty or has no parent.
func ouFromDN(dn string) string {
	dn = strings.TrimSpace(dn)
	for i := 0; i < len(dn); i++ {
		if dn[i] == '\\' { // escaped char -- skip the next byte
			i++
			continue
		}
		if dn[i] == ',' {
			return strings.TrimSpace(dn[i+1:])
		}
	}
	return ""
}

// AgentHardwareRequest is the body the agent POSTs with its collected
// hardware spec. Identified by agent_id (the server-minted object id).
// Identity is optional: agents that can read SMBIOS via WMI include it so
// a manually-installed machine (which never PXE-boots) still gets its
// make/model and base-board facts into inventory — without them the
// machine sits as a bare UUID and can't seed driver filters.
type AgentHardwareRequest struct {
	AgentID  string         `json:"agent_id"`
	Hardware model.Hardware `json:"hardware"`
	Identity match.Identity `json:"identity,omitempty"`
}

func handleAgentHardware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentHardwareRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		m, err := r.Inventory.GetByAgentID(req.Context(), in.AgentID)
		if err != nil {
			writeError(w, err)
			return
		}
		// Fold any reported SMBIOS identity into the machine record first.
		// Guarded by the UUID matching the agent_id's record so a confused
		// client can't relabel a different machine; absent fields never
		// overwrite stored values (UpsertFromIdentity COALESCEs).
		if in.Identity.SystemUUID != "" && in.Identity.SystemUUID == m.SystemUUID {
			if _, uerr := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity); uerr != nil {
				writeError(w, uerr)
				return
			}
		}
		if err := r.Inventory.UpdateHardware(req.Context(), m.ID, in.Hardware); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"machine_id": m.ID})
	}
}

func handleListMachines(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		m, err := r.Inventory.List(req.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		if m == nil {
			m = []model.MachineRecord{}
		}
		writeJSON(w, http.StatusOK, m)
	}
}

func handleGetMachine(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.Inventory.Get(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleMachineHistory(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		h, err := r.Inventory.HistoryFor(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if h == nil {
			h = []model.DeploymentRecord{}
		}
		writeJSON(w, http.StatusOK, h)
	}
}

func handleGetBinding(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		b, err := r.Inventory.GetBinding(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}

func handlePutBinding(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in model.MachineBinding
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		in.MachineID = id
		if err := r.Inventory.UpsertBinding(req.Context(), in); err != nil {
			writeError(w, err)
			return
		}
		b, _ := r.Inventory.GetBinding(req.Context(), id)
		writeJSON(w, http.StatusOK, b)
	}
}

func handleDeleteMachine(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.Inventory.Delete(req.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": int64(id)})
	}
}

func handleDetectedState(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		s, err := r.Inventory.DetectedStateFor(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		if s == nil {
			s = []model.DetectedState{}
		}
		writeJSON(w, http.StatusOK, s)
	}
}

// AgentReportRequest is the body the agent POSTs at the end of a deploy
// (or after a check-in run in Phase 13). Carries identity (so the server
// can find or create the machine record) and per-package outcomes.
type AgentReportRequest struct {
	Identity     match.Identity       `json:"identity"`
	ImageID      *model.ID            `json:"image_id,omitempty"`
	DeploymentID *model.ID            `json:"deployment_id,omitempty"`
	Outcome      string               `json:"outcome"` // ok | failed | in_progress
	Notes        string               `json:"notes,omitempty"`
	Packages     []AgentPackageReport `json:"packages,omitempty"`
}

// AgentPackageReport is one package's final state after the agent ran.
type AgentPackageReport struct {
	PackageID model.ID `json:"package_id"`
	Detected  bool     `json:"detected"`
	Installed bool     `json:"installed"`
	Skipped   bool     `json:"skipped"`
	Failed    bool     `json:"failed"`
	Message   string   `json:"message,omitempty"`
}

type agentReportResponse struct {
	MachineID    model.ID `json:"machine_id"`
	DeploymentID model.ID `json:"deployment_id"`
	// AgentID is the machine's server-minted resident identity. Returned
	// so a manually run agent (no registry provisioning) can adopt and
	// persist it, then report hardware/identity and poll /agent/self like
	// a PXE-provisioned machine.
	AgentID string `json:"agent_id,omitempty"`
}

// handleBulkBinding applies one binding edit (image / name template / OU /
// group membership changes) to many machines at once. Bindings are
// INTENDED state — the changes take effect at the next deploy, join, or
// rename, exactly like editing a single machine's binding.
func handleBulkBinding(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			MachineIDs []model.ID        `json:"machine_ids"`
			Set        model.BindingEdit `json:"set"`
		}
		if err := decodeJSON(req, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		// A set image must exist — catch it once here instead of as a
		// per-machine FK failure.
		if in.Set.ImageID != nil && *in.Set.ImageID != 0 && r.Images != nil {
			if _, err := r.Images.Get(req.Context(), *in.Set.ImageID); err != nil {
				if errors.Is(err, model.ErrNotFound) {
					writeJSON(w, http.StatusBadRequest, apiError{Error: "image not found"})
					return
				}
				writeError(w, err)
				return
			}
		}
		updated, err := r.Inventory.BulkEditBindings(req.Context(), in.MachineIDs, in.Set)
		if err != nil {
			writeError(w, err)
			return
		}
		if r.Logs != nil {
			user, _ := UserFromRequest(req, r)
			fields, _ := json.Marshal(map[string]any{
				"machines": len(in.MachineIDs), "updated": updated, "set": in.Set,
			})
			_ = r.Logs.Append(req.Context(), model.LogEvent{
				Component: "inventory",
				Actor:     user.Username,
				Action:    "binding.bulk_edit",
				Target:    "machines:" + strconv.Itoa(len(in.MachineIDs)),
				Fields:    string(fields),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"matched": len(in.MachineIDs),
			"updated": updated,
		})
	}
}

// handleAgentEnroll upserts a machine by SMBIOS identity and hands back
// its agent_id — the same trust model as the boot-client manifest flow,
// which also derives the id from unauthenticated hardware identity.
func handleAgentEnroll(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Identity match.Identity `json:"identity"`
		}
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
		_ = r.Inventory.TouchLastSeen(req.Context(), m.ID)
		writeJSON(w, http.StatusOK, map[string]any{
			"machine_id": m.ID,
			"agent_id":   m.AgentID,
		})
	}
}

func handleAgentReport(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentReportRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		machine, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		emitFirstSeen(req, r, machine)
		// Open or reuse the deployment row.
		depID := model.ID(0)
		if in.DeploymentID != nil {
			depID = *in.DeploymentID
		} else {
			depID, err = r.Inventory.RecordDeployment(req.Context(), machine.ID, in.ImageID)
			if err != nil {
				writeError(w, err)
				return
			}
		}
		if in.Outcome == "" {
			in.Outcome = "in_progress"
		}
		if in.Outcome != "in_progress" {
			if err := r.Inventory.CompleteDeployment(req.Context(), depID, in.Outcome, in.Notes); err != nil {
				writeError(w, err)
				return
			}
			emitDeployOutcome(req, r, machine, in.Outcome, in.Notes)
		}
		// Record per-package detection state.
		for _, p := range in.Packages {
			if err := r.Inventory.RecordDetectedState(req.Context(), model.DetectedState{
				MachineID:         machine.ID,
				SoftwarePackageID: p.PackageID,
				Detected:          p.Detected,
			}); err != nil {
				writeError(w, err)
				return
			}
		}
		// A machine deployed by a manually run agent has no operator-
		// created binding; record the deployed image on it so the
		// inventory shows what's on the box. Never overwrites an image an
		// operator already bound. Best-effort.
		if in.Outcome == "in_progress" && in.ImageID != nil {
			_ = r.Inventory.EnsureBindingImage(req.Context(), machine.ID, *in.ImageID)
		}
		resp := agentReportResponse{MachineID: machine.ID, DeploymentID: depID, AgentID: machine.AgentID}
		writeJSON(w, http.StatusOK, resp)
	}
}
