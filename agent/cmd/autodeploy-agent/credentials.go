package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
)

// Agent secrets. The agent proves its identity with its agent_id plus a
// secret it keeps in a protected file (SYSTEM and Administrators only on
// Windows). It obtains the secret in one of these ways:
//
//   - imported from a one-time file the boot client staged at deploy
//     (%WINDIR%\AutoDeploy\agent-credential.json);
//   - requested from the server: with the enrollment token an exported
//     USB/ISO carries (%WINDIR%\AutoDeploy\enroll.json), a deploy grant, on
//     first contact (an agent upgrading from before secrets), or after an
//     operator approves the request.
//
// It rotates the secret every rotation period without ever getting out of
// step with the server: the next secret is saved as "pending" BEFORE the
// server is asked to switch, the server keeps the old one valid until the new
// one is first used, and the switch is idempotent. Whatever fails part-way --
// a lost response, a crash, a restart -- the agent holds a secret the server
// still accepts, and simply finishes the rotation on its next check-in.

// agentCredential is the agent's on-disk credential state.
type agentCredential struct {
	AgentID string `json:"agent_id"`
	Secret  string `json:"secret,omitempty"`
	// Pending is the next secret, saved before the rotation request.
	Pending   string    `json:"pending_secret,omitempty"`
	RotatedAt time.Time `json:"rotated_at,omitempty"`
	// Nonce identifies an outstanding request for a secret (one awaiting
	// an operator), so approval reaches this agent.
	Nonce string `json:"request_nonce,omitempty"`
}

// credentialDir is where the agent keeps its secret: beside its work folder
// (C:\ProgramData\AutoDeploy\identity by default).
func credentialDir(f agentFlags) string {
	return filepath.Join(filepath.Dir(filepath.Clean(f.workDir)), "identity")
}

func credentialFile(f agentFlags) string { return filepath.Join(credentialDir(f), "credential.json") }

// stagedFile is the path of a one-time file Windows Setup copied from the
// deployment media into %WINDIR%\AutoDeploy. A package var so tests can
// point it at a temp dir; empty off Windows.
var stagedFile = func(name string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	win := os.Getenv("WINDIR")
	if win == "" {
		win = `C:\Windows`
	}
	return filepath.Join(win, "AutoDeploy", name)
}

// credentialNow is the clock credential ages are measured against; a
// package var so tests can move time on.
var credentialNow = time.Now

func loadCredential(f agentFlags) (agentCredential, error) {
	var c agentCredential
	b, err := os.ReadFile(credentialFile(f))
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	err = json.Unmarshal(b, &c)
	return c, err
}

// saveCredential writes the credential atomically (temp file + rename) in a
// directory only SYSTEM and Administrators can read.
func saveCredential(log *slog.Logger, f agentFlags, c agentCredential) error {
	dir := credentialDir(f)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	secureDir(log, dir)
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "credential-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	_ = os.Chmod(tmp.Name(), 0o600)
	return os.Rename(tmp.Name(), credentialFile(f))
}

func newRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// credManager owns the agent's credential for the life of the process.
type credManager struct {
	log *slog.Logger
	f   agentFlags

	mu          sync.Mutex
	c           agentCredential
	unsupported bool // the server predates agent secrets
	announced   string
}

// newCredManager loads the stored credential and imports a staged one. A
// stored credential for a different agent_id (the machine was re-provisioned)
// is ignored.
func newCredManager(log *slog.Logger, f agentFlags) *credManager {
	m := &credManager{log: log, f: f}
	c, err := loadCredential(f)
	if err != nil {
		log.Warn("agent.credential.error", slog.String("error", "read stored secret: "+err.Error()))
	}
	if c.AgentID != "" && f.agentID != "" && c.AgentID != f.agentID {
		c = agentCredential{}
	}
	m.c = c
	m.importStaged()
	return m
}

// importStaged takes over a secret the boot client staged at deploy and
// deletes the one-time file.
func (m *credManager) importStaged() {
	p := stagedFile("agent-credential.json")
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var in struct {
		AgentID string `json:"agent_id"`
		Secret  string `json:"secret"`
	}
	if json.Unmarshal(b, &in) != nil || in.AgentID == "" || in.Secret == "" ||
		(m.f.agentID != "" && in.AgentID != m.f.agentID) {
		_ = os.Remove(p)
		return
	}
	c := agentCredential{AgentID: in.AgentID, Secret: in.Secret, RotatedAt: credentialNow()}
	if err := saveCredential(m.log, m.f, c); err != nil {
		m.log.Warn("agent.credential.error", slog.String("error", "store staged secret: "+err.Error()))
		return // keep the file; retry on next start
	}
	_ = os.Remove(p)
	m.c = c
	m.log.Info("agent.credential.imported")
}

// Credentials is the httpc.Client callback: the agent id and current secret.
func (m *credManager) Credentials() (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.f.agentID
	if id == "" {
		id = m.c.AgentID
	}
	if m.c.AgentID != "" && m.c.AgentID != id {
		return id, ""
	}
	return id, m.c.Secret
}

// HasSecret reports whether the agent holds a secret.
func (m *credManager) HasSecret() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c.Secret != ""
}

// SetAgentID records the agent id once enrollment provided one.
func (m *credManager) SetAgentID(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.f.agentID = id
}

type credentialResponse struct {
	Status        string `json:"status"`
	AgentID       string `json:"agent_id"`
	Secret        string `json:"secret"`
	RotateSeconds int64  `json:"rotate_seconds"`
}

// Obtain asks the server for a secret when the agent has none (legacy, or
// its secret was revoked). It returns true once the agent holds one. A
// request needing an operator stays pending across check-ins (same nonce).
func (m *credManager) Obtain(ctx context.Context, c *httpc.Client) bool {
	m.mu.Lock()
	if m.c.Secret != "" {
		m.mu.Unlock()
		return true
	}
	if m.unsupported {
		m.mu.Unlock()
		return false
	}
	if m.c.Nonce == "" {
		n, err := newRandom()
		if err != nil {
			m.mu.Unlock()
			return false
		}
		m.c.Nonce = n
		m.c.AgentID = m.f.agentID
		// Persist the nonce first, so a restart keeps asking with it and an
		// operator's approval still reaches this agent.
		if err := saveCredential(m.log, m.f, m.c); err != nil {
			m.log.Warn("agent.credential.error", slog.String("error", "save request: "+err.Error()))
		}
	}
	body := map[string]any{"agent_id": m.f.agentID, "system_uuid": m.f.uuid, "nonce": m.c.Nonce}
	m.mu.Unlock()

	enrollPath := stagedFile("enroll.json")
	if enrollPath != "" {
		if b, err := os.ReadFile(enrollPath); err == nil {
			var tok struct {
				ImageID     int64  `json:"image_id"`
				EnrollToken string `json:"enroll_token"`
			}
			if json.Unmarshal(b, &tok) == nil && tok.EnrollToken != "" {
				body["image_id"], body["enroll_token"] = tok.ImageID, tok.EnrollToken
			}
		}
	}

	var resp credentialResponse
	err := c.PostJSON(ctx, "/api/v1/agent/credential", body, &resp)
	switch {
	case httpc.IsStatus(err, 404) && !httpc.IsUnknownMachine(err):
		// A server from before agent secrets: keep the old protocol.
		m.mu.Lock()
		m.unsupported = true
		m.mu.Unlock()
		return false
	case httpc.IsStatus(err, 403):
		// Denied: start a fresh request an operator can approve.
		m.mu.Lock()
		m.c.Nonce = ""
		m.mu.Unlock()
		m.announce("denied", func() { m.log.Warn("agent.credential.denied_req") })
		return false
	case err != nil:
		m.log.Warn("agent.credential.error", slog.String("error", err.Error()))
		return false
	}
	switch resp.Status {
	case "issued":
		if resp.Secret == "" {
			return false
		}
		m.mu.Lock()
		id := m.f.agentID
		if resp.AgentID != "" {
			id = resp.AgentID
		}
		next := agentCredential{AgentID: id, Secret: resp.Secret, RotatedAt: credentialNow()}
		if err := saveCredential(m.log, m.f, next); err != nil {
			// Not saved: use it for this process only and log loudly;
			// the server considers it issued, so a restart would need
			// approval. Saving is retried at the next rotation.
			m.log.Warn("agent.credential.error", slog.String("error", "store issued secret: "+err.Error()))
		}
		m.c = next
		m.mu.Unlock()
		if enrollPath != "" {
			_ = os.Remove(enrollPath)
		}
		m.log.Info("agent.credential.obtained")
		return true
	default:
		m.announce("pending", func() { m.log.Warn("agent.credential.pending") })
		return false
	}
}

// announce logs a state once per change (pending requests poll every tick).
func (m *credManager) announce(state string, fn func()) {
	m.mu.Lock()
	same := m.announced == state
	m.announced = state
	m.mu.Unlock()
	if !same {
		fn()
	}
}

// Rotate replaces the secret when it is older than every, or finishes a
// rotation a previous attempt left pending. Safe to call on every check-in.
func (m *credManager) Rotate(ctx context.Context, c *httpc.Client, every time.Duration) {
	m.mu.Lock()
	if m.c.Secret == "" || every <= 0 {
		m.mu.Unlock()
		return
	}
	if m.c.Pending == "" {
		if credentialNow().Sub(m.c.RotatedAt) < every {
			m.mu.Unlock()
			return
		}
		next, err := newRandom()
		if err != nil {
			m.mu.Unlock()
			return
		}
		m.c.Pending = next
		// Saved BEFORE the server hears of it: if we crash after the server
		// switched, we still hold the new secret.
		if err := saveCredential(m.log, m.f, m.c); err != nil {
			m.c.Pending = ""
			m.mu.Unlock()
			m.log.Warn("agent.credential.rotate_fail", slog.String("error", "save next secret: "+err.Error()))
			return
		}
	}
	pending := m.c.Pending
	m.mu.Unlock()

	// Authenticated with the current secret (the Credentials callback).
	err := c.PostJSON(ctx, "/api/v1/agent/credential/rotate", map[string]string{"new_secret": pending}, nil)
	if err != nil {
		// Unchanged on our side; the server either didn't switch (current
		// still valid) or did (current still valid as "previous"). Retry
		// next check-in.
		m.log.Warn("agent.credential.rotate_fail", slog.String("error", err.Error()))
		return
	}
	m.commitPending()
	m.log.Info("agent.credential.rotated")
}

// commitPending makes the pending secret current.
func (m *credManager) commitPending() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.c.Pending == "" {
		return
	}
	next := m.c
	next.Secret, next.Pending, next.RotatedAt = m.c.Pending, "", credentialNow()
	if err := saveCredential(m.log, m.f, next); err != nil {
		// Keep using the old secret (still valid server-side until the new
		// one is used) and retry the commit next time.
		m.log.Warn("agent.credential.rotate_fail", slog.String("error", "save new secret: "+err.Error()))
		return
	}
	m.c = next
}

// Unauthorized handles a 401 from the server. If a rotation is pending the
// server may already be on the new secret (e.g. the old one expired as
// "previous"): switch to it. Otherwise the secret was revoked or never
// existed server-side: drop it and ask for a new one (operator approval).
// Returns true when the caller should retry.
func (m *credManager) Unauthorized(ctx context.Context, c *httpc.Client) bool {
	m.mu.Lock()
	if m.c.Pending != "" {
		m.mu.Unlock()
		m.commitPending()
		return true
	}
	had := m.c.Secret != ""
	if had {
		m.c.Secret = ""
		m.c.Nonce = ""
		_ = saveCredential(m.log, m.f, m.c)
	}
	m.mu.Unlock()
	return m.Obtain(ctx, c)
}
