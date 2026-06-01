package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterDomainJoin mounts the agent domain-join endpoint.
func RegisterDomainJoin(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/agent/domain-join", handleAgentDomainJoin(r))
}

type agentDomainJoinReq struct {
	AgentID string `json:"agent_id"`
}

// AgentDomainJoinResponse tells the agent whether to join AD and with what.
// Resolved from the machine's bound image's domain-join config; the OU is
// overridden by the machine binding's TargetOU when set. Password is a
// secret: returned only to the agent, never logged.
type AgentDomainJoinResponse struct {
	Join     bool   `json:"join"`
	Domain   string `json:"domain,omitempty"`
	OU       string `json:"ou,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
}

// handleAgentDomainJoin resolves the deploying machine's domain-join intent
// from its bound image and returns the credentials for the agent to run the
// join. Identified by the server-minted agent_id (the same capability the
// /self loop uses); the join account should be least-privilege. Logs only the
// fact of access, never the credentials.
func handleAgentDomainJoin(r Repos) http.HandlerFunc {
	noJoin := func(w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, AgentDomainJoinResponse{Join: false})
	}
	return func(w http.ResponseWriter, req *http.Request) {
		if r.DomainJoin == nil {
			noJoin(w)
			return
		}
		var in agentDomainJoinReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.GetByAgentID(req.Context(), strings.TrimSpace(in.AgentID))
		if err != nil {
			writeError(w, err)
			return
		}
		token := req.Header.Get(DeployTokenHeader)
		ok, terr := r.Inventory.ValidateDeployToken(req.Context(), m.ID, token)
		if terr != nil || !ok {
			http.Error(w, "unauthorized: missing or invalid deploy token", http.StatusUnauthorized)
			return
		}
		b, err := r.Inventory.GetBinding(req.Context(), m.ID)
		if err != nil || b.ImageID == nil {
			noJoin(w)
			return
		}
		cfg, err := r.DomainJoin.Get(req.Context(), *b.ImageID)
		if err != nil || !cfg.Enabled || cfg.Domain == "" {
			noJoin(w)
			return
		}
		ou := cfg.OU
		if b.TargetOU != "" {
			ou = b.TargetOU // per-machine binding overrides the image default
		}
		pw, err := r.DomainJoin.RetrievePassword(req.Context(), *b.ImageID)
		if err != nil && !errors.Is(err, model.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, AgentDomainJoinResponse{
			Join:     true,
			Domain:   cfg.Domain,
			OU:       ou,
			User:     cfg.JoinUser,
			Password: pw,
		})
	}
}
