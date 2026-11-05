package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSameUUIDAgent(t *testing.T) {
	if !sameUUID("4c4c4544-0042-3010-8051-b4c04f4e3132", "44454C4C-4200-1030-8051-B4C04F4E3132") {
		t.Fatal("byte-swapped form should match")
	}
	if sameUUID("4c4c4544-0042-3010-8051-b4c04f4e3132", "4c4c4544-0042-3010-8051-b4c04f4e3133") {
		t.Fatal("different hardware matched")
	}
}

// cloneServer answers whoami for the golden machine and enrolls new ones.
func cloneServer(t *testing.T, goldenID, goldenUUID string, whoamiCode int) (*httptest.Server, *[]string) {
	var enrolled []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/agent/whoami":
			if whoamiCode != 200 {
				http.Error(w, "cloned identity", whoamiCode)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"same_hardware": sameUUID(r.Header.Get("X-AutoDeploy-Machine-UUID"), goldenUUID)})
		case "/api/v1/agent/enroll":
			var in struct {
				Identity map[string]any `json:"identity"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			u, _ := in.Identity["system_uuid"].(string)
			enrolled = append(enrolled, u)
			_ = json.NewEncoder(w).Encode(map[string]any{"machine_id": 2, "agent_id": "agent-for-" + u})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &enrolled
}

func TestCloneDetectedFromCopiedCredential(t *testing.T) {
	srv, enrolled := cloneServer(t, "golden", "aaaaaaaa-0000-0000-0000-000000000001", 200)
	f := agentFlags{server: srv.URL, agentID: "golden", uuid: "bbbbbbbb-0000-0000-0000-000000000002",
		workDir: filepath.Join(t.TempDir(), "work")}
	// The golden image's credential file, copied onto this hardware.
	golden := f
	golden.uuid = "aaaaaaaa-0000-0000-0000-000000000001"
	if err := saveCredential(quietLog(), golden, agentCredential{AgentID: "golden", Secret: "s", SystemUUID: golden.uuid}); err != nil {
		t.Fatal(err)
	}
	if !isCloned(context.Background(), quietLog(), f) {
		t.Fatal("copied credential not detected as a clone")
	}
	saved := agentCreds
	t.Cleanup(func() { agentCreds = saved })
	reidentify(context.Background(), quietLog(), &f)
	if f.agentID != "agent-for-bbbbbbbb-0000-0000-0000-000000000002" || len(*enrolled) != 1 {
		t.Fatalf("not re-enrolled as itself: %q %v", f.agentID, *enrolled)
	}
	if _, err := os.Stat(credentialFile(f)); !os.IsNotExist(err) {
		t.Fatal("inherited secret not discarded")
	}
	if id, sec := agentCreds.Credentials(); id != f.agentID || sec != "" {
		t.Fatalf("credentials after re-enrol = %q %q", id, sec)
	}
}

func TestCloneDetectedFromWhoami(t *testing.T) {
	// No credential file (legacy golden image): the server's record says the
	// agent id belongs to other hardware.
	srv, _ := cloneServer(t, "golden", "aaaaaaaa-0000-0000-0000-000000000001", 200)
	f := agentFlags{server: srv.URL, agentID: "golden", uuid: "cccccccc-0000-0000-0000-000000000003", workDir: filepath.Join(t.TempDir(), "work")}
	if !isCloned(context.Background(), quietLog(), f) {
		t.Fatal("legacy clone not detected via whoami")
	}
	// The genuine machine (UUID in another spelling) is not a clone.
	f.uuid = "AAAAAAAA-0000-0000-0000-000000000001"
	if isCloned(context.Background(), quietLog(), f) {
		t.Fatal("genuine machine flagged as a clone")
	}
	// Server refusal (409 cloned) also counts; an unreachable/old server doesn't.
	srv409, _ := cloneServer(t, "golden", "x", 409)
	f.server = srv409.URL
	if !isCloned(context.Background(), quietLog(), f) {
		t.Fatal("409 not treated as a clone")
	}
	srv404, _ := cloneServer(t, "golden", "x", 404)
	f.server = srv404.URL
	if isCloned(context.Background(), quietLog(), f) {
		t.Fatal("old server (404) treated as a clone")
	}
	_ = strings.TrimSpace
}
