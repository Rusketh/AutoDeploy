package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swaccess"
)

type swFixture struct {
	srv    *httptest.Server
	inv    *model.InventoryRepo
	bulk   *model.BulkRepo
	sw     *model.SoftwarePackageRepo
	images *model.ImageRepo
	check  *swaccess.Checker
}

func newSWFixture(t *testing.T) swFixture {
	t.Helper()
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	inv := model.NewInventoryRepo(db)
	isos, una := model.NewISORepo(db), model.NewUnattendRepo(db)
	images := model.NewImageRepo(db)
	f := swFixture{inv: inv, bulk: model.NewBulkRepo(db, inv), sw: model.NewSoftwarePackageRepo(db), images: images}
	res := resolve.New(images, isos, una)
	f.check = &swaccess.Checker{Inventory: inv, Software: f.sw, Bulk: f.bulk, Resolver: res}
	mux := http.NewServeMux()
	Register(mux, Repos{Inventory: inv, Bulk: f.bulk, Software: f.sw, Images: images, Resolver: res})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f swFixture) do(t *testing.T, method, path, agentID, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if agentID != "" {
		req.Header.Set(swaccess.AgentIDHeader, agentID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPackageItemsBoundToRunningPush(t *testing.T) {
	ctx := context.Background()
	f := newSWFixture(t)
	dep, _ := f.sw.Create(ctx, model.SoftwarePackage{Name: "VC-Redist", StepsJSON: `[{"type":"exe","exe_path":"vc.exe"}]`})
	pkg, _ := f.sw.Create(ctx, model.SoftwarePackage{Name: "App", StepsJSON: `[{"type":"msi","msi_path":"app.msi","msi_args":["PIDKEY=SECRET-KEY"]}]`,
		DependsOn: []model.ID{dep.ID}})
	other, _ := f.sw.Create(ctx, model.SoftwarePackage{Name: "Other"})
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "push-1"})
	stranger, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "push-2"})
	_, jobs, err := f.bulk.CreateOperation(ctx, model.BulkOperation{
		Action: model.BulkActionSoftwarePush, Payload: `{"package_id":` + strconv.FormatInt(int64(pkg.ID), 10) + `}`,
		Target: model.BulkTarget{MachineIDs: []model.ID{m.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	job := strconv.FormatInt(int64(jobs[0].ID), 10)
	pid := strconv.FormatInt(int64(pkg.ID), 10)
	path := "/api/v1/agent/package-items?package=" + pid + "&job=" + job

	if c, body := f.do(t, "GET", "/api/v1/agent/package-items?package="+pid, "", ""); c != http.StatusForbidden || strings.Contains(body, "SECRET-KEY") {
		t.Fatalf("anonymous: %d", c)
	}
	if c, _ := f.do(t, "GET", path, m.AgentID, ""); c != http.StatusForbidden {
		t.Fatalf("job not yet claimed: %d", c)
	}
	if _, err := f.bulk.ClaimJobsFor(ctx, m.ID, 0); err != nil {
		t.Fatal(err)
	}
	if c, _ := f.do(t, "GET", path, stranger.AgentID, ""); c != http.StatusForbidden {
		t.Fatalf("another machine: %d", c)
	}
	otherPath := "/api/v1/agent/package-items?package=" + strconv.FormatInt(int64(other.ID), 10) + "&job=" + job
	if c, _ := f.do(t, "GET", otherPath, m.AgentID, ""); c != http.StatusForbidden {
		t.Fatalf("package other than the job's: %d", c)
	}
	c, body := f.do(t, "GET", path, m.AgentID, "")
	if c != http.StatusOK || !strings.Contains(body, "VC-Redist") || !strings.Contains(body, "SECRET-KEY") {
		t.Fatalf("owner: %d %s", c, body)
	}

	// The running push entitles the machine to the package and its
	// dependency's files -- and nothing else.
	if !f.check.Allowed(ctx, m.ID, pkg.ID) || !f.check.Allowed(ctx, m.ID, dep.ID) || f.check.Allowed(ctx, m.ID, other.ID) {
		t.Fatal("push entitlement wrong")
	}
	if f.check.Allowed(ctx, stranger.ID, pkg.ID) {
		t.Fatal("stranger entitled")
	}
	_, _ = f.bulk.CompleteJob(ctx, jobs[0].ID, "ok", "{}")
	if c, _ := f.do(t, "GET", path, m.AgentID, ""); c != http.StatusForbidden {
		t.Fatalf("after the job finished: %d", c)
	}
}

func TestAgentSoftwareRequiresBoundAgent(t *testing.T) {
	ctx := context.Background()
	f := newSWFixture(t)
	pkg, _ := f.sw.Create(ctx, model.SoftwarePackage{Name: "Office", StepsJSON: `[{"type":"exe","exe_path":"setup.exe"}]`})
	imgA, _ := f.images.Create(ctx, model.Image{Name: "A", SoftwareLinks: []model.ImageSoftwareLink{{PackageID: pkg.ID}}})
	imgB, _ := f.images.Create(ctx, model.Image{Name: "B"})
	m, _ := f.inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "usb-1"})
	body := func(img model.ID) string {
		return `{"image_id":` + strconv.FormatInt(int64(img), 10) + `}`
	}

	if c, _ := f.do(t, "POST", "/api/v1/agent/software", "", body(imgA.ID)); c != http.StatusForbidden {
		t.Fatalf("anonymous: %d", c)
	}
	if c, _ := f.do(t, "POST", "/api/v1/agent/software", m.AgentID, body(9999)); c != http.StatusForbidden {
		t.Fatalf("unknown image: %d", c)
	}
	if f.check.Allowed(ctx, m.ID, pkg.ID) {
		t.Fatal("unbound machine entitled to the package")
	}
	// First call from an unbound machine (USB/ISO first boot) binds it.
	c, out := f.do(t, "POST", "/api/v1/agent/software", m.AgentID, body(imgA.ID))
	if c != http.StatusOK || !strings.Contains(out, "Office") {
		t.Fatalf("unbound machine: %d %s", c, out)
	}
	if b, _ := f.inv.GetBinding(ctx, m.ID); b.ImageID == nil || *b.ImageID != imgA.ID {
		t.Fatalf("machine not bound: %+v", b)
	}
	if !f.check.Allowed(ctx, m.ID, pkg.ID) {
		t.Fatal("bound machine not entitled to its image's package")
	}
	// Bound to A: asking for B's set is refused and doesn't re-bind.
	if c, _ := f.do(t, "POST", "/api/v1/agent/software", m.AgentID, body(imgB.ID)); c != http.StatusForbidden {
		t.Fatalf("other image: %d", c)
	}
	if b, _ := f.inv.GetBinding(ctx, m.ID); *b.ImageID != imgA.ID {
		t.Fatal("binding changed by a refused request")
	}
}
