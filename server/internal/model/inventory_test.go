package model

import (
	"context"
	"errors"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestDeployPhaseProgress(t *testing.T) {
	cases := []struct {
		phase, outcome string
		wantLabel      string
		wantPct        int
		wantIndet      bool
	}{
		{"staging", "in_progress", "Staging media", 10, false},
		{"staged", "in_progress", "Rebooting into Setup", 30, false},
		{"specialize", "in_progress", "Windows Setup (specialize)", 55, false},
		{"first-logon", "in_progress", "First logon (OOBE)", 65, false},
		{"agent-online", "in_progress", "Windows installed — agent running", 70, false},
		{"installing", "in_progress", "Installing software", 75, false}, // no counts -> band low end
		{"complete", "in_progress", "Finishing up", 95, false},
		{"first-logon", "ok", "Done", 100, false},   // outcome wins over phase
		{"staging", "failed", "Failed", 100, false}, // outcome wins over phase
		{"", "in_progress", "In progress", 0, true}, // legacy row -> indeterminate
		{"bogus", "in_progress", "In progress", 0, true},
	}
	for _, c := range cases {
		label, pct, indet := DeployPhaseProgress(c.phase, c.outcome)
		if label != c.wantLabel || pct != c.wantPct || indet != c.wantIndet {
			t.Errorf("DeployPhaseProgress(%q,%q) = (%q,%d,%v), want (%q,%d,%v)",
				c.phase, c.outcome, label, pct, indet, c.wantLabel, c.wantPct, c.wantIndet)
		}
	}
}

func TestDeployRecordProgressInstallingOverlay(t *testing.T) {
	cases := []struct {
		done, total int
		wantLabel   string
		wantPct     int
	}{
		{0, 5, "Installing software (0/5)", 75}, // band low
		{1, 5, "Installing software (1/5)", 78}, // 75 + 17*1/5 = 78.4 -> 78
		{5, 5, "Installing software (5/5)", 92}, // band high
		{9, 5, "Installing software (5/5)", 92}, // clamp overshoot
		{2, 4, "Installing software (2/4)", 83}, // 75 + 17/2 = 83.5 -> 83
	}
	for _, c := range cases {
		d := DeploymentRecord{Outcome: "in_progress", Phase: "installing",
			ProgressDone: c.done, ProgressTotal: c.total}
		label, pct, indet := DeployRecordProgress(d)
		if label != c.wantLabel || pct != c.wantPct || indet {
			t.Errorf("DeployRecordProgress(done=%d,total=%d) = (%q,%d,%v), want (%q,%d,false)",
				c.done, c.total, label, pct, indet, c.wantLabel, c.wantPct)
		}
	}
	// total==0 -> no overlay, falls back to the plain "installing" base.
	base := DeploymentRecord{Outcome: "in_progress", Phase: "installing"}
	if label, pct, _ := DeployRecordProgress(base); label != "Installing software" || pct != 75 {
		t.Errorf("installing with no total = (%q,%d), want (Installing software,75)", label, pct)
	}
	// A non-installing phase is unchanged by the overlay.
	spec := DeploymentRecord{Outcome: "in_progress", Phase: "specialize", ProgressTotal: 5}
	if label, pct, _ := DeployRecordProgress(spec); label != "Windows Setup (specialize)" || pct != 55 {
		t.Errorf("specialize overlay leaked: (%q,%d)", label, pct)
	}
}

func TestUpsertFromIdentityCreatesThenUpdates(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))

	id := match.Identity{
		SystemUUID:         "uuid-aaaa",
		SystemSerial:       "SN-1",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
	}
	a, err := repo.UpsertFromIdentity(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.SystemUUID != "uuid-aaaa" {
		t.Errorf("create wrong: %+v", a)
	}
	// A server-minted agent_id is assigned at creation and is a distinct,
	// non-empty value (NOT the BIOS UUID).
	if a.AgentID == "" || a.AgentID == a.SystemUUID {
		t.Errorf("agent_id not minted distinctly: %+v", a)
	}
	// Second upsert with same UUID returns same record AND keeps the same
	// agent_id (never re-minted).
	b, err := repo.UpsertFromIdentity(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != a.ID {
		t.Errorf("expected same id on second upsert, got %d -> %d", a.ID, b.ID)
	}
	if b.AgentID != a.AgentID {
		t.Errorf("agent_id changed across upserts: %q -> %q", a.AgentID, b.AgentID)
	}
	// GetByAgentID resolves the same machine; empty never matches.
	got, err := repo.GetByAgentID(ctx, a.AgentID)
	if err != nil || got.ID != a.ID {
		t.Errorf("GetByAgentID(%q) = %+v, %v", a.AgentID, got, err)
	}
	if _, err := repo.GetByAgentID(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty agent_id should not match, got %v", err)
	}
	if _, err := repo.UpsertFromIdentity(ctx, match.Identity{}); !errors.Is(err, ErrValidation) {
		t.Errorf("expected validation error for empty UUID, got %v", err)
	}
}

func TestBindingAndHistory(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	img := NewImageRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	i, _ := img.Create(ctx, Image{Name: "base"})
	imgID := i.ID
	if err := inv.UpsertBinding(ctx, MachineBinding{
		MachineID: m.ID, ImageID: &imgID, MachineName: "LAB-01",
		TargetOU:         "OU=Lab,DC=corp,DC=example",
		GroupMemberships: []string{"Lab-Computers", "All-Workstations"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := inv.GetBinding(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.MachineName != "LAB-01" || len(b.GroupMemberships) != 2 || b.ImageID == nil || *b.ImageID != imgID {
		t.Errorf("binding wrong: %+v", b)
	}

	// History.
	depID, err := inv.RecordDeployment(ctx, m.ID, &imgID)
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.CompleteDeployment(ctx, depID, "ok", "smoke test"); err != nil {
		t.Fatal(err)
	}
	hist, err := inv.HistoryFor(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 1 || hist[0].Outcome != "ok" || hist[0].CompletedAt == nil {
		t.Errorf("history wrong: %+v", hist)
	}
}

func TestDetectedStateUpsert(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	sw := NewSoftwarePackageRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	pkg, _ := sw.Create(ctx, SoftwarePackage{Name: "acme"})

	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: m.ID, SoftwarePackageID: pkg.ID, Detected: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := inv.RecordDetectedState(ctx, DetectedState{
		MachineID: m.ID, SoftwarePackageID: pkg.ID, Detected: false,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := inv.DetectedStateFor(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Detected {
		t.Errorf("expected single not-detected row, got %+v", got)
	}
}

// TestUpsertFromIdentityPreservesMakeModel guards the inventory make/model
// display: the Boot Client reads SMBIOS make/model pre-boot, but the resident
// agent (and other callers) later report an identity carrying only the UUID.
// A UUID-only upsert must NOT wipe the previously captured SMBIOS fields.
func TestUpsertFromIdentityPreservesMakeModel(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))

	// Boot Client captures full SMBIOS identity pre-boot.
	full := match.Identity{
		SystemUUID:         "mk-uuid",
		SystemSerial:       "SN123",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5520",
		BIOSVendor:         "Dell",
		BoardManufacturer:  "Dell",
		BoardProduct:       "0ABCD",
	}
	if _, err := repo.UpsertFromIdentity(ctx, full); err != nil {
		t.Fatal(err)
	}

	// Resident agent checks in with only the UUID populated.
	m, err := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "mk-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if m.SystemManufacturer != "Dell Inc." || m.SystemProduct != "Latitude 5520" {
		t.Errorf("UUID-only upsert wiped make/model: got %q / %q",
			m.SystemManufacturer, m.SystemProduct)
	}
	if m.SystemSerial != "SN123" || m.BoardProduct != "0ABCD" {
		t.Errorf("UUID-only upsert wiped serial/board: got %q / %q",
			m.SystemSerial, m.BoardProduct)
	}

	// A real SMBIOS report with changed (non-empty) values still updates.
	m, err = repo.UpsertFromIdentity(ctx, match.Identity{
		SystemUUID:         "mk-uuid",
		SystemManufacturer: "Dell Inc.",
		SystemProduct:      "Latitude 5530",
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.SystemProduct != "Latitude 5530" {
		t.Errorf("non-empty update did not apply: got %q", m.SystemProduct)
	}
}

func TestUpdateLatestInProgressNote(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)

	m, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "milestone-uuid"})
	depID, err := inv.RecordDeployment(ctx, m.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// While in_progress: the note + phase update, completed_at stays NULL,
	// outcome stays in_progress.
	if err := inv.UpdateLatestInProgressNote(ctx, m.ID, "reached specialize", "specialize"); err != nil {
		t.Fatal(err)
	}
	hist, _ := inv.HistoryFor(ctx, m.ID)
	if len(hist) != 1 {
		t.Fatalf("want 1 history row, got %d", len(hist))
	}
	if hist[0].Outcome != "in_progress" || hist[0].CompletedAt != nil || hist[0].Notes != "reached specialize" || hist[0].Phase != "specialize" {
		t.Errorf("after milestone: %+v", hist[0])
	}

	// Agent closes the row ok. A late milestone must NOT resurrect or alter
	// it (race guard) — neither notes nor phase.
	if err := inv.CompleteDeployment(ctx, depID, "ok", "done"); err != nil {
		t.Fatal(err)
	}
	if err := inv.UpdateLatestInProgressNote(ctx, m.ID, "late specialize callback", "first-logon"); err != nil {
		t.Fatal(err)
	}
	hist, _ = inv.HistoryFor(ctx, m.ID)
	if hist[0].Outcome != "ok" || hist[0].Notes != "done" || hist[0].CompletedAt == nil || hist[0].Phase != "complete" {
		t.Errorf("late milestone must not mutate a closed row: %+v", hist[0])
	}
}

// ListOSCaptions returns every machine's reported OS in one query — the
// machines list renders (and searches/sorts) the OS column from it instead
// of paying a per-row Get.
func TestListOSCaptions(t *testing.T) {
	ctx := context.Background()
	repo := NewInventoryRepo(openTestDB(t))

	a, _ := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "os-a"})
	b, _ := repo.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "os-b"})
	if err := repo.UpdateHardware(ctx, a.ID, Hardware{OSCaption: "Microsoft Windows 11 Pro"}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListOSCaptions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[a.ID] != "Microsoft Windows 11 Pro" {
		t.Errorf("caption for a: %q", got[a.ID])
	}
	if _, ok := got[b.ID]; ok {
		t.Errorf("machine with no hardware report should be absent, got %q", got[b.ID])
	}
}
