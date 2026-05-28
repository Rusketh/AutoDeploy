package api

import (
	"net/http"

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
	Identity     match.Identity      `json:"identity"`
	ImageID      *model.ID           `json:"image_id,omitempty"`
	DeploymentID *model.ID           `json:"deployment_id,omitempty"`
	Outcome      string              `json:"outcome"` // ok | failed | in_progress
	Notes        string              `json:"notes,omitempty"`
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
		writeJSON(w, http.StatusOK, agentReportResponse{
			MachineID: machine.ID, DeploymentID: depID,
		})
	}
}
