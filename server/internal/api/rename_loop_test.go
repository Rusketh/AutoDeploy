package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// renameSim plays the resident agent's poll against the real server code, in
// the agent's order: /self hands back the desired name, the agent reports its
// observed identity, then renames (a reboot) when the desired name differs
// from its live hostname.
type renameSim struct {
	t        *testing.T
	ctx      context.Context
	r        Repos
	agentID  string
	hostname string
	renames  int
}

func newRenameSim(t *testing.T, r Repos, ctx context.Context, uuid, hostname string) (*renameSim, model.MachineRecord) {
	t.Helper()
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: uuid, SystemSerial: "SER" + uuid})
	if err != nil {
		t.Fatal(err)
	}
	if m.AgentID == "" {
		t.Fatal("machine has no agent id")
	}
	s := &renameSim{t: t, ctx: ctx, r: r, agentID: m.AgentID, hostname: hostname}
	s.report() // the first poll's identity report
	return s, m
}

func (s *renameSim) report() {
	s.t.Helper()
	body := `{"agent_id":"` + s.agentID + `","computer_name":"` + s.hostname + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/identity", strings.NewReader(body))
	w := httptest.NewRecorder()
	handleAgentIdentity(s.r)(w, req)
	if w.Code != http.StatusOK {
		s.t.Fatalf("identity report: %d %s", w.Code, w.Body.String())
	}
}

// poll runs one agent check-in; a rename ends it with a reboot.
func (s *renameSim) poll() {
	s.t.Helper()
	m, err := s.r.Inventory.GetByAgentID(s.ctx, s.agentID)
	if err != nil {
		s.t.Fatal(err)
	}
	desired := desiredNameFor(s.ctx, s.r, m.ID) // /api/v1/agent/self
	s.report()                                  // reportObservedIdentity
	if validComputerName(desired) && !strings.EqualFold(desired, s.hostname) {
		s.hostname = desired // Rename-Computer -Restart
		s.renames++
	}
}

func (s *renameSim) settle(polls int) {
	for i := 0; i < polls; i++ {
		s.poll()
	}
}

func (s *renameSim) bindingName(id model.ID) string {
	b, err := s.r.Inventory.GetBinding(s.ctx, id)
	if err != nil {
		return ""
	}
	return b.MachineName
}

// A name set in the portal is applied once and stays: the pre-rename name the
// agent reports before its reboot must not overwrite it (that ping-ponged the
// machine between the two names, rebooting every minute).
func TestRenameFromPortalAppliesOnce(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	s, m := newRenameSim(t, r, ctx, "u-portal", "DESKTOP-ABC123")
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "BA-SL-SEN-01"}); err != nil {
		t.Fatal(err)
	}
	s.settle(8)
	if s.hostname != "BA-SL-SEN-01" || s.renames != 1 {
		t.Fatalf("hostname %q after %d renames, want BA-SL-SEN-01 after 1", s.hostname, s.renames)
	}
	if got := s.bindingName(m.ID); got != "BA-SL-SEN-01" {
		t.Errorf("binding name = %q, want the portal name kept", got)
	}

	// Renaming it again in the portal later also applies exactly once.
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "BA-SL-SEN-09"}); err != nil {
		t.Fatal(err)
	}
	s.settle(8)
	if s.hostname != "BA-SL-SEN-09" || s.renames != 2 {
		t.Fatalf("hostname %q after %d renames, want BA-SL-SEN-09 after 2", s.hostname, s.renames)
	}
}

// A rename made on the machine itself (Control Panel, Rename-Computer) is
// adopted into the binding and is not reverted.
func TestRenameOnMachineIsAdopted(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	s, m := newRenameSim(t, r, ctx, "u-local", "LAB-01")
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "LAB-01"}); err != nil {
		t.Fatal(err)
	}
	s.settle(3)
	if s.renames != 0 {
		t.Fatalf("in-sync machine was renamed %d times", s.renames)
	}

	s.hostname = "FRONT-DESK" // renamed locally, then rebooted
	s.settle(8)
	if s.hostname != "FRONT-DESK" || s.renames != 0 {
		t.Fatalf("hostname %q after %d agent renames, want FRONT-DESK kept", s.hostname, s.renames)
	}
	if got := s.bindingName(m.ID); got != "FRONT-DESK" {
		t.Errorf("binding name = %q, want the local rename adopted", got)
	}
}

// A machine with no binding name but an image name template is renamed to
// the template's name once and stays there.
func TestRenameFromImageTemplateAppliesOnce(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	ua, err := r.Unattend.Create(ctx, model.Unattend{Name: "t", SettingsJSON: `{"name_template":"BA-AUT-%serial(4)%"}`})
	if err != nil {
		t.Fatal(err)
	}
	img, err := r.Images.Create(ctx, model.Image{Name: "base", UnattendID: &ua.ID})
	if err != nil {
		t.Fatal(err)
	}
	s, m := newRenameSim(t, r, ctx, "u-tmpl", "DESKTOP-RANDOM")
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, ImageID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	s.settle(8)
	if s.renames != 1 || !strings.HasPrefix(s.hostname, "BA-AUT-") {
		t.Fatalf("hostname %q after %d renames, want BA-AUT-… after 1", s.hostname, s.renames)
	}
}

// The state a looping machine is in after upgrading: the binding was already
// overwritten with one of the two names. It settles within one reboot.
func TestRenameLoopSettlesAfterUpgrade(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	s, m := newRenameSim(t, r, ctx, "u-stuck", "BA-SL-SEN-01")
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "BA-AUT-0ZY7MKJ2"}); err != nil {
		t.Fatal(err)
	}
	s.settle(8)
	if s.renames > 1 {
		t.Fatalf("still looping: %d renames", s.renames)
	}
	if got := s.bindingName(m.ID); !strings.EqualFold(got, s.hostname) {
		t.Errorf("binding %q and hostname %q disagree after settling", got, s.hostname)
	}
}

func TestValidComputerNameMatchesAgent(t *testing.T) {
	for name, want := range map[string]bool{
		"LAB-01": true, "BA-SL-SEN-01": true, "": false, "123": false,
		"LAB_01": false, "WAY-TOO-LONG-NAME": false, "has space": false,
	} {
		if got := validComputerName(name); got != want {
			t.Errorf("validComputerName(%q) = %v, want %v", name, got, want)
		}
	}
}

// A portal Rename action queues a rename job and sets the binding name. The
// /self poll that delivers the job must not also ask for a DesiredName rename,
// or the agent reboots on that before running (and closing) the job.
func TestSelfLeavesRenameToTheRenameJob(t *testing.T) {
	r, ctx := newDesiredNameRepos(t)
	m, err := r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u-job"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "NEW-NAME"}); err != nil {
		t.Fatal(err)
	}
	self := func() AgentSelfResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/self?id="+m.AgentID, nil)
		w := httptest.NewRecorder()
		handleAgentSelf(r)(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("self: %d %s", w.Code, w.Body.String())
		}
		var resp AgentSelfResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if got := self().DesiredName; got != "NEW-NAME" {
		t.Fatalf("without a job: desired = %q, want NEW-NAME", got)
	}
	if _, _, err := r.Bulk.CreateOperation(ctx, model.BulkOperation{
		Action:  model.BulkActionRename,
		Payload: `{"new_name":"NEW-NAME"}`,
		Target:  model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	}); err != nil {
		t.Fatal(err)
	}
	resp := self()
	if len(resp.Jobs) != 1 || resp.DesiredName != "" {
		t.Fatalf("with a rename job: jobs=%d desired=%q, want 1 job and no desired name", len(resp.Jobs), resp.DesiredName)
	}
}
