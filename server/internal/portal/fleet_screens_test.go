package portal

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// ---- Fleet status derivation ----

func TestDeriveMachineStatusThresholds(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	run := func(outcome string) *model.DeploymentRecord {
		return &model.DeploymentRecord{Outcome: outcome, StartedAt: ago(time.Hour)}
	}
	cases := []struct {
		name     string
		lastSeen time.Time
		latest   *model.DeploymentRecord
		reimage  bool
		want     string
	}{
		{"just checked in", ago(time.Minute), nil, false, statusOnline},
		{"online edge", ago(machineOnlineWithin), nil, false, statusOnline},
		{"just past online", ago(machineOnlineWithin + time.Second), nil, false, statusIdle},
		{"idle for days", ago(6 * 24 * time.Hour), run("ok"), false, statusIdle},
		{"offline edge", ago(machineOfflineAfter), nil, false, statusIdle},
		{"offline", ago(machineOfflineAfter + time.Minute), nil, false, statusOffline},
		{"deploying", ago(2 * time.Minute), run("in_progress"), false, statusDeploying},
		{"stalled", ago(deployStallAfter + time.Minute), run("in_progress"), false, statusStalled},
		{"deploying wins over reimage flag", ago(time.Minute), run("in_progress"), true, statusDeploying},
		{"reimage pending", ago(time.Minute), run("ok"), true, statusReimage},
		{"reimage wins over failed", ago(time.Minute), run("failed"), true, statusReimage},
		{"failed while online", ago(time.Minute), run("failed"), false, statusFailed},
		{"failed while offline", ago(30 * 24 * time.Hour), run("failed"), false, statusFailed},
	}
	for _, c := range cases {
		got := deriveMachineStatus(now, c.lastSeen, c.latest, c.reimage)
		if got.Key != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got.Key, c.want)
		}
		if got.Label == "" || got.Dot == "" || got.Title == "" {
			t.Errorf("%s: status must carry a text label, dot and tooltip: %+v", c.name, got)
		}
	}
	// Rank orders the most urgent first.
	f, _ := statusByKey(statusFailed)
	o, _ := statusByKey(statusOnline)
	if f.Rank >= o.Rank {
		t.Error("failed must sort before online")
	}
	if humanDuration(machineOfflineAfter) != "7 days" || humanDuration(machineOnlineWithin) != "15 minutes" {
		t.Errorf("tooltip durations: %q, %q", humanDuration(machineOfflineAfter), humanDuration(machineOnlineWithin))
	}
}

// ---- Machine list filters (server side, kept in the URL) ----

func TestMachineFiltersMatch(t *testing.T) {
	now := time.Now()
	img := model.ID(3)
	ms := []model.MachineRecord{
		{ID: 1, SystemManufacturer: "Dell Inc.", SystemProduct: "OptiPlex 7090", ADJoinStatus: model.ADJoinJoined, LastSeen: now},
		{ID: 2, SystemManufacturer: "HP", SystemProduct: "EliteDesk", ADJoinStatus: model.ADTrustBroken, LastSeen: now},
		{ID: 3, SystemManufacturer: "HP", SystemProduct: "EliteDesk", LastSeen: now},
	}
	facts := fleetFacts{
		Bindings: map[model.ID]model.MachineBinding{1: {ImageID: &img}},
		Latest: map[model.ID]model.DeploymentRecord{
			1: {Outcome: "failed", StartedAt: now.Add(-2 * 24 * time.Hour)},
			2: {Outcome: "failed", StartedAt: now.Add(-20 * 24 * time.Hour)},
		},
		Status: map[model.ID]machineStatus{},
	}
	for _, m := range ms {
		var l *model.DeploymentRecord
		if d, ok := facts.Latest[m.ID]; ok {
			l = &d
		}
		facts.Status[m.ID] = deriveMachineStatus(now, m.LastSeen, l, false)
	}
	ids := func(f machineFilters) []model.ID {
		var out []model.ID
		for _, m := range f.apply(ms, facts, now) {
			out = append(out, m.ID)
		}
		return out
	}
	for name, c := range map[string]struct {
		f    machineFilters
		want []model.ID
	}{
		"none":             {machineFilters{}, []model.ID{1, 2, 3}},
		"failed":           {machineFilters{Status: "failed"}, []model.ID{1, 2}},
		"failed last 7d":   {machineFilters{Status: "failed", Since: "7d"}, []model.ID{1}},
		"image":            {machineFilters{Image: "3"}, []model.ID{1}},
		"no image":         {machineFilters{Image: "none"}, []model.ID{2, 3}},
		"ad problem":       {machineFilters{AD: "problem"}, []model.ID{2}},
		"ad not reported":  {machineFilters{AD: "none"}, []model.ID{3}},
		"model ignorecase": {machineFilters{Model: "hp elitedesk"}, []model.ID{2, 3}},
		"unknown since":    {machineFilters{Since: "1y"}, nil},
	} {
		got := ids(c.f)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v want %v", name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v want %v", name, got, c.want)
			}
		}
	}
}

// seedFleet creates three machines through the repos: one whose latest
// deployment failed, one with a broken AD trust, one flagged for re-image.
func seedFleet(t *testing.T, tp *testPortal) (failed, trust, reimage model.MachineRecord) {
	t.Helper()
	ctx := context.Background()
	mk := func(uuid, serial, mfr string) model.MachineRecord {
		m, err := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: uuid, SystemSerial: serial, SystemManufacturer: mfr, SystemProduct: "Box"})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	failed = mk("aaaaaaaa-0000-0000-0000-000000000001", "SN-FAILED", "Dell Inc.")
	trust = mk("aaaaaaaa-0000-0000-0000-000000000002", "SN-TRUST", "HP")
	reimage = mk("aaaaaaaa-0000-0000-0000-000000000003", "SN-REIMAGE", "HP")
	// An older run left open, then a newer failed one: the latest is failed.
	if _, err := tp.r.Inventory.RecordDeployment(ctx, failed.ID, nil); err != nil {
		t.Fatal(err)
	}
	did, err := tp.r.Inventory.RecordDeployment(ctx, failed.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tp.r.Inventory.CompleteDeployment(ctx, did, "failed", "disk"); err != nil {
		t.Fatal(err)
	}
	if _, err := tp.r.Inventory.UpdateADJoinStatus(ctx, trust.AgentID, model.ADTrustBroken, "trust failed"); err != nil {
		t.Fatal(err)
	}
	if err := tp.r.Inventory.SetReimagePending(ctx, reimage.ID, 0); err != nil {
		t.Fatal(err)
	}
	return failed, trust, reimage
}

func TestMachineListStatusFiltersAndCSV(t *testing.T) {
	tp := newTestPortal(t)
	seedFleet(t, tp)

	out := tp.get("/portal/machines?status=failed")
	if !strings.Contains(out, "SN-FAILED") || strings.Contains(out, "SN-TRUST") || strings.Contains(out, "SN-REIMAGE") {
		t.Errorf("?status=failed must keep only the failed machine")
	}
	// Status column: dot + text, never colour alone.
	if !regexp.MustCompile(`<span class="dot bad" aria-hidden="true"></span>Failed`).MatchString(out) {
		t.Error("want a status dot with its text label")
	}
	// The filter selects join the GET search form and are not duplicated as hidden fields.
	if !strings.Contains(out, `id="machine-search"`) || !strings.Contains(out, `form="machine-search"`) {
		t.Error("filter selects must submit with the search form")
	}
	if !strings.Contains(out, `<input type="hidden" name="status" value="failed">`) {
		t.Error("the active status chip must ride along with a search")
	}
	// Group links keep the filter.
	if !strings.Contains(out, `href="/portal/machines?status=failed"`) {
		t.Error("All machines link should keep ?status=")
	}

	out = tp.get("/portal/machines?ad=problem")
	if !strings.Contains(out, "SN-TRUST") || strings.Contains(out, "SN-FAILED") {
		t.Error("?ad=problem must keep only the AD failure")
	}
	out = tp.get("/portal/machines?status=reimage&q=SN")
	if !strings.Contains(out, "SN-REIMAGE") || strings.Contains(out, "SN-TRUST") {
		t.Error("status filter must combine with ?q=")
	}
	out = tp.get("/portal/machines?status=deploying")
	if !strings.Contains(out, "No machines match these filters") || !strings.Contains(out, "Clear search and filters") {
		t.Error("empty filtered list needs its own empty state with Clear")
	}
	// Old bookmarks still work (unknown values just match nothing).
	tp.get("/portal/machines?q=x&status=bogus&since=forever")

	// Sort by status: most urgent first.
	out = tp.get("/portal/machines?sort=status&dir=asc")
	if i, j := strings.Index(out, "SN-FAILED"), strings.Index(out, "SN-TRUST"); i < 0 || j < 0 || i > j {
		t.Error("sort=status asc must put the failed machine before an online one")
	}

	// CSV: old columns unchanged, status appended, filters honoured.
	rec := tp.do("GET", "/portal/machines.csv?status=failed", nil, nil)
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if !strings.HasPrefix(lines[0], "id,name,agent_id,manufacturer,product,serial,system_uuid,bios_vendor,bios_version,board_manufacturer,board_product,image_id,first_seen,last_seen,image_name,reported_name,display_name,status") {
		t.Errorf("CSV header changed incompatibly: %s", lines[0])
	}
	if len(lines) != 2 || !strings.HasSuffix(lines[1], ",Failed") {
		t.Errorf("CSV must honour ?status= and append the status: %v", lines)
	}
}

// ---- Machine detail: current state, no contradictory progress ----

func TestMachineDetailFailedLatestHidesProgress(t *testing.T) {
	tp := newTestPortal(t)
	failed, _, _ := seedFleet(t, tp)
	out := tp.get("/portal/machines/" + idStr(failed.ID))
	if !strings.Contains(out, `class="deploy-progress idle"`) {
		t.Error("an older open run behind a newer failed one must not show live progress")
	}
	if !strings.Contains(out, "Current state") || !strings.Contains(out, ">Failed<") && !strings.Contains(out, "</span>Failed</span>") {
		t.Error("want the Current state card naming the outcome")
	}
	rec := tp.do("GET", "/portal/machines/"+idStr(failed.ID)+"/deploy-status", nil, nil)
	var st map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["active"] != false || st["outcome"] != "failed" || st["finished"] != true {
		t.Errorf("deploy-status must report the latest (failed) run, got %v", st)
	}
	assertNoRawTokens(t, out)
}

// assertNoRawTokens fails when page text (outside tags) shows a stored
// deployment token instead of its plain label.
func assertNoRawTokens(t *testing.T, html string) {
	t.Helper()
	text := regexp.MustCompile(`(?s)<script.*?</script>|<[^>]*>`).ReplaceAllString(html, " ")
	for _, tok := range []string{"in_progress", "first-logon", "agent-online", "specialize"} {
		if strings.Contains(text, tok) {
			t.Errorf("page text shows raw token %q", tok)
		}
	}
}

func TestMachineDetailHistoryUsesPlainLabels(t *testing.T) {
	now := time.Now()
	d := baseMachineData()
	d["History"] = []historyRowT{
		{DeploymentRecord: model.DeploymentRecord{ID: 2, StartedAt: now, Outcome: "in_progress", Phase: "first-logon"}},
		{DeploymentRecord: model.DeploymentRecord{ID: 1, StartedAt: now, CompletedAt: &now, Outcome: "ok", Phase: "agent-online"}},
	}
	d["LatestDeploy"] = &model.DeploymentRecord{ID: 2, StartedAt: now, Outcome: "in_progress", Phase: "first-logon"}
	d["DeployCount"] = 2
	out := renderMachineDetail(t, d)
	for _, want := range []string{"In progress", "First logon", "Succeeded", "Agent started"} {
		if !strings.Contains(out, want) {
			t.Errorf("want plain label %q", want)
		}
	}
	assertNoRawTokens(t, out)
}

// TestTemplatesDontPrintRawDeployTokens is the guard: no template prints a
// deployment's stored outcome or phase directly — they go through
// outcomeLabel / phaseLabel.
func TestTemplatesDontPrintRawDeployTokens(t *testing.T) {
	re := regexp.MustCompile(`\{\{-?\s*(\$\w*)?\.(Outcome|Phase)\s*-?\}\}`)
	files, _ := fs.Glob(assetsFS, "templates/*.html")
	for _, f := range files {
		b, _ := fs.ReadFile(assetsFS, f)
		for _, loc := range re.FindAllIndex(b, -1) {
			if !insideTag(string(b), loc[0]) {
				t.Errorf("%s prints a raw deployment token: %s", f, b[loc[0]:loc[1]])
			}
		}
	}
	if outcomeLabel("in_progress") != "In progress" || phaseLabel("first-logon", "in_progress") != "First logon" ||
		phaseLabel("", "ok") != "Not recorded" || outcomeLabel("needs_reboot") != "Needs reboot" {
		t.Error("label helpers changed")
	}
}

// ---- Actions: no default, type-to-confirm re-image ----

func TestActionPickerHasNoDefault(t *testing.T) {
	tp := newTestPortal(t)
	failed, _, _ := seedFleet(t, tp)
	out := tp.get("/portal/machines/" + idStr(failed.ID))
	if regexp.MustCompile(`name="action" value="\w+" checked`).MatchString(out) {
		t.Error("no action may be preselected")
	}
	if !strings.Contains(out, "data-needs-action disabled") {
		t.Error("Queue button must start disabled")
	}
	if !strings.Contains(out, `name="reimage_confirm_required" value="1"`) || !strings.Contains(out, `data-confirm-match="`) {
		t.Errorf("portal re-image form must carry the type-to-confirm field")
	}
	if formPrefill(nil, time.UTC)["Action"] != "" {
		t.Error("bulk form prefill must not preselect an action either")
	}
}

func TestMachineActionReimageConfirm(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	m, _ := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "bbbbbbbb-0000-0000-0000-000000000001", SystemSerial: "S1"})
	_ = tp.r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "LAB-07"})
	path := "/portal/machines/" + idStr(m.ID) + "/action"
	ops := func() int {
		l, _ := tp.r.Bulk.ListOperations(ctx)
		return len(l)
	}

	rec := tp.do("POST", path, url.Values{}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Choose an action first") {
		t.Errorf("no action: want 422 with a message, got %d", rec.Code)
	}
	rec = tp.do("POST", path, url.Values{"action": {"reimage"}, "reimage_confirm_required": {"1"}, "reimage_confirm": {"LAB-08"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `value="LAB-08"`) ||
		!strings.Contains(rec.Body.String(), `name="action" value="reimage" checked`) {
		t.Errorf("wrong confirm: want 422 re-render keeping the choice, got %d", rec.Code)
	}
	if ops() != 0 {
		t.Fatal("a rejected re-image must not queue anything")
	}
	rec = tp.do("POST", path, url.Values{"action": {"reimage"}, "reimage_confirm_required": {"1"}, "reimage_confirm": {" lab-07 "}}, nil)
	if rec.Code != http.StatusFound || ops() != 1 {
		t.Errorf("matching confirm (case-insensitive) must queue: %d ops=%d", rec.Code, ops())
	}
	// Older callers posting without the marker keep working.
	rec = tp.do("POST", path, url.Values{"action": {"reimage"}}, nil)
	if rec.Code != http.StatusFound || ops() != 2 {
		t.Errorf("legacy re-image POST must still queue: %d ops=%d", rec.Code, ops())
	}
	// Payload errors stay inline next to the field.
	rec = tp.do("POST", path, url.Values{"action": {"script"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `id="err-script_body"`) {
		t.Errorf("empty script: want inline field error, got %d", rec.Code)
	}
}

// ---- Binding save errors are inline ----

func TestMachineBindingRejectedInline(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	m, _ := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "cccccccc-0000-0000-0000-000000000001"})
	_ = tp.r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: "LAB-01"})
	path := "/portal/machines/" + idStr(m.ID) + "/binding"

	rec := tp.do("POST", path, url.Values{"machine_name": {"WAY-TOO-LONG-COMPUTER-NAME"}, "target_ou": {"Lab Computers"}}, nil)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", rec.Code)
	}
	for _, want := range []string{`value="WAY-TOO-LONG-COMPUTER-NAME"`, `id="err-machine_name"`, `id="err-target_ou"`, `value="Lab Computers"`, "data-form-error"} {
		if !strings.Contains(body, want) {
			t.Errorf("rejected save must keep values and show %q", want)
		}
	}
	if b, _ := tp.r.Inventory.GetBinding(ctx, m.ID); b.MachineName != "LAB-01" {
		t.Errorf("nothing may be saved, got %q", b.MachineName)
	}
	rec = tp.do("POST", path, url.Values{"image_id": {"999"}}, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `id="err-image_id"`) {
		t.Errorf("unknown image: want inline error, got %d", rec.Code)
	}
	// Valid values, templates and escaped DNs save as before.
	rec = tp.do("POST", path, url.Values{"machine_name": {"LAB-%serial(4)%"}, "target_ou": {`OU=Lab\, North,DC=corp,DC=example`}}, nil)
	if rec.Code != http.StatusFound {
		t.Errorf("valid binding must save and redirect, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestValidateBindingRules(t *testing.T) {
	ok := []model.MachineBinding{{}, {MachineName: "LAB-01"}, {MachineName: "PC_7"}, {MachineName: "%random(15)%-X"}, {TargetOU: "OU=Lab,DC=corp,DC=example"}, {TargetOU: "CN=Computers,DC=corp,DC=example"}}
	for _, b := range ok {
		if fe := validateBinding(b); fe != nil {
			t.Errorf("%+v should be valid: %v", b, fe.Fields)
		}
	}
	bad := []model.MachineBinding{{MachineName: "1234"}, {MachineName: "LAB 01"}, {MachineName: "lab.01"}, {MachineName: "ABCDEFGHIJKLMNOP"}, {TargetOU: "Lab"}, {TargetOU: "OU=Lab,corp"}}
	for _, b := range bad {
		if fe := validateBinding(b); fe == nil {
			t.Errorf("%+v should be rejected", b)
		}
	}
}

// ---- Dashboard: needs attention ----

func TestNeedsAttentionCounts(t *testing.T) {
	now := time.Now()
	ms := []model.MachineRecord{
		{ID: 1, LastSeen: now},
		{ID: 2, LastSeen: now},
		{ID: 3, LastSeen: now.Add(-10 * 24 * time.Hour)},
		{ID: 4, LastSeen: now.Add(-2 * time.Hour)},
		{ID: 5, LastSeen: now, ADJoinStatus: model.ADJoinFailed},
		{ID: 6, LastSeen: now},
	}
	latest := map[model.ID]model.DeploymentRecord{
		1: {Outcome: "failed", StartedAt: now.Add(-24 * time.Hour)},
		2: {Outcome: "failed", StartedAt: now.Add(-30 * 24 * time.Hour)}, // older than 7 days
		4: {Outcome: "in_progress", StartedAt: now.Add(-3 * time.Hour)},
	}
	f := fleetFacts{Latest: latest, Reimage: map[model.ID]bool{6: true}, Status: map[model.ID]machineStatus{}}
	for _, m := range ms {
		var l *model.DeploymentRecord
		if d, ok := latest[m.ID]; ok {
			l = &d
		}
		f.Status[m.ID] = deriveMachineStatus(now, m.LastSeen, l, f.Reimage[m.ID])
	}
	got := map[string]attentionItem{}
	for _, it := range needsAttention(ms, f, 2, now) {
		got[it.Key] = it
	}
	want := map[string]int{"failed": 1, "stalled": 1, "offline": 1, "reimage": 1, "ad": 1, "iso": 2}
	for k, n := range want {
		if got[k].Count != n {
			t.Errorf("%s: got %d want %d", k, got[k].Count, n)
		}
	}
	if got["failed"].URL != "/portal/machines?status=failed&since=7d" || got["iso"].Label != "ISOs failed to prepare" || got["reimage"].Label != "re-image pending" {
		t.Errorf("links/labels: %+v", got)
	}
	if len(needsAttention(nil, fleetFacts{}, 0, now)) != 0 {
		t.Error("nothing to do must yield no items (the All clear state)")
	}
}

func TestDashboardShowsAttentionAndAllClear(t *testing.T) {
	tp := newTestPortal(t)
	out := tp.get("/portal/")
	if !strings.Contains(out, "All clear") {
		t.Error("an empty fleet is all clear")
	}
	seedFleet(t, tp)
	out = tp.get("/portal/")
	for _, want := range []string{"Needs attention", `href="/portal/machines?status=failed&amp;since=7d"`, `href="/portal/machines?ad=problem"`, `href="/portal/machines?status=reimage"`} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Contains(out, "All clear") {
		t.Error("All clear must not show while there is something to do")
	}
	if strings.Index(out, "Needs attention") > strings.Index(out, "Library") {
		t.Error("Needs attention comes first, the library counts lower")
	}
}

// ---- Delete buttons: named confirms, disabled while in use ----

func TestDeleteDisabledWhileInUseAndConfirmsNameItem(t *testing.T) {
	tp := newTestPortal(t)
	ctx := context.Background()
	used, _ := tp.r.ISOs.Create(ctx, model.ISO{Name: "Win11 23H2", OSType: "windows-11"})
	_, _ = tp.r.ISOs.Create(ctx, model.ISO{Name: "Spare", OSType: "windows-11"})
	if _, err := tp.r.Images.Create(ctx, model.Image{Name: "Lab", ISOID: &used.ID}); err != nil {
		t.Fatal(err)
	}
	out := tp.get("/portal/isos")
	if !strings.Contains(out, `title="In use by 1 image — unlink it from them first"`) {
		t.Errorf("in-use ISO needs a disabled Delete with the reason:\n%s", out)
	}
	if !strings.Contains(out, `data-confirm="Delete ISO “Spare”?"`) {
		t.Error("confirm must name the ISO")
	}
	if strings.Contains(out, `data-confirm="Delete ISO “Win11 23H2”?"`) {
		t.Error("in-use ISO must not offer a working Delete")
	}
	files, _ := fs.Glob(assetsFS, "templates/*.html")
	generic := regexp.MustCompile(`confirm\(?'?"?=?"?(Delete|Remove) this `)
	for _, f := range files {
		if strings.HasPrefix(f, "templates/bulk_") || strings.HasPrefix(f, "templates/settings") || strings.HasPrefix(f, "templates/windowsupdate_") {
			continue // owned by another screen set
		}
		b, _ := fs.ReadFile(assetsFS, f)
		if generic.Match(b) || strings.Contains(string(b), `data-confirm="Delete this`) {
			t.Errorf("%s has a confirm that doesn't name the item", f)
		}
	}
}
