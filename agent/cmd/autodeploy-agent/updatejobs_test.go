package main

import (
	"encoding/json"
	"testing"

	"github.com/rusketh/autodeploy/agent/internal/updates"
)

func TestUpdatePayloadName(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"windows10.0-kb5034441-x64.msu", "windows10.0-kb5034441-x64.msu"},
		{"kb5033372.cab", "kb5033372.cab"},
		// Old servers send no filename: legacy fallback keeps working.
		{"", "update-7.msu"},
		// Path components are stripped, never trusted.
		{`..\..\evil.msu`, "evil.msu"},
		{"a/b/c.msu", "c.msu"},
		{"..", "update-7.msu"},
		{"  ", "update-7.msu"},
	}
	for _, tt := range tests {
		j := updateJob{UpdateID: 7, PayloadFilename: tt.filename}
		if got := updatePayloadName(j); got != tt.want {
			t.Errorf("updatePayloadName(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

// A legacy server response (no metadata fields) must still decode and
// leave the new fields zero-valued.
func TestUpdateJobDecodesLegacyServerJSON(t *testing.T) {
	legacy := `{"id":3,"deployment_id":1,"machine_id":2,"update_id":7,"status":"running"}`
	var j updateJob
	if err := json.Unmarshal([]byte(legacy), &j); err != nil {
		t.Fatal(err)
	}
	if j.ID != 3 || j.UpdateID != 7 || j.Status != "running" {
		t.Errorf("legacy decode mismatch: %+v", j)
	}
	if j.KBNumber != "" || j.PayloadFilename != "" || j.RebootAfter || j.SizeBytes != 0 {
		t.Errorf("new fields should be zero for legacy JSON: %+v", j)
	}
}

func TestDecideJobOutcome(t *testing.T) {
	tests := []struct {
		name        string
		outcome     updates.Outcome
		rebootAfter bool
		wantStatus  string
		wantReboot  bool
	}{
		{"clean install", updates.ClassifyExitCode(0), true, "ok", false},
		{"reboot required, operator opted in", updates.ClassifyExitCode(3010), true, "ok", true},
		{"reboot required, no opt-in", updates.ClassifyExitCode(3010), false, "ok", false},
		{"already installed", updates.ClassifyExitCode(0x00240006), true, "ok", false},
		{"not applicable", updates.ClassifyExitCode(int(uint32(0x80240017))), true, "failed", false},
	}
	for _, tt := range tests {
		status, reboot := decideJobOutcome(tt.outcome, tt.rebootAfter)
		if status != tt.wantStatus || reboot != tt.wantReboot {
			t.Errorf("%s: decideJobOutcome = (%q, %v), want (%q, %v)",
				tt.name, status, reboot, tt.wantStatus, tt.wantReboot)
		}
	}
}
