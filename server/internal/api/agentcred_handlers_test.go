package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/agentauth"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

type credFixture struct {
	srv    *httptest.Server
	inv    *model.InventoryRepo
	creds  *model.AgentCredentialRepo
	images *model.ImageRepo
	dj     *model.DomainJoinRepo
	auth   *agentauth.Authenticator
}

func newCredFixture(t *testing.T, allowLegacy bool) credFixture {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	bx, _ := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	inv := model.NewInventoryRepo(db)
	f := credFixture{inv: inv, creds: model.NewAgentCredentialRepo(db), images: model.NewImageRepo(db),
		dj: model.NewDomainJoinRepo(db, bx)}
	f.auth = &agentauth.Authenticator{Inventory: inv, Creds: f.creds, AllowLegacy: allowLegacy}
	mux := http.NewServeMux()
	Register(mux, Repos{
		Inventory: inv, Images: f.images, Bulk: model.NewBulkRepo(db, inv), DomainJoin: f.dj,
		AgentAuth: f.auth, Credentials: f.creds, AgentSecretRotation: 720 * time.Hour,
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// call sends a request; hdr may carry agent id/secret/UUID headers.
func (f credFixture) call(t *testing.T, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func secretHdr(m model.MachineRecord, secret string) map[string]string {
	return map[string]string{agentauth.HeaderAgentID: m.AgentID, agentauth.HeaderSecret: secret}
}

const nonceA = "nonce-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const nonceB = "nonce-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func credBody(agentID, nonce string) string {
	return `{"agent_id":"` + agentID + `","nonce":"` + nonce + `"}`
}

// TestAgentUpgradePathAndSecuredChannel walks one machine from the legacy
// protocol to a secret, then checks every gate on the secured channel.
func TestAgentUpgradePathAndSecuredChannel(t *testing.T) {
	ctx := context.Background()
	f := newCredFixture(t, true)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "UPG-1"})

	// 1. Legacy: the old protocol works, and /self says "get a secret".
	code, body := f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", nil)
	if code != 200 || !strings.Contains(body, `"credential_status":"legacy"`) {
		t.Fatalf("legacy /self: %d %s", code, body)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/checkin", `{"identity":{"system_uuid":"UPG-1"}}`, nil); code != 200 {
		t.Fatalf("legacy checkin: %d", code)
	}

	// 2. Upgrade: first contact issues the secret.
	code, body = f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceA), nil)
	var issued AgentCredentialResponse
	_ = json.Unmarshal([]byte(body), &issued)
	if code != 200 || issued.Status != "issued" || len(issued.Secret) < model.MinSecretLength || issued.RotateSeconds != 720*3600 {
		t.Fatalf("first contact: %d %s", code, body)
	}
	s1 := issued.Secret

	// 3. From now on the old protocol is refused for this machine...
	for name, c := range map[string]int{
		"self by id":            mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", nil)),
		"checkin by uuid":       mustCode(f.call(t, "POST", "/api/v1/agent/checkin", `{"identity":{"system_uuid":"UPG-1"}}`, nil)),
		"domain join":           mustCode(f.call(t, "POST", "/api/v1/agent/domain-join", `{"agent_id":"`+m.AgentID+`"}`, nil)),
		"wrong secret":          mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, "not-the-secret"))),
		"hardware report":       mustCode(f.call(t, "POST", "/api/v1/agent/hardware", `{"agent_id":"`+m.AgentID+`"}`, nil)),
		"another first contact": mustCode(f.call(t, "POST", "/api/v1/agent/credential/rotate", `{"new_secret":"x"}`, map[string]string{agentauth.HeaderAgentID: m.AgentID})),
	} {
		if c != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, c)
		}
	}
	// ...and the secret opens the channel.
	code, body = f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, s1))
	if code != 200 || !strings.Contains(body, `"credential_status":"secured"`) {
		t.Fatalf("secured /self: %d %s", code, body)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/checkin", `{"identity":{"system_uuid":"UPG-1"}}`, secretHdr(m, s1)); code != 200 {
		t.Fatalf("secured checkin: %d", code)
	}

	// 4. Knowing the agent_id no longer gets a secret: a second requester
	// waits for an operator, who is notified.
	code, body = f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceB), nil)
	if code != http.StatusAccepted || !strings.Contains(body, "pending") {
		t.Fatalf("takeover attempt: %d %s", code, body)
	}
	if n, _ := f.creds.PendingRequestCount(ctx); n != 1 {
		t.Fatalf("pending = %d", n)
	}

	// 5. Rotation over the API: idempotent, old secret valid until the new
	// one is used.
	s2, _ := model.NewAgentSecret()
	rot := `{"new_secret":"` + s2 + `"}`
	if code, b := f.call(t, "POST", "/api/v1/agent/credential/rotate", rot, secretHdr(m, s1)); code != 200 {
		t.Fatalf("rotate: %d %s", code, b)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential/rotate", rot, secretHdr(m, s1)); code != 200 {
		t.Fatalf("retried rotate (lost response): %d", code)
	}
	if mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, s1))) != 200 {
		t.Fatal("old secret refused before the new one was used")
	}
	if mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, s2))) != 200 {
		t.Fatal("new secret refused")
	}
	if mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, s1))) != 401 {
		t.Fatal("old secret still accepted after the new one was used")
	}

	// 6. Approval: the operator approves the pending request; that requester
	// (and only it) gets a fresh secret, which replaces the old one.
	reqs, _ := f.creds.Requests(ctx, m.ID, 5)
	if _, err := f.creds.Decide(ctx, reqs[0].ID, true, "admin"); err != nil {
		t.Fatal(err)
	}
	code, body = f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceB), nil)
	_ = json.Unmarshal([]byte(body), &issued)
	if code != 200 || issued.Status != "issued" {
		t.Fatalf("approved request: %d %s", code, body)
	}
	if mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", secretHdr(m, s2))) != 401 {
		t.Fatal("secret from before the approval still works")
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceB), nil); code != http.StatusForbidden {
		t.Fatalf("approval redeemed twice: %d", code)
	}
}

func mustCode(code int, _ string) int { return code }

func TestAgentCredentialGrantsAndLegacyOff(t *testing.T) {
	ctx := context.Background()
	f := newCredFixture(t, false)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "OFF-1"})

	// Legacy auth off: the old protocol is refused, and first contact is
	// not trusted.
	if code := mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", nil)); code != 401 {
		t.Fatalf("legacy with legacy auth off: %d", code)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceA), nil); code != http.StatusAccepted {
		t.Fatalf("first contact with legacy off should need approval: %d", code)
	}

	// A deploy grant (left by an authorised network deploy for an older
	// boot client) is redeemed once by the new OS.
	_ = f.creds.GrantDeploy(ctx, m.ID, time.Hour)
	code, body := f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, nonceB), nil)
	if code != 200 || !strings.Contains(body, "issued") {
		t.Fatalf("deploy grant: %d %s", code, body)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", credBody(m.AgentID, "nonce-cccccccccccccccccccccccccccccccccc"), nil); code != http.StatusAccepted {
		t.Fatalf("grant redeemed twice: %d", code)
	}

	// Exported media: its token issues a secret even for a secured machine
	// (the media is the deploy authorisation), but only for its image.
	img, _ := f.images.Create(ctx, model.Image{Name: "usb"})
	other, _ := f.images.Create(ctx, model.Image{Name: "other"})
	tok, _ := f.creds.NewImageEnrollToken(ctx, img.ID, time.Hour)
	bad := `{"system_uuid":"OFF-1","nonce":"` + nonceA + `x","image_id":` + itoa64(int64(other.ID)) + `,"enroll_token":"` + tok + `"}`
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", bad, nil); code != http.StatusAccepted {
		t.Fatalf("token for another image: %d", code)
	}
	good := `{"system_uuid":"OFF-1","nonce":"` + nonceA + `y","image_id":` + itoa64(int64(img.ID)) + `,"enroll_token":"` + tok + `"}`
	if code, body := f.call(t, "POST", "/api/v1/agent/credential", good, nil); code != 200 || !strings.Contains(body, "issued") {
		t.Fatalf("media token: %d %s", code, body)
	}

	// Revoked: everything refused until approval.
	_ = f.creds.Revoke(ctx, m.ID)
	if c, _ := f.creds.Get(ctx, m.ID); c.Status != model.CredApproval {
		t.Fatalf("status after revoke: %s", c.Status)
	}

	// Bad requests.
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", `{"agent_id":"`+m.AgentID+`","nonce":"short"}`, nil); code != 400 {
		t.Fatalf("short nonce: %d", code)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/credential", credBody("no-such-agent", nonceA), nil); code != 404 {
		t.Fatalf("unknown machine: %d", code)
	}
}
