package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
)

// fakeCredServer implements the server's credential rules (current +
// previous secret, idempotent rotation, nonce-keyed approvals) with fault
// injection, so the agent's side can be checked for never falling out of
// step.
type fakeCredServer struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	agentID   string
	cur, prev string
	legacy    bool // no credential yet: first contact issues one
	approval  map[string]string
	// faults
	dropRotateResponse int // apply the rotation, then answer 500 this many times
	noEndpoint         bool
	// observations
	unauthorized int
	rotations    int
	requests     []map[string]any
}

func newFakeCredServer(t *testing.T, agentID string) *fakeCredServer {
	s := &fakeCredServer{t: t, agentID: agentID, legacy: true, approval: map[string]string{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeCredServer) authOK(r *http.Request) bool {
	sec := r.Header.Get("X-AutoDeploy-Agent-Secret")
	if r.Header.Get("X-AutoDeploy-Agent-ID") != s.agentID || sec == "" {
		return false
	}
	if sec == s.cur {
		s.prev = "" // using the new secret confirms the rotation
		return true
	}
	return sec == s.prev && s.prev != ""
}

func (s *fakeCredServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/api/v1/agent/self":
		if s.legacy {
			_, _ = w.Write([]byte(`{"credential_status":"legacy"}`))
			return
		}
		if !s.authOK(r) {
			s.unauthorized++
			http.Error(w, "agent credential required", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"credential_status":"secured","credential_rotate_seconds":3600}`))
	case "/api/v1/agent/credential":
		if s.noEndpoint {
			http.NotFound(w, r)
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		s.requests = append(s.requests, in)
		nonce, _ := in["nonce"].(string)
		issue := func() {
			sec, _ := newRandom()
			s.cur, s.prev, s.legacy = sec, "", false
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "issued", "agent_id": s.agentID, "secret": sec})
		}
		switch {
		case in["enroll_token"] == "media-token":
			issue()
		case s.legacy:
			issue()
		case s.approval[nonce] == "approved":
			s.approval[nonce] = "redeemed"
			issue()
		case s.approval[nonce] == "denied":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"denied"}`))
		default:
			s.approval[nonce] = "pending"
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"pending"}`))
		}
	case "/api/v1/agent/credential/rotate":
		if !s.authOK(r) {
			s.unauthorized++
			http.Error(w, "agent credential required", http.StatusUnauthorized)
			return
		}
		var in struct {
			NewSecret string `json:"new_secret"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		presented := r.Header.Get("X-AutoDeploy-Agent-Secret")
		if in.NewSecret != s.cur {
			s.prev, s.cur = presented, in.NewSecret
			s.rotations++
		}
		if s.dropRotateResponse > 0 {
			s.dropRotateResponse--
			http.Error(w, "lost", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	default:
		http.NotFound(w, r)
	}
}

type credHarness struct {
	srv  *fakeCredServer
	f    agentFlags
	m    *credManager
	c    *httpc.Client
	now  time.Time
	stag string
}

func newCredHarness(t *testing.T) *credHarness {
	t.Helper()
	h := &credHarness{srv: newFakeCredServer(t, "agent-xyz"), now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	root := t.TempDir()
	h.f = agentFlags{server: h.srv.srv.URL, agentID: "agent-xyz", uuid: "UUID-1", workDir: filepath.Join(root, "work")}
	h.stag = filepath.Join(root, "staged")
	_ = os.MkdirAll(h.stag, 0o755)
	savedNow, savedStaged := credentialNow, stagedFile
	credentialNow = func() time.Time { return h.now }
	stagedFile = func(name string) string { return filepath.Join(h.stag, name) }
	t.Cleanup(func() { credentialNow, stagedFile = savedNow, savedStaged })
	h.start()
	return h
}

// start (re)starts the agent process: a fresh manager loaded from disk.
func (h *credHarness) start() {
	h.m = newCredManager(quietLog(), h.f)
	h.c = httpc.New(h.f.server, h.f.uuid, false)
	h.c.Credentials = h.m.Credentials
}

// poll mimics one check-in of runSelfOnce's credential handling.
func (h *credHarness) poll(t *testing.T) {
	t.Helper()
	var resp selfResponse
	err := h.c.GetJSON(context.Background(), "/api/v1/agent/self?id="+h.f.agentID, &resp)
	if httpc.IsUnauthorized(err) && h.m.Unauthorized(context.Background(), h.c) {
		err = h.c.GetJSON(context.Background(), "/api/v1/agent/self?id="+h.f.agentID, &resp)
	}
	if err != nil {
		return
	}
	if resp.CredentialStatus == "legacy" {
		h.m.Obtain(context.Background(), h.c)
	} else {
		h.m.Rotate(context.Background(), h.c, time.Duration(resp.CredentialRotateSeconds)*time.Second)
	}
}

func TestCredentialUpgradeAndRotation(t *testing.T) {
	h := newCredHarness(t)
	// Legacy agent: first check-in upgrades it.
	h.poll(t)
	if !h.m.HasSecret() || h.srv.legacy {
		t.Fatal("legacy agent didn't obtain a secret")
	}
	if fi, err := os.Stat(credentialFile(h.f)); err != nil {
		t.Fatalf("secret not stored: %v", err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("credential file mode %v", fi.Mode().Perm())
	}
	h.poll(t)
	if h.srv.unauthorized != 0 {
		t.Fatal("secured agent was refused")
	}

	// Not due: no rotation.
	h.now = h.now.Add(30 * time.Minute)
	h.poll(t)
	if h.srv.rotations != 0 {
		t.Fatal("rotated early")
	}
	// Due: rotates, and the new secret is used from then on.
	old := h.srv.cur
	h.now = h.now.Add(time.Hour)
	h.poll(t)
	h.poll(t)
	if h.srv.rotations != 1 || h.srv.cur == old || h.srv.prev != "" || h.srv.unauthorized != 0 {
		t.Fatalf("rotation: rotations=%d prev=%q unauthorized=%d", h.srv.rotations, h.srv.prev, h.srv.unauthorized)
	}
	stored, _ := loadCredential(h.f)
	if stored.Secret != h.srv.cur || stored.Pending != "" {
		t.Fatal("stored secret out of step with the server")
	}
}

func TestCredentialRotationNeverDesyncs(t *testing.T) {
	h := newCredHarness(t)
	h.poll(t) // obtain

	// 1. The server applies the rotation but the response is lost (twice).
	h.srv.dropRotateResponse = 2
	h.now = h.now.Add(2 * time.Hour)
	h.poll(t)
	h.poll(t)
	if h.srv.unauthorized != 0 {
		t.Fatal("agent refused while its rotation response was lost")
	}
	h.poll(t) // retry finally answered: commit
	h.poll(t)
	stored, _ := loadCredential(h.f)
	if stored.Secret != h.srv.cur || stored.Pending != "" || h.srv.unauthorized != 0 {
		t.Fatalf("after lost responses: stored=%v server cur matches=%v unauthorized=%d",
			stored.Pending, stored.Secret == h.srv.cur, h.srv.unauthorized)
	}

	// 2. The agent crashes after the server switched but before committing.
	h.srv.dropRotateResponse = 1
	h.now = h.now.Add(2 * time.Hour)
	h.poll(t) // server switched; agent saw an error and kept pending
	h.start() // restart from disk
	h.poll(t) // old secret still valid as "previous"; finishes the rotation
	h.poll(t)
	stored, _ = loadCredential(h.f)
	if stored.Secret != h.srv.cur || stored.Pending != "" || h.srv.unauthorized != 0 {
		t.Fatalf("after crash: unauthorized=%d in step=%v", h.srv.unauthorized, stored.Secret == h.srv.cur)
	}

	// 3. The server dropped the previous secret (the new one was used by a
	// copy of the agent) while this agent still holds it with the pending
	// one: a 401 makes it switch to the pending secret.
	h.srv.dropRotateResponse = 1
	h.now = h.now.Add(2 * time.Hour)
	h.poll(t)
	h.srv.mu.Lock()
	h.srv.prev = ""
	h.srv.mu.Unlock()
	before := h.srv.unauthorized
	h.poll(t)
	stored, _ = loadCredential(h.f)
	if stored.Secret != h.srv.cur || stored.Pending != "" {
		t.Fatal("agent didn't recover onto the pending secret")
	}
	h.poll(t)
	if h.srv.unauthorized != before+1 {
		t.Fatalf("expected exactly one refused request while recovering, got %d", h.srv.unauthorized-before)
	}
}

func TestCredentialStagedImportAndMedia(t *testing.T) {
	h := newCredHarness(t)
	// Boot client staged a secret: imported on start, file removed.
	h.srv.legacy = false
	h.srv.cur = "staged-secret-0123456789012345678901234567890123"
	_ = os.WriteFile(filepath.Join(h.stag, "agent-credential.json"),
		[]byte(`{"agent_id":"agent-xyz","secret":"staged-secret-0123456789012345678901234567890123"}`), 0o600)
	h.start()
	if _, err := os.Stat(filepath.Join(h.stag, "agent-credential.json")); !os.IsNotExist(err) {
		t.Fatal("staged credential file not removed")
	}
	h.poll(t)
	if h.srv.unauthorized != 0 {
		t.Fatal("imported secret refused")
	}

	// A staged file for another agent is ignored (and removed).
	h2 := newCredHarness(t)
	_ = os.WriteFile(filepath.Join(h2.stag, "agent-credential.json"), []byte(`{"agent_id":"someone-else","secret":"x"}`), 0o600)
	h2.start()
	if h2.m.HasSecret() {
		t.Fatal("imported another agent's secret")
	}

	// Exported media: the token is presented and the file removed on issue.
	h3 := newCredHarness(t)
	h3.srv.legacy = false // machine already has a secret elsewhere: only the token helps
	_ = os.WriteFile(filepath.Join(h3.stag, "enroll.json"), []byte(`{"image_id":7,"enroll_token":"media-token"}`), 0o600)
	if !h3.m.Obtain(context.Background(), h3.c) {
		t.Fatal("media token not used")
	}
	if _, err := os.Stat(filepath.Join(h3.stag, "enroll.json")); !os.IsNotExist(err) {
		t.Fatal("enroll file not removed")
	}
}

func TestCredentialApprovalAndRevocation(t *testing.T) {
	h := newCredHarness(t)
	h.srv.legacy = false // a secret exists server-side the agent doesn't have
	h.srv.cur = "server-side-secret-012345678901234567890123456789"
	h.poll(t) // 401 -> request -> pending
	if h.m.HasSecret() || len(h.srv.approval) != 1 {
		t.Fatalf("expected one pending request, got %v", h.srv.approval)
	}
	var nonce string
	for n := range h.srv.approval {
		nonce = n
	}
	// Restart: the same request (nonce) is kept, so approval reaches us.
	h.start()
	h.poll(t)
	if len(h.srv.approval) != 1 {
		t.Fatal("restart created a second request")
	}
	h.srv.approval[nonce] = "approved"
	h.poll(t)
	if !h.m.HasSecret() {
		t.Fatal("approved request not redeemed")
	}
	before := h.srv.unauthorized
	h.poll(t)
	if h.srv.unauthorized != before {
		t.Fatal("approved secret refused")
	}

	// Revoked by an operator: the agent drops it and asks again (pending).
	h.srv.cur = "revoked-and-reissued-elsewhere-0123456789012345678"
	h.poll(t)
	if h.m.HasSecret() {
		t.Fatal("agent kept a revoked secret")
	}
	if len(h.srv.approval) != 2 {
		t.Fatalf("no new request after revocation: %v", h.srv.approval)
	}
}

func TestCredentialOldServer(t *testing.T) {
	h := newCredHarness(t)
	h.srv.noEndpoint = true
	if h.m.Obtain(context.Background(), h.c) {
		t.Fatal("obtained a secret from a server without the endpoint")
	}
	n := len(h.srv.requests)
	h.m.Obtain(context.Background(), h.c)
	if len(h.srv.requests) != n {
		t.Fatal("kept asking a server that doesn't support secrets")
	}
	if id, sec := h.m.Credentials(); id != "agent-xyz" || sec != "" {
		t.Fatalf("credentials = %q %q", id, sec)
	}
}
