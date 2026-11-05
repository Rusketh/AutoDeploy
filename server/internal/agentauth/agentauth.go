// Package agentauth authenticates requests from AutoDeploy agents.
//
// An agent identifies itself with its server-minted agent_id and proves it
// with its secret, sent as headers:
//
//	X-AutoDeploy-Agent-ID:     <agent_id>
//	X-AutoDeploy-Agent-Secret: <secret>
//
// A machine that holds a secret must present it on every agent request. A
// machine that has never been issued one ("legacy": an agent from before
// secrets, mid-upgrade) is still accepted the old way -- by agent_id, or by
// its SMBIOS UUID header where the old protocol used that -- while legacy
// auth is enabled, so existing sites keep working until their agents update
// and pick up a secret. A machine whose secret was revoked is refused until
// an operator approves a new one.
package agentauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Header names.
const (
	HeaderAgentID = "X-AutoDeploy-Agent-ID"
	HeaderSecret  = "X-AutoDeploy-Agent-Secret"
	HeaderUUID    = "X-AutoDeploy-Machine-UUID"
)

// Errors. ErrUnauthenticated means "this machine needs its secret" (HTTP 401:
// the agent should present or obtain one); ErrForbidden means the caller is
// unknown or not allowed (HTTP 403).
var (
	ErrUnauthenticated = errors.New("agent credential required")
	ErrForbidden       = errors.New("forbidden")
	// ErrUnknownAgent: no machine has that agent id (HTTP 404, as before).
	ErrUnknownAgent = errors.New("unknown agent")
	// ErrCloned: a valid secret presented from hardware with a different
	// SMBIOS UUID -- a clone of the machine (golden image, copied disk).
	// HTTP 409; the agent re-enrols as the machine it really is.
	ErrCloned = errors.New("cloned identity: this agent's secret belongs to another machine")
)

// Authenticator checks agent requests.
type Authenticator struct {
	Inventory *model.InventoryRepo
	// Creds holds the machines' credentials. nil treats every machine as
	// legacy (only for tests and tools wired without credentials).
	Creds *model.AgentCredentialRepo
	// AllowLegacy accepts machines that have never been issued a secret.
	AllowLegacy bool
	// OnFailure, when set, is told about a refused request for a known
	// machine (for audit logging). Never receives a secret.
	OnFailure func(req *http.Request, m model.MachineRecord, reason string)
}

// Caller is an authenticated agent.
type Caller struct {
	Machine model.MachineRecord
	// Legacy is true when the machine has no secret and was accepted the
	// old way; the agent should obtain one.
	Legacy bool
}

// StatusCode maps an authentication error to its HTTP status.
func StatusCode(err error) int {
	switch {
	case errors.Is(err, ErrCloned):
		return http.StatusConflict
	case errors.Is(err, ErrUnauthenticated):
		return http.StatusUnauthorized
	case errors.Is(err, ErrUnknownAgent):
		return http.StatusNotFound
	}
	return http.StatusForbidden
}

// Write sends the refusal for err.
func Write(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnauthenticated) {
		w.Header().Set("WWW-Authenticate", `AutoDeploy-Agent realm="autodeploy"`)
	}
	http.Error(w, err.Error(), StatusCode(err))
}

// Agent authenticates a request by agent id: the header, or idHint (where the
// old protocol carried the id in the body or query). The header wins.
func (a *Authenticator) Agent(req *http.Request, idHint string) (Caller, error) {
	id := strings.TrimSpace(req.Header.Get(HeaderAgentID))
	if id == "" {
		id = strings.TrimSpace(idHint)
	}
	if id == "" || a == nil || a.Inventory == nil {
		return Caller{}, ErrForbidden
	}
	m, err := a.Inventory.GetByAgentID(req.Context(), id)
	if err != nil {
		return Caller{}, ErrUnknownAgent
	}
	return a.Check(req, m)
}

// Any authenticates by agent id when one is present (header or idHint),
// otherwise by the SMBIOS UUID header -- which only a legacy machine may use.
func (a *Authenticator) Any(req *http.Request, idHint string) (Caller, error) {
	if strings.TrimSpace(req.Header.Get(HeaderAgentID)) != "" || strings.TrimSpace(idHint) != "" {
		return a.Agent(req, idHint)
	}
	return a.ByUUID(req, req.Header.Get(HeaderUUID))
}

// ByUUID authenticates a request the old protocol identified by SMBIOS UUID.
// The machine must already exist (callers that create machines on first
// contact upsert first, then call Check).
func (a *Authenticator) ByUUID(req *http.Request, uuid string) (Caller, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" || a == nil || a.Inventory == nil {
		return Caller{}, ErrForbidden
	}
	m, err := a.Inventory.GetByUUID(req.Context(), uuid)
	if err != nil {
		return Caller{}, ErrForbidden
	}
	return a.Check(req, m)
}

// Check authenticates a request on behalf of a machine identified some other
// way (agent id, UUID, a job's owner): a machine with a secret must present
// it, with its own agent id; a legacy machine passes while legacy auth is on.
func (a *Authenticator) Check(req *http.Request, m model.MachineRecord) (Caller, error) {
	if a == nil {
		return Caller{}, ErrForbidden
	}
	status := model.CredLegacy
	if a.Creds != nil {
		c, err := a.Creds.Get(req.Context(), m.ID)
		if err != nil {
			return Caller{}, ErrForbidden
		}
		status = c.Status
	}
	switch status {
	case model.CredSecured:
		id := strings.TrimSpace(req.Header.Get(HeaderAgentID))
		secret := req.Header.Get(HeaderSecret)
		if id == "" || id != m.AgentID || secret == "" {
			a.fail(req, m, "secret missing")
			return Caller{}, ErrUnauthenticated
		}
		// A clone carries the original's id and secret but its own
		// hardware: refuse before the secret is used (it would otherwise
		// confirm rotations on the original's behalf).
		if hw := req.Header.Get(HeaderUUID); hw != "" && m.SystemUUID != "" && !SameUUID(hw, m.SystemUUID) {
			a.fail(req, m, "secret presented from different hardware (clone)")
			return Caller{}, ErrCloned
		}
		ok, err := a.Creds.Verify(req.Context(), m.ID, secret)
		if err != nil || !ok {
			a.fail(req, m, "secret did not match")
			return Caller{}, ErrUnauthenticated
		}
		return Caller{Machine: m}, nil
	case model.CredApproval:
		a.fail(req, m, "credential revoked or awaiting approval")
		return Caller{}, ErrUnauthenticated
	default: // legacy
		if !a.AllowLegacy {
			a.fail(req, m, "legacy agent authentication is disabled")
			return Caller{}, ErrUnauthenticated
		}
		return Caller{Machine: m, Legacy: true}, nil
	}
}

func (a *Authenticator) fail(req *http.Request, m model.MachineRecord, reason string) {
	if a.OnFailure != nil {
		a.OnFailure(req, m, reason)
	}
}

// Legacy is a convenience for callers holding only a context and machine
// (e.g. background checks): it reports whether the machine is still legacy.
func (a *Authenticator) Legacy(ctx context.Context, machineID model.ID) bool {
	if a == nil || a.Creds == nil {
		return true
	}
	c, err := a.Creds.Get(ctx, machineID)
	return err == nil && c.Status == model.CredLegacy
}

// SameUUID compares two SMBIOS UUIDs as the same hardware: case, braces and
// hyphens are ignored, and the mixed-endian form some firmware and tools
// report (first three fields byte-swapped) is treated as equal. Anything that
// isn't 32 hex digits compares as a plain case-insensitive string.
func SameUUID(a, b string) bool {
	na, nb := normUUID(a), normUUID(b)
	if na == "" || nb == "" {
		return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
	}
	return na == nb || na == swapUUID(nb)
}

func normUUID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("{", "", "}", "", "-", "").Replace(s)
	if len(s) != 32 {
		return ""
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return ""
		}
	}
	return s
}

// swapUUID byte-swaps the first three fields of a normalised UUID.
func swapUUID(s string) string {
	rev := func(h string) string {
		out := make([]byte, 0, len(h))
		for i := len(h) - 2; i >= 0; i -= 2 {
			out = append(out, h[i], h[i+1])
		}
		return string(out)
	}
	return rev(s[0:8]) + rev(s[8:12]) + rev(s[12:16]) + s[16:]
}
