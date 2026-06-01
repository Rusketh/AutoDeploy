package api

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterBitLocker mounts the BitLocker endpoints. Operator-facing
// endpoints require an authenticated session; agent endpoints are
// machine-identified.
func RegisterBitLocker(mux *http.ServeMux, r Repos) {
	// Operator side.
	mux.HandleFunc("PUT /api/v1/machines/{id}/bitlocker/pin", handleSetBitLockerPIN(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/bitlocker", handleGetBitLockerStatus(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/bitlocker/pin", handleRetrieveBitLockerPIN(r))
	mux.HandleFunc("GET /api/v1/machines/{id}/bitlocker/recovery-keys", handleListRecoveryKeys(r))
	mux.HandleFunc("GET /api/v1/recovery-keys/{id}", handleRetrieveRecoveryKey(r))

	// Agent side.
	mux.HandleFunc("POST /api/v1/agent/bitlocker/config", handleAgentBitLockerConfig(r))
	mux.HandleFunc("POST /api/v1/agent/bitlocker/escrow", handleAgentEscrowRecoveryKey(r))
}

type setPINReq2 struct {
	PIN string `json:"pin"` // empty clears
}

func handleSetBitLockerPIN(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		var in setPINReq2
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := r.BitLocker.SetPIN(req.Context(), id, in.PIN); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Audit: log the FACT of a PIN change, not the value.
		slog.Default().Info("bitlocker.pin.set",
			slog.String("actor", "portal:operator"),
			slog.String("target", "machine:"+itoa64(int64(id))),
			slog.Bool("pin_set", in.PIN != ""))
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleGetBitLockerStatus(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		s, err := r.BitLocker.PINStatus(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, s)
	}
}

func handleRetrieveBitLockerPIN(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromRequest(req, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		pin, err := r.BitLocker.RetrievePIN(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		// Audit access — log who retrieved which secret, never the value.
		slog.Default().Warn("secret.access",
			slog.String("actor", "portal:"+u.Username),
			slog.String("target", "bitlocker.pin:machine:"+itoa64(int64(id))),
			slog.String("note", "value returned to client; not logged"),
		)
		writeJSON(w, http.StatusOK, map[string]any{"pin": pin})
	}
}

func handleListRecoveryKeys(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.BitLocker.ListRecoveryKeys(req.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if v == nil {
			v = []model.RecoveryKeyRecord{}
		}
		writeJSON(w, http.StatusOK, v)
	}
}

func handleRetrieveRecoveryKey(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		u, ok := UserFromRequest(req, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := parseID(req)
		if err != nil {
			writeError(w, err)
			return
		}
		v, err := r.BitLocker.RetrieveRecoveryKey(req.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		slog.Default().Warn("secret.access",
			slog.String("actor", "portal:"+u.Username),
			slog.String("target", "bitlocker.recovery_key:"+itoa64(int64(id))),
			slog.String("note", "value returned to client; not logged"),
		)
		writeJSON(w, http.StatusOK, v)
	}
}

// Agent endpoints.

type agentBLConfigReq struct {
	Identity match.Identity `json:"identity"`
}

type agentBLConfigResp struct {
	PINSet bool   `json:"pin_set"`
	PIN    string `json:"pin,omitempty"` // present only if pin_set
}

// DeployTokenHeader is the header the agent sets to authenticate to
// secret-returning endpoints. The token is issued by the agent-report
// endpoint at deploy time and never logged.
const DeployTokenHeader = "X-AutoDeploy-Deploy-Token"

func handleAgentBitLockerConfig(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in agentBLConfigReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		// Bearer-token check. SMBIOS UUID alone is too weak a
		// credential to disclose a cleartext PIN -- the audit
		// (§10.2) flagged this. The token was issued to the agent
		// during the open report at the start of this deploy, so
		// only the freshly deploying client has it.
		token := req.Header.Get(DeployTokenHeader)
		ok, err := r.Inventory.ValidateDeployToken(req.Context(), m.ID, token)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			slog.Default().Warn("deploy.token.invalid",
				slog.String("actor", "agent:"+in.Identity.SystemUUID),
				slog.String("target", "machine:"+itoa64(int64(m.ID))),
				slog.String("endpoint", "bitlocker/config"),
			)
			http.Error(w, "unauthorized: missing or invalid deploy token", http.StatusUnauthorized)
			return
		}
		st, err := r.BitLocker.PINStatus(req.Context(), m.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := agentBLConfigResp{PINSet: st.PINSet}
		if st.PINSet {
			pin, err := r.BitLocker.RetrievePIN(req.Context(), m.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out.PIN = pin
			slog.Default().Warn("secret.access",
				slog.String("actor", "agent:"+in.Identity.SystemUUID),
				slog.String("target", "bitlocker.pin:machine:"+itoa64(int64(m.ID))),
				slog.String("note", "PIN returned to agent for encryption"),
			)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type agentEscrowReq struct {
	Identity     match.Identity `json:"identity"`
	RecoveryKey  string         `json:"recovery_key"`
	Note         string         `json:"note,omitempty"`
}

func handleAgentEscrowRecoveryKey(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in agentEscrowReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.UpsertFromIdentity(req.Context(), in.Identity)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := r.BitLocker.EscrowRecoveryKey(req.Context(), m.ID, in.RecoveryKey, in.Note); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Default().Info("bitlocker.recovery_key.escrowed",
			slog.String("actor", "agent:"+in.Identity.SystemUUID),
			slog.String("target", "machine:"+itoa64(int64(m.ID))),
			slog.String("note", "key value not logged"),
		)
		w.WriteHeader(http.StatusCreated)
	}
}

func itoa64(n int64) string {
	return strconv.FormatInt(n, 10)
}
