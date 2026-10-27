package model

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
)

func TestMachineDisplayNamePrecedence(t *testing.T) {
	uuid := "F45ABB24-34BF-4E68-86FA-6FB478A223AA"
	cases := []struct {
		name     string
		reported string
		desired  string
		want     MachineName
	}{
		{"desired wins", "PC-0001", "LAB-01", MachineName{Name: "LAB-01", RenamingFrom: "PC-0001"}},
		{"same name no hint", "LAB-01", "lab-01", MachineName{Name: "lab-01"}},
		{"desired only", "", "LAB-01", MachineName{Name: "LAB-01"}},
		{"reported only", "PC-0001", "", MachineName{Name: "PC-0001"}},
		{"template skipped", "PC-0001", "LAB-%SERIAL%", MachineName{Name: "PC-0001"}},
		{"short uuid", "", "", MachineName{Name: "F45ABB24…", Unnamed: true}},
	}
	for _, c := range cases {
		got := MachineDisplayName(MachineRecord{ID: 7, SystemUUID: uuid, ReportedName: c.reported},
			MachineBinding{MachineName: c.desired})
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := MachineDisplayName(MachineRecord{ID: 7}, MachineBinding{}); got.Name != "machine #7" || !got.Unnamed {
		t.Errorf("no uuid: got %+v", got)
	}
}

func TestParseLogTarget(t *testing.T) {
	cases := []struct {
		in   string
		kind string
		id   ID
		ref  string
		ok   bool
	}{
		{"machine:12", KindMachine, 12, "12", true},
		{"software/3", KindSoftware, 3, "3", true},
		{"software_package:3", KindSoftware, 3, "3", true},
		{"iso:77", KindISO, 77, "77", true},
		{"machine:F45ABB24-34BF", KindMachine, 0, "F45ABB24-34BF", true},
		{"machines:5", "", 0, "", false},
		{"group:Lab", "", 0, "", false},
		{"imported_asset:3", "", 0, "", false},
		{"plain text", "", 0, "", false},
		{"machine:", "", 0, "", false},
		{"", "", 0, "", false},
	}
	for _, c := range cases {
		k, id, ref, ok := ParseLogTarget(c.in)
		if k != c.kind || id != c.id || ref != c.ref || ok != c.ok {
			t.Errorf("ParseLogTarget(%q) = %q %d %q %v", c.in, k, id, ref, ok)
		}
	}
}

func TestNameLookupAndEnrichLogs(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	inv := NewInventoryRepo(db)
	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "UUID-1111-2222"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpsertBinding(ctx, MachineBinding{MachineID: m.ID, MachineName: "LAB-01"}); err != nil {
		t.Fatal(err)
	}
	sw, err := NewSoftwarePackageRepo(db).Create(ctx, SoftwarePackage{Name: "7-Zip"})
	if err != nil {
		t.Fatal(err)
	}

	l := NewNameLookup(ctx, inv)
	if n, ok := l.Name(KindSoftware, sw.ID); !ok || n != "7-Zip" {
		t.Errorf("software name = %q %v", n, ok)
	}
	if res := l.Lookup("iso", 99); res.Found || !res.Loaded || res.Label(99) != "#99 (deleted)" {
		t.Errorf("deleted iso = %+v %q", res, res.Label(99))
	}
	if res := l.Lookup("nonsense", 1); res.Loaded || res.Label(1) != "#1" {
		t.Errorf("unknown kind = %+v", res)
	}
	if n, ok := l.Machine(m.ID); !ok || n.Name != "LAB-01" {
		t.Errorf("machine = %+v %v", n, ok)
	}

	evs := []LogEvent{
		{Actor: "UUID-1111-2222", Target: "software/" + itoa(int64(sw.ID))},
		{Actor: m.AgentID, Target: "machine:" + itoa(int64(m.ID))}, // legacy agent-ID actor
		{Actor: "admin", Target: "machine:" + itoa(int64(m.ID))},
		{Actor: "admin", Target: "iso:77"},
		{Actor: "admin", Target: "group:Lab"},
	}
	l.EnrichLogs(evs)
	if evs[0].MachineName != "LAB-01" || evs[0].TargetName != "7-Zip" || evs[0].TargetKind != KindSoftware {
		t.Errorf("ev0 = %+v", evs[0])
	}
	if evs[1].MachineName != "LAB-01" || evs[1].TargetName != "LAB-01" {
		t.Errorf("agent-id actor not resolved: %+v", evs[1])
	}
	if evs[2].MachineName != "LAB-01" {
		t.Errorf("machine target should fill machine_name: %+v", evs[2])
	}
	if evs[3].TargetName != "#77 (deleted)" {
		t.Errorf("deleted target = %q", evs[3].TargetName)
	}
	if evs[4].TargetName != "" || evs[4].TargetKind != "" {
		t.Errorf("group name target should be untouched: %+v", evs[4])
	}

	// A nil lookup never panics and resolves nothing.
	var nl *NameLookup
	nl.EnrichLogs(evs)
	if res := nl.Lookup(KindImage, 1); res.Loaded {
		t.Errorf("nil lookup loaded: %+v", res)
	}
	if res := NewNameLookup(ctx, nil).Lookup(KindMachine, 1); res.Loaded {
		t.Errorf("repo-less lookup loaded: %+v", res)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestInUseErrorDelete(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	isos := NewISORepo(db)
	iso, err := isos.Create(ctx, ISO{Name: "Win11 23H2", OSType: "windows-11"})
	if err != nil {
		t.Fatal(err)
	}
	images := NewImageRepo(db)
	for _, n := range []string{"Office", "Lab", "Kiosk"} {
		if _, err := images.Create(ctx, Image{Name: n, ISOID: &iso.ID}); err != nil {
			t.Fatal(err)
		}
	}
	err = isos.Delete(ctx, iso.ID)
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("want ErrInUse, got %v", err)
	}
	var iu *InUseError
	if !errors.As(err, &iu) {
		t.Fatalf("want *InUseError, got %T", err)
	}
	if got, want := iu.Friendly(), `Can't delete "Win11 23H2": used by 3 images (Kiosk, Lab, Office)`; got != want {
		t.Errorf("Friendly = %q, want %q", got, want)
	}
	// The API-facing text keeps its historical shape.
	if !strings.Contains(err.Error(), "in use (referenced by 3 images)") {
		t.Errorf("Error() = %q", err.Error())
	}

	loadouts := NewSoftwareLoadoutRepo(db)
	parent, err := loadouts.Create(ctx, SoftwareLoadout{Name: "Base"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadouts.Create(ctx, SoftwareLoadout{Name: "Base-Dev", ParentID: &parent.ID}); err != nil {
		t.Fatal(err)
	}
	err = loadouts.Delete(ctx, parent.ID)
	if !errors.As(err, &iu) || iu.Friendly() != `Can't delete "Base": used by 1 child loadout (Base-Dev)` {
		t.Errorf("loadout in use = %v", err)
	}
}

func TestInUseFriendlyTruncates(t *testing.T) {
	e := &InUseError{Kind: "iso", ID: 3, Refs: []InUseRef{{Noun: "image", Names: []string{"a", "b", "c", "d", "e", "f", "g"}}}}
	if got := e.Friendly(); got != `Can't delete "iso #3": used by 7 images (a, b, c, d, e, +2 more)` {
		t.Errorf("Friendly = %q", got)
	}
	if e.Count() != 7 {
		t.Errorf("Count = %d", e.Count())
	}
}
