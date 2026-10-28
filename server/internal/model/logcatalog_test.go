package model

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// logCallPatterns find action-code literals in the source tree: slog calls
// (agent, boot client and server log through slog, the message is the
// action), logging.Event, and LogEvent / deployFailure literals' Action.
var logCallPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.(?:Info|Warn|Error|Debug)(?:Context)?\((?:ctx, )?"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)*)"`),
	regexp.MustCompile(`logging\.Event\(ctx, "([a-z][a-z0-9_.]*)"`),
	regexp.MustCompile(`\.Log(?:Attrs)?\([^,()]+, [^,()]+, "([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"`),
	regexp.MustCompile(`\bAction:\s+"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"`),
}

// TestLogCatalogCoversSource fails when any action code logged anywhere in
// the repository has no catalogue entry, so a new log call can't ship with
// a raw code as its only description.
func TestLogCatalogCoversSource(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, dir := range []string{"agent", "boot-client", "server"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			t.Skipf("source tree not available: %v", err)
		}
		_ = filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, re := range logCallPatterns {
				for _, m := range re.FindAllStringSubmatch(string(src), -1) {
					found[m[1]] = strings.TrimPrefix(p, root+"/")
				}
			}
			return nil
		})
	}
	t.Logf("scanned %d action codes", len(found))
	if len(found) < 150 {
		t.Fatalf("only found %d action codes; the scan patterns are probably broken", len(found))
	}
	var missing []string
	for a, where := range found {
		if !HasLogCatalogEntry(a) {
			missing = append(missing, a+" ("+where+")")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d action codes have no log catalogue entry (add them to logCatalog in logcatalog.go):\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// Every entry renders to something readable even with no fields at all
// (old rows), and never leaks template syntax.
func TestLogCatalogEntriesRender(t *testing.T) {
	for action, en := range logCatalog {
		if en.Cat == "" {
			t.Errorf("%s: no category", action)
		}
		d := DescribeLog(LogEvent{Action: action, Fields: "{}"}, LogNames{})
		if d.Message == "" {
			t.Errorf("%s: empty message", action)
		}
		if strings.ContainsAny(d.Message, "{}[]|") {
			t.Errorf("%s: template syntax leaked: %q", action, d.Message)
		}
	}
}

func TestDescribeLogMessages(t *testing.T) {
	cases := []struct {
		ev    LogEvent
		names LogNames
		want  string
	}{
		{LogEvent{Action: "package.install.fail", Target: "Chrome", Level: "ERROR",
			Fields: `{"step":2,"steps":2,"exit_code":1603,"error":"exit status 1603"}`}, LogNames{},
			"Chrome failed to install at step 2 of 2 — exit code 1603 (fatal error during installation)"},
		// Old agents sent the Go error as {} and no step detail.
		{LogEvent{Action: "package.install.fail", Target: "Chrome", Level: "ERROR", Fields: `{"error":{}}`}, LogNames{},
			"Chrome failed to install"},
		{LogEvent{Action: "package.install.fail", Level: "ERROR", Fields: `{}`}, LogNames{},
			"A package failed to install"},
		{LogEvent{Action: "deploy.partition.fail", Level: "ERROR",
			Fields: `{"error_code":"PARTITION","message":"Couldn't prepare the disk (partitioning failed): parted: exit status 1"}`}, LogNames{},
			"Couldn't prepare the disk (partitioning failed): parted: exit status 1"},
		{LogEvent{Action: "deploy.partition.fail", Level: "ERROR", Target: "/dev/sda", Fields: `{"error":"exit status 1"}`}, LogNames{},
			"Couldn't partition the disk /dev/sda: exit status 1"},
		{LogEvent{Action: "manifest.received", Target: "image:1", Fields: `{"items":6}`}, LogNames{Target: "Lab"},
			"Received the deployment plan for Lab (6 items)"},
		{LogEvent{Action: "image.delete", User: "admin", Target: "image:4", Fields: `{"name":"Kiosk"}`}, LogNames{Target: "#4 (deleted)"},
			"admin deleted image “Kiosk”"},
		{LogEvent{Action: "image.update", User: "admin", Target: "image:2"}, LogNames{Target: "Office"},
			"admin updated image Office"},
		{LogEvent{Action: "deploy.step_failed", Target: "machine:1"}, LogNames{Target: "PC-0000"},
			"Deploy step failed (PC-0000)"},
		{LogEvent{Action: "steps.exec.stdout", Fields: `{"line":"hello"}`}, LogNames{}, "hello"},
	}
	for _, c := range cases {
		got := DescribeLog(c.ev, c.names).Message
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.ev.Action, got, c.want)
		}
	}
}

func TestDescribeLogLevelHintOutcome(t *testing.T) {
	d := DescribeLog(LogEvent{Action: "steps.exec.stderr", Level: "WARN", Fields: `{"line":"x"}`}, LogNames{})
	if d.Level != "INFO" {
		t.Errorf("stderr from old agents should read as INFO, got %s", d.Level)
	}
	d = DescribeLog(LogEvent{Action: "deploy.disk.none", Level: "ERROR", Fields: `{"error_code":"NO-DISK"}`}, LogNames{})
	if !strings.Contains(d.Hint, "AHCI") || d.Outcome != "failed" || d.Category != LogCatDeployment {
		t.Errorf("deploy.disk.none: %+v", d)
	}
	d = DescribeLog(LogEvent{Action: "package.install.fail", Level: "ERROR", Fields: `{"exit_code":1618}`}, LogNames{})
	if !strings.Contains(d.Hint, "already running") {
		t.Errorf("1618 hint: %q", d.Hint)
	}
	if c := DescribeLog(LogEvent{Action: "kbscan.whatever"}, LogNames{}).Category; c != LogCatUpdates {
		t.Errorf("prefix category: %s", c)
	}
	if c := DescribeLog(LogEvent{Action: "zzz.unknown"}, LogNames{}).Category; c != LogCatSystem {
		t.Errorf("default category: %s", c)
	}
}

// Search filters added in 0035: level overrides, category, free text over
// catalogue text, machine (incl. untagged old rows), deployment window,
// user, and id cursor paging.
func TestLogSearchFilters(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	r := NewLogRepo(db)
	now := time.Now().UTC()
	evs := []LogEvent{
		{Component: "agent", Level: "WARN", Actor: "agent-1", Action: "steps.exec.stderr", Fields: `{"line":"x"}`, OccurredAt: now.Add(-50 * time.Minute)},
		{Component: "agent", Level: "ERROR", Actor: "UUID-1", Action: "package.install.fail", Target: "Chrome", Fields: `{}`, OccurredAt: now.Add(-40 * time.Minute)},
		{Component: "agent", Level: "INFO", Actor: "UUID-2", Action: "package.install.ok", Target: "7-Zip", MachineID: 2, DeploymentID: 9, OccurredAt: now.Add(-30 * time.Minute)},
		{Component: "portal", Level: "INFO", Actor: "admin", User: "admin", Action: "image.update", Target: "image:1", OccurredAt: now.Add(-20 * time.Minute)},
		{Component: "boot", Level: "INFO", Actor: "UUID-9", Action: "wat.unknown", OccurredAt: now.Add(-10 * time.Minute)},
	}
	if err := r.AppendBatch(ctx, evs); err != nil {
		t.Fatal(err)
	}
	count := func(s LogSearch) int {
		t.Helper()
		got, err := r.Search(ctx, s)
		if err != nil {
			t.Fatalf("search %+v: %v", s, err)
		}
		n, err := r.Count(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if n != len(got) {
			t.Fatalf("count %d != rows %d", n, len(got))
		}
		return n
	}
	if n := count(LogSearch{Levels: []string{"WARN"}}); n != 0 {
		t.Errorf("WARN: old stderr row should count as INFO, got %d rows", n)
	}
	if n := count(LogSearch{Levels: []string{"WARN", "ERROR"}}); n != 1 {
		t.Errorf("WARN+ERROR: %d", n)
	}
	if n := count(LogSearch{Category: LogCatSoftware}); n != 3 {
		t.Errorf("Software: %d", n)
	}
	if n := count(LogSearch{Category: LogCatSystem}); n != 1 {
		t.Errorf("System: %d", n)
	}
	if n := count(LogSearch{Text: "failed to install"}); n != 1 {
		t.Errorf("text via catalogue: %d", n)
	}
	if n := count(LogSearch{Text: "chrome"}); n != 1 {
		t.Errorf("text via target: %d", n)
	}
	if n := count(LogSearch{Machine: &LogMachine{ID: 1, Actors: []string{"UUID-1", "agent-1"}}}); n != 2 {
		t.Errorf("machine 1 (untagged rows by actor): %d", n)
	}
	if n := count(LogSearch{Machine: &LogMachine{ID: 2, Actors: []string{"UUID-2"}}}); n != 1 {
		t.Errorf("machine 2: %d", n)
	}
	if n := count(LogSearch{DeploymentID: 9}); n != 1 {
		t.Errorf("deployment tagged: %d", n)
	}
	w := &LogWindow{From: now.Add(-45 * time.Minute), To: now, Machine: LogMachine{ID: 1, Actors: []string{"UUID-1"}}}
	if n := count(LogSearch{DeploymentID: 9, Window: w}); n != 2 {
		t.Errorf("deployment with window: %d", n)
	}
	if n := count(LogSearch{User: "admin"}); n != 1 {
		t.Errorf("user: %d", n)
	}
	page, _ := r.Search(ctx, LogSearch{ByID: true, Limit: 2})
	if len(page) != 2 || page[0].ID <= page[1].ID {
		t.Fatalf("page1 %+v", page)
	}
	next, _ := r.Search(ctx, LogSearch{BeforeID: page[1].ID, Limit: 10})
	if len(next) != 3 || next[0].ID >= page[1].ID {
		t.Errorf("page2 %d rows", len(next))
	}
	if n, _ := r.CountNewer(ctx, LogSearch{}, page[1].ID); n != 2 {
		t.Errorf("CountNewer %d", n)
	}
	// The new columns round-trip.
	got, _ := r.Search(ctx, LogSearch{Machine: &LogMachine{ID: 2}})
	if len(got) != 1 || got[0].MachineID != 2 || got[0].DeploymentID != 9 {
		t.Errorf("round trip: %+v", got)
	}
}
