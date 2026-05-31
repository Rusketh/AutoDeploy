package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterInventory mounts the machine inventory endpoints. The agent
// posts reports here; the portal reads through /api/v1/machines/*.
func RegisterInventory(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("GET /api/v1/machines", handleListMachines(r))
	mux.HandleFunc("GET /api/v1/machines/{id}", handleGetMachine(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/history", handleMachineHistory(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/binding", handleGetBinding(r))
	mux.HandleFunc("PUT /api/v1/machines/{id}/binding", handlePutBinding(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/detected", handleDetectedState(r))

	// Agent-facing report endpoint: open a deployment row, record final
	// outcome, replace detected state for the packages it just ran.
	mux.HandleFunc("POST /api/v1/agent/report", handleAgentReport(r))

	// Agent-facing hardware report: store the collected spec set.
	mux.HandleFunc("POST /api/v1/agent/hardware", handleAgentHardware(r))
}

// AgentHardwareRequest is the body the agent POSTs with its collected
// hardware spec. Identified by agent_id (the server-minted object id).
type AgentHardwareRequest struct {
	AgentID  string         `json:"agent_id"`
	Hardware model.Hardware `json:"hardware"`
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
	// DeployToken is the per-machine bearer token issued when a
	// deploy opens (outcome=in_progress). The agent stores it and
	// presents it as X-AutoDeploy-Deploy-Token on subsequent calls
	// that return secrets (currently the BitLocker config endpoint).
	// Empty on close reports.
	DeployToken string `json:"deploy_token,omitempty"`
}

// deployTokenTTL is how long an issued token stays valid. A whole
// deploy plus a generous buffer for first-logon agent work; far less
// than a typical Windows install. Beyond this the agent re-opens a
// report and gets a fresh token.
const deployTokenTTL = 24 * time.Hour

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
		// Issue / rotate a deploy token on the open report so the
		// agent has a bearer credential for subsequent secret-
		// returning calls (BitLocker config). On close reports we
		// don't issue -- the agent only needs the token while a
		// deploy is in flight.
		resp := agentReportResponse{MachineID: machine.ID, DeploymentID: depID}
		if in.Outcome == "in_progress" {
			tok, err := r.Inventory.IssueDeployToken(req.Context(), machine.ID, deployTokenTTL)
			if err != nil {
				writeError(w, err)
				return
			}
			resp.DeployToken = tok
			slog.Default().Info("deploy.token.issued",
				slog.String("actor", "server"),
				slog.String("target", "machine:"+itoa64(int64(machine.ID))),
				slog.String("note", "token value not logged; SHA-256 hash stored"),
			)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}
