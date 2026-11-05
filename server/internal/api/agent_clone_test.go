package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/agentauth"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

func TestAgentCloneRefusedAndWhoami(t *testing.T) {
	ctx := context.Background()
	f := newCredFixture(t, true)
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "4c4c4544-0042-3010-8051-b4c04f4e3132"})
	secret, _ := f.creds.Issue(ctx, m.ID, "pxe", false)
	hdr := secretHdr(m, secret)

	// whoami: a yes/no about the caller's own hardware, no secret needed.
	if code, body := f.call(t, "GET", "/api/v1/agent/whoami?id="+m.AgentID, "",
		map[string]string{agentauth.HeaderUUID: "44454C4C-4200-1030-8051-B4C04F4E3132"}); code != 200 || !strings.Contains(body, `"same_hardware":true`) {
		t.Fatalf("whoami genuine: %d %s", code, body)
	}
	if code, body := f.call(t, "GET", "/api/v1/agent/whoami?id="+m.AgentID, "",
		map[string]string{agentauth.HeaderUUID: "11111111-2222-3333-4444-555555555555"}); code != 200 || !strings.Contains(body, `"same_hardware":false`) || strings.Contains(body, m.SystemUUID) {
		t.Fatalf("whoami clone: %d %s", code, body)
	}
	if code := mustCode(f.call(t, "GET", "/api/v1/agent/whoami?id=nope", "", map[string]string{agentauth.HeaderUUID: "x"})); code != 404 {
		t.Fatalf("whoami unknown: %d", code)
	}
	// The real machine (any UUID spelling) is accepted on the channel.
	hdr[agentauth.HeaderUUID] = "44454C4C-4200-1030-8051-B4C04F4E3132"
	if code := mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", hdr)); code != 200 {
		t.Fatalf("genuine machine refused: %d", code)
	}
	// A clone: same id + secret, different hardware -> 409, secret not used.
	hdr[agentauth.HeaderUUID] = "11111111-2222-3333-4444-555555555555"
	if code, body := f.call(t, "GET", "/api/v1/agent/self?id="+m.AgentID, "", hdr); code != 409 || !strings.Contains(body, "cloned") {
		t.Fatalf("clone: %d %s", code, body)
	}
	// Legacy machines are not refused as clones (old agents behave as before).
	l, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "LEGACY-1"})
	if code := mustCode(f.call(t, "GET", "/api/v1/agent/self?id="+l.AgentID, "", map[string]string{agentauth.HeaderUUID: "OTHER"})); code != 200 {
		t.Fatalf("legacy machine with other UUID header: %d", code)
	}
}

func TestAgentAutoEnrollNewMachines(t *testing.T) {
	ctx := context.Background()
	// Legacy auth OFF, auto-enrol of new machines ON (the default).
	f2 := newCredFixtureOpts(t, false, true)
	fresh, _ := f2.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "FRESH-CLONE"})
	code, body := f2.call(t, "POST", "/api/v1/agent/credential", credBody(fresh.AgentID, nonceA), nil)
	var r AgentCredentialResponse
	_ = json.Unmarshal([]byte(body), &r)
	if code != 200 || r.Status != "issued" {
		t.Fatalf("new machine with legacy off: %d %s", code, body)
	}
	// An existing machine (first seen long ago) still needs approval.
	old, _ := f2.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "OLD-MACHINE"})
	f2.backdate(t, old.ID, 48*time.Hour)
	if code, _ := f2.call(t, "POST", "/api/v1/agent/credential", credBody(old.AgentID, nonceB), nil); code != 202 {
		t.Fatalf("existing legacy machine with legacy off: %d, want 202", code)
	}
}

func TestAgentSoftwareBindingNeedsMedia(t *testing.T) {
	ctx := context.Background()
	f := newCredFixtureOpts(t, true, true)
	imgA, _ := f.images.Create(ctx, model.Image{Name: "A"})
	imgB, _ := f.images.Create(ctx, model.Image{Name: "B"})
	body := func(id model.ID) string { return `{"image_id":` + itoa64(int64(id)) + `}` }

	// Secured by first contact (no media): can't bind itself to an image.
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "BIND-1"})
	s, _ := f.creds.Issue(ctx, m.ID, "first-contact", false)
	if code, _ := f.call(t, "POST", "/api/v1/agent/software", body(imgA.ID), secretHdr(m, s)); code != 403 {
		t.Fatalf("non-media self-bind: %d", code)
	}
	// Secured via image A's media: binds to A only.
	u, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "BIND-2"})
	tok, _ := f.creds.EnsureImageEnrollToken(ctx, imgA.ID)
	code, out := f.call(t, "POST", "/api/v1/agent/credential",
		`{"system_uuid":"BIND-2","nonce":"`+nonceA+`","image_id":`+itoa64(int64(imgA.ID))+`,"enroll_token":"`+tok+`"}`, nil)
	var r AgentCredentialResponse
	_ = json.Unmarshal([]byte(out), &r)
	if code != 200 {
		t.Fatalf("media issue: %d %s", code, out)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/software", body(imgB.ID), secretHdr(u, r.Secret)); code != 403 {
		t.Fatalf("media machine bound to another image: %d", code)
	}
	if code, _ := f.call(t, "POST", "/api/v1/agent/software", body(imgA.ID), secretHdr(u, r.Secret)); code != 200 {
		t.Fatalf("media machine bind to its image: %d", code)
	}
}
