package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/agentauth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// RegisterAgentCredentials mounts the endpoints an agent uses to obtain and
// rotate its secret.
func RegisterAgentCredentials(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/agent/credential", handleAgentCredentialRequest(r))
	mux.HandleFunc("POST /api/v1/agent/credential/rotate", handleAgentCredentialRotate(r))
	mux.HandleFunc("GET /api/v1/agent/whoami", handleAgentWhoami(r))
}

type agentCredentialReq struct {
	// AgentID or SystemUUID names the machine.
	AgentID    string `json:"agent_id,omitempty"`
	SystemUUID string `json:"system_uuid,omitempty"`
	// Nonce is the requester's random id for this request (the agent keeps
	// it until the request is decided, so approval reaches the right agent).
	Nonce string `json:"nonce"`
	// ImageID + EnrollToken: the token baked into an exported USB/ISO.
	ImageID     model.ID `json:"image_id,omitempty"`
	EnrollToken string   `json:"enroll_token,omitempty"`
}

// AgentCredentialResponse answers a credential request.
type AgentCredentialResponse struct {
	// Status is "issued", "pending" (awaiting an operator) or "denied".
	Status  string `json:"status"`
	AgentID string `json:"agent_id,omitempty"`
	// Secret is present only when Status is "issued". Shown once.
	Secret        string `json:"secret,omitempty"`
	RotateSeconds int64  `json:"rotate_seconds,omitempty"`
}

// handleAgentCredentialRequest hands a machine's agent its secret when the
// request is authorised, else records it for an operator to decide:
//
//   - exported USB/ISO media: a valid enrollment token for the image;
//   - a deploy grant left by an authorised network deploy (older boot clients);
//   - first contact: the machine has never had a secret (an agent upgrading
//     from before secrets, or a new manual install) and legacy auth is on;
//   - an operator approved this exact request (nonce).
//
// Anything else -- above all a machine that already has a secret -- becomes a
// pending request, raising a notification, so knowing a machine's agent_id
// or UUID is never enough to take it over.
func handleAgentCredentialRequest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Credentials == nil {
			http.Error(w, "agent credentials not configured", http.StatusNotFound)
			return
		}
		var in agentCredentialReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if len(in.Nonce) < 32 {
			http.Error(w, "nonce must be at least 32 characters", http.StatusBadRequest)
			return
		}
		ctx := req.Context()
		var m model.MachineRecord
		var err error
		switch {
		case strings.TrimSpace(in.AgentID) != "":
			m, err = r.Inventory.GetByAgentID(ctx, strings.TrimSpace(in.AgentID))
		case strings.TrimSpace(in.SystemUUID) != "":
			m, err = r.Inventory.GetByUUID(ctx, strings.TrimSpace(in.SystemUUID))
		default:
			err = model.ErrNotFound
		}
		if err != nil {
			http.Error(w, "unknown machine", http.StatusNotFound)
			return
		}
		state, err := r.Credentials.Get(ctx, m.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		auth := agentAuth(r)

		via := ""
		switch {
		case in.ImageID > 0 && r.Credentials.ValidImageEnrollToken(ctx, in.ImageID, in.EnrollToken):
			via = "media"
		case r.Credentials.RedeemDeployGrant(ctx, m.ID):
			via = "deploy"
		case state.Status == model.CredLegacy && auth.AllowLegacy && strings.TrimSpace(in.AgentID) == m.AgentID:
			via = "first-contact"
		case state.Status == model.CredLegacy && r.AgentAutoEnrollNew && time.Since(m.FirstSeen) < NewMachineWindow &&
			strings.TrimSpace(in.AgentID) == m.AgentID:
			// A machine the server has only just met (new install, fresh
			// clone): there is no existing identity to take over.
			via = "new-machine"
		}
		reason := "this machine already has a secret"
		switch state.Status {
		case model.CredLegacy:
			reason = "first contact while legacy agent authentication is off"
		case model.CredApproval:
			reason = "the machine's secret was revoked"
		}
		if via == "" {
			cr, created, rerr := r.Credentials.RequestCredential(ctx, m.ID, in.Nonce, reason, clientIP(req))
			if rerr != nil {
				writeError(w, rerr)
				return
			}
			switch cr.Status {
			case model.CredRequestApproved:
				if err := r.Credentials.MarkRedeemed(ctx, cr.ID); err != nil {
					writeError(w, err)
					return
				}
				via = "approved"
			case model.CredRequestDenied, model.CredRequestRedeemed:
				writeJSON(w, http.StatusForbidden, AgentCredentialResponse{Status: model.CredRequestDenied})
				return
			default:
				if created {
					announceCredentialRequest(req, r, m, cr, reason)
				}
				writeJSON(w, http.StatusAccepted, AgentCredentialResponse{Status: model.CredRequestPending})
				return
			}
		}
		// Every path here hands the secret to the OS now running (media and
		// deploy grants are redeemed by the freshly installed OS), so the
		// old secret is replaced outright.
		secret, err := r.Credentials.Issue(ctx, m.ID, via, false)
		if err != nil {
			writeError(w, err)
			return
		}
		if via == "media" {
			_ = r.Credentials.SetIssuedImage(ctx, m.ID, in.ImageID)
		}
		recordEvent(req, r, model.LogEvent{
			Actor:     m.SystemUUID,
			Action:    "agent.credential.issued",
			Target:    fmt.Sprintf("machine:%d", int64(m.ID)),
			MachineID: m.ID,
			Fields:    fieldsJSON(map[string]any{"via": via, "ip": clientIP(req), "note": "secret not logged"}),
		})
		writeJSON(w, http.StatusOK, AgentCredentialResponse{
			Status: "issued", AgentID: m.AgentID, Secret: secret,
			RotateSeconds: int64(r.AgentSecretRotation.Seconds()),
		})
	}
}

func announceCredentialRequest(req *http.Request, r Repos, m model.MachineRecord, cr model.CredentialRequest, reason string) {
	recordEvent(req, r, model.LogEvent{
		Actor:     m.SystemUUID,
		Action:    "agent.credential.requested",
		Target:    fmt.Sprintf("machine:%d", int64(m.ID)),
		MachineID: m.ID,
		Fields:    fieldsJSON(map[string]any{"request_id": int64(cr.ID), "reason": reason, "ip": cr.RemoteAddr}),
	})
	if r.Emitter == nil {
		return
	}
	r.Emitter.Emit(req.Context(), notify.Event{
		Name:     notify.EventAgentCredentialRequest,
		Severity: notify.SevWarning,
		Title:    "Agent secret requested for " + machineDisplayName(req.Context(), r, m),
		Body:     "Approve it on the machine's page only if this machine was just re-imaged or re-installed (" + reason + "; from " + cr.RemoteAddr + ").",
		Link:     machineLink(m.ID) + "#agent-credential",
		Actor:    m.SystemUUID,
	})
}

// NewMachineWindow is how recently a machine must have been first seen to
// count as new for AgentAutoEnrollNew.
const NewMachineWindow = time.Hour

// handleAgentWhoami lets an agent check that its agent id belongs to the
// hardware it is running on, so a clone -- e.g. of a golden image captured
// with the agent already enrolled -- notices and re-enrols as itself. It
// needs no secret (a clone of a machine that never had one, or whose secret
// it can't use, must still find out) and reveals nothing beyond a yes/no
// about the caller's own claim: same_hardware compares the caller's
// X-AutoDeploy-Machine-UUID with the machine the id belongs to.
func handleAgentWhoami(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := strings.TrimSpace(req.URL.Query().Get("id"))
		hw := strings.TrimSpace(req.Header.Get(agentauth.HeaderUUID))
		if id == "" || hw == "" {
			http.Error(w, "id and machine UUID header required", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.GetByAgentID(req.Context(), id)
		if err != nil {
			agentauth.Write(w, agentauth.ErrUnknownAgent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{
			"same_hardware": m.SystemUUID == "" || agentauth.SameUUID(hw, m.SystemUUID),
		})
	}
}

type agentRotateReq struct {
	NewSecret string `json:"new_secret"`
}

// handleAgentCredentialRotate replaces the calling agent's secret with the
// one it generated, authorised by its current (or still-valid previous)
// secret. Idempotent, so a lost response is simply retried by the agent.
func handleAgentCredentialRotate(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Credentials == nil {
			http.Error(w, "agent credentials not configured", http.StatusNotFound)
			return
		}
		var in agentRotateReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		id := strings.TrimSpace(req.Header.Get(agentauth.HeaderAgentID))
		m, err := r.Inventory.GetByAgentID(req.Context(), id)
		if id == "" || err != nil {
			agentauth.Write(w, agentauth.ErrUnauthenticated)
			return
		}
		err = r.Credentials.Rotate(req.Context(), m.ID, req.Header.Get(agentauth.HeaderSecret), in.NewSecret)
		switch {
		case errors.Is(err, model.ErrBadSecret):
			agentauth.Write(w, agentauth.ErrUnauthenticated)
			return
		case err != nil:
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok", "rotate_seconds": int64(r.AgentSecretRotation.Seconds()),
		})
	}
}
