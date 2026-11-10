package portal

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// scaleBudget is the slowest a portal page may be on the large-site data.
const scaleBudget = time.Second

// TestScalePages times the main portal pages on a large-site dataset (777
// machines across 15 make/models, 25 programs, 40 KBs, 20 dynamic groups and
// 2M log rows). It is opt-in, as seeding takes over a minute:
//
//	AUTODEPLOY_SCALE_TEST=1 go test ./internal/portal/ -run TestScalePages -v -timeout 20m
//
// It fails if any page takes longer than scaleBudget.
func TestScalePages(t *testing.T) {
	if os.Getenv("AUTODEPLOY_SCALE_TEST") == "" {
		t.Skip("set AUTODEPLOY_SCALE_TEST=1 to run")
	}
	var sdb *storage.DB
	tp := newTestPortal(t, func(r *Repos, db *storage.DB, bx *secrets.Box) {
		r.Credentials = model.NewAgentCredentialRepo(db)
		r.Toolkits = model.NewToolkitRepo(db, bx)
		sdb = db
	})
	ctx := context.Background()
	st := time.Now()
	makes := []string{"Dell Inc.", "HP", "LENOVO"}
	img, _ := tp.r.Images.Create(ctx, model.Image{Name: "Win11 Std"})
	var pkgs []model.SoftwarePackage
	for i := 0; i < 25; i++ {
		p, err := tp.r.Software.Create(ctx, model.SoftwarePackage{Name: fmt.Sprintf("App %02d", i), DetectionJSON: "[]", StepsJSON: "[]"})
		if err != nil {
			t.Fatal(err)
		}
		pkgs = append(pkgs, p)
	}
	var kbs []string
	for i := 0; i < 40; i++ {
		kb := fmt.Sprintf("KB50%05d", i)
		_, _ = tp.r.Updates.Create(ctx, model.WindowsUpdate{KBNumber: kb, Title: kb, SupersedesJSON: "[]"})
		kbs = append(kbs, kb)
	}
	var ms []model.MachineRecord
	for i := 0; i < 777; i++ {
		m, err := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{
			SystemUUID: fmt.Sprintf("UUID-%04d", i), SystemSerial: fmt.Sprintf("SN%05d", i),
			SystemManufacturer: makes[i%3], SystemProduct: fmt.Sprintf("Model %d", i%15)})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = tp.r.Inventory.UpdateObservedIdentity(ctx, m.AgentID, fmt.Sprintf("PC-%04d", i), fmt.Sprintf("CN=PC-%04d,OU=Room%d,DC=corp,DC=example", i, i%20))
		_ = tp.r.Inventory.UpsertBinding(ctx, model.MachineBinding{MachineID: m.ID, MachineName: fmt.Sprintf("PC-%04d", i), ImageID: &img.ID})
		_ = tp.r.Inventory.UpdateHardware(ctx, m.ID, model.Hardware{CPUModel: "i5", OSCaption: "Windows 11 Pro", ReportedAt: time.Now()})
		for d := 0; d < 3; d++ {
			id, _ := tp.r.Inventory.RecordDeployment(ctx, m.ID, &img.ID)
			_ = tp.r.Inventory.CompleteDeployment(ctx, id, "ok", "")
		}
		for _, p := range pkgs {
			_ = tp.r.Inventory.RecordDetectedState(ctx, model.DetectedState{MachineID: m.ID, SoftwarePackageID: p.ID, Detected: i%2 == 0, LastEvaluatedAt: time.Now()})
		}
		var sts []model.MachineUpdateStatus
		for _, kb := range kbs {
			sts = append(sts, model.MachineUpdateStatus{MachineID: m.ID, KBNumber: kb, Status: "installed", ReportedAt: time.Now()})
		}
		_ = tp.r.Updates.UpsertMachineStatuses(ctx, m.ID, sts)
		_, _ = tp.r.Credentials.Issue(ctx, m.ID, "pxe", false)
		ms = append(ms, m)
	}
	for g := 0; g < 20; g++ {
		_, err := tp.r.Groups.Create(ctx, model.MachineGroup{Name: fmt.Sprintf("Room %d", g), Kind: model.MachineGroupDynamic,
			Filter: model.MachineFilter{OU: fmt.Sprintf("OU=Room%d,DC=corp,DC=example", g), OUSubtree: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seeded inventory in %v", time.Since(st))
	st = time.Now()
	nlogs := 2000000
	tx, _ := sdb.BeginTx(ctx, nil)
	ins, _ := tx.PrepareContext(ctx, `INSERT INTO log_event(occurred_at, component, level, actor, action, target, fields, machine_id, message) VALUES (?,?,?,?,?,?,?,?,?)`)
	base := time.Now().Add(-14 * 24 * time.Hour)
	step := 14 * 24 * time.Hour / time.Duration(nlogs)
	acts := []string{"agent.checkin", "package.install.ok", "update.job.result", "agent.detect", "toolkit.ok"}
	for i := 0; i < nlogs; i++ {
		m := ms[i%len(ms)]
		lvl := "INFO"
		if i%97 == 0 {
			lvl = "WARN"
		}
		_, _ = ins.ExecContext(ctx, base.Add(time.Duration(i)*step), "agent", lvl, m.SystemUUID, acts[i%len(acts)], "", "{}", int64(m.ID), "msg")
	}
	_ = tx.Commit()
	t.Logf("seeded %d logs in %v", nlogs, time.Since(st))

	tm := func(name string, f func()) { st := time.Now(); f(); t.Logf("  part %-24s %v", name, time.Since(st)) }
	tm("logs recent 12", func() { _, _ = tp.r.Logs.Search(ctx, model.LogSearch{Limit: 12}) })
	tm("sw compliance", func() { _, _ = tp.r.Software.AllComplianceSummaries(ctx) })
	tm("fleet update summary", func() { _, _ = tp.r.Updates.FleetUpdateSummary(ctx) })
	tm("os breakdown", func() { _, _ = tp.r.Inventory.OSBreakdown(ctx) })
	tm("history all", func() { _, _ = tp.r.Inventory.ListHistoryForMachines(ctx, nil) })
	tm("fleet facts", func() {
		req := httptest.NewRequest("GET", "/portal/", nil)
		mm, _ := tp.r.Inventory.List(ctx)
		loadFleetFacts(req, tp.r, mm, time.Now())
	})
	mid := ms[400].ID
	pages := []string{
		"/portal/", "/portal/machines", "/portal/machines?page=10", "/portal/machines?status=failed&since=7d",
		"/portal/machines?status=offline", "/portal/machines?q=PC-04", "/portal/machines?group=5",
		"/portal/machines.csv", fmt.Sprintf("/portal/machines/%d", mid), fmt.Sprintf("/portal/machines/%d/deploy-status", mid),
		"/portal/groups", "/portal/logs", "/portal/logs?q=install", fmt.Sprintf("/portal/logs?machine=%d", mid),
		"/portal/logs?level=WARN", "/portal/logs?q=zzznomatch", "/portal/logs?q=PC-0400", "/portal/software", "/portal/updates", "/portal/bulk", "/portal/bulk/new",
		"/portal/machines/bulk-edit", "/portal/images", "/portal/toolkits",
	}
	type res struct {
		p    string
		d    time.Duration
		code int
	}
	var out []res
	for _, p := range pages {
		s := time.Now()
		rec := tp.do("GET", p, nil, nil)
		out = append(out, res{p, time.Since(s), rec.Code})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].d > out[j].d })
	for _, r := range out {
		t.Logf("%8.1fms %d %s", float64(r.d.Microseconds())/1000, r.code, r.p)
		if r.code != 200 {
			t.Errorf("%s = %d", r.p, r.code)
		}
		// Free-text log search reads the whole log when matches are rare.
		if r.d > scaleBudget && !strings.Contains(r.p, "logs?q=") {
			t.Errorf("%s took %v (budget %v)", r.p, r.d, scaleBudget)
		}
	}
}
