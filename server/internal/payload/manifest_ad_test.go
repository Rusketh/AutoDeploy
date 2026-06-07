package payload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/addomain"
	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestManifestPreparesADWhenUnattendDeclaresDomainJoin asserts that the
// manifest endpoint runs the Domain Integration Service before returning
// the manifest, when the resolved unattend has a domain_join section AND
// the machine has a binding with a name+OU.
func TestManifestPreparesADWhenUnattendDeclaresDomainJoin(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	_ = model.NewSoftwarePackageRepo(db) // not used in this test
	loadouts := model.NewSoftwareLoadoutRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)

	fakeDir := addomain.NewFakeDirectory()
	adSvc := &addomain.Service{Dir: fakeDir, Enabled: true}

	mh := &ManifestHandler{
		Resolver:  resolve.New(imgs, isos, una).WithDrivers(drivers).WithLoadouts(loadouts),
		AD:        adSvc,
		Inventory: inv,
		Unattend:  una,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Build the world: ISO, AD-enabled unattend, image.
	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	uaRow, _ := una.Create(ctx, model.Unattend{
		Name:         "ad-ua",
		SettingsJSON: `{"domain_join":{"domain":"corp.example","join_user":"joiner@corp.example","join_password":"secret-not-real","ou":"OU=Lab,DC=corp,DC=example"}}`,
	})
	isoID := iso.ID
	uaID := uaRow.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID, UnattendID: &uaID})

	// Machine and binding.
	machine, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u-aaaa"})
	imgID := img.ID
	if err := inv.UpsertBinding(ctx, model.MachineBinding{
		MachineID: machine.ID, ImageID: &imgID,
		MachineName: "LAB-01", TargetOU: "OU=Lab,DC=corp,DC=example",
		GroupMemberships: []string{"Lab-Computers"},
	}); err != nil {
		t.Fatal(err)
	}

	// Post identity at manifest endpoint.
	body, _ := json.Marshal(match.Identity{SystemUUID: "u-aaaa"})
	resp, _ := http.Post(srv.URL+"/api/v1/images/"+itoa(img.ID)+"/manifest",
		"application/json", bytes.NewReader(body))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", resp.StatusCode, b)
	}

	if len(fakeDir.Computers) != 1 {
		t.Fatalf("expected exactly one computer object created, got %+v", fakeDir.Computers)
	}
	for dn := range fakeDir.Computers {
		if dn != `CN=LAB-01,OU=Lab,DC=corp,DC=example` {
			t.Errorf("unexpected DN: %s", dn)
		}
	}
	if len(fakeDir.Groups["Lab-Computers"]) != 1 {
		t.Errorf("expected Lab-Computers membership, got %+v", fakeDir.Groups)
	}
}

// TestManifestPreparesADForAgentDrivenJoinWithImageOU is the regression
// guard for the "re-join fails with access denied" report: an image that
// uses agent-driven join (image_domain_join) configures the OU on the
// IMAGE, leaving the per-machine binding's TargetOU empty. prepareAD must
// still delete-and-replace the stale computer object using the image OU —
// previously it bailed out because TargetOU was empty, so the old object
// survived and the agent's re-join was denied.
func TestManifestPreparesADForAgentDrivenJoinWithImageOU(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	bx, err := secrets.Open("", filepath.Join(t.TempDir(), "k.bin"))
	if err != nil {
		t.Fatal(err)
	}
	dj := model.NewDomainJoinRepo(db, bx)

	fakeDir := addomain.NewFakeDirectory()
	// Seed a STALE computer object left behind by a previous deploy.
	const staleDN = `CN=LAB-09,OU=Lab,DC=corp,DC=example`
	fakeDir.Computers[staleDN] = "LAB-09"

	adSvc := &addomain.Service{Dir: fakeDir, Enabled: true}
	mh := &ManifestHandler{
		Resolver:   resolve.New(imgs, isos, una),
		AD:         adSvc,
		Inventory:  inv,
		Unattend:   una,
		DomainJoin: dj,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/images/{id}/manifest", mh.Handler())
	mux.HandleFunc("POST /api/v1/images/{id}/manifest", mh.Handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Plain unattend (NO legacy domain_join block): join is agent-driven.
	iso, _ := isos.Create(ctx, model.ISO{Name: "Win11", OSType: "windows-11"})
	uaRow, _ := una.Create(ctx, model.Unattend{Name: "plain-ua", SettingsJSON: `{}`})
	isoID := iso.ID
	uaID := uaRow.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &isoID, UnattendID: &uaID})

	// Image-level agent-driven join config carries the OU.
	if err := dj.Set(ctx, model.ImageDomainJoin{
		ImageID: img.ID, Enabled: true,
		Domain: "corp.example", OU: "OU=Lab,DC=corp,DC=example",
		JoinUser: "joiner@corp.example",
	}, "secret-not-real"); err != nil {
		t.Fatal(err)
	}

	// Binding has a name and group memberships but NO TargetOU (the
	// agent-driven default — the OU lives on the image config above).
	machine, _ := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u-bbbb"})
	imgID := img.ID
	if err := inv.UpsertBinding(ctx, model.MachineBinding{
		MachineID: machine.ID, ImageID: &imgID,
		MachineName:      "LAB-09",
		GroupMemberships: []string{"Lab-Computers"},
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(match.Identity{SystemUUID: "u-bbbb"})
	resp, _ := http.Post(srv.URL+"/api/v1/images/"+itoa(img.ID)+"/manifest",
		"application/json", bytes.NewReader(body))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest status=%d body=%s", resp.StatusCode, b)
	}

	// prepareAD must have run delete-and-replace using the IMAGE OU. The
	// decisive evidence is group reconciliation: SetGroupMemberships only
	// runs inside PrepareComputer, so if prepareAD had bailed (the bug) the
	// group would be empty and the stale object would be untouched.
	if got := len(fakeDir.Groups["Lab-Computers"]); got != 1 {
		t.Fatalf("expected Lab-Computers to have 1 member after prepare (proves PrepareComputer ran with the image OU), got %d: %+v", got, fakeDir.Groups)
	}
	if len(fakeDir.Computers) != 1 {
		t.Fatalf("expected exactly one computer object after prepare, got %+v", fakeDir.Computers)
	}
	if _, ok := fakeDir.Computers[staleDN]; !ok {
		t.Fatalf("expected recreated computer object at %s, got %+v", staleDN, fakeDir.Computers)
	}
}
