package payload

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestServeUnattendInjectsBindingIdentity verifies that
// /payload/unattend/{id}?uuid=... layers the bound machine name
// onto the resolved unattend. The shared catalog says "random"; the
// binding says "LAB-07"; the served XML should reflect the binding.
func TestServeUnattendInjectsBindingIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	blobs, err := storage.NewBlobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	isos := model.NewISORepo(db)
	unattendRepo := model.NewUnattendRepo(db)
	images := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)

	// Seed: an unattend with NameStrategy=random + no computer
	// name, joined to an image, and a machine bound to LAB-07.
	ua, err := unattendRepo.Create(ctx, model.Unattend{
		Name:         "win11-base",
		SettingsJSON: `{"target_os":"windows-11","name_strategy":"random"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	img, err := images.Create(ctx, model.Image{
		Name: "Win11", UnattendID: &ua.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	m, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "uuid-lab07"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpsertBinding(ctx, model.MachineBinding{
		MachineID:   m.ID,
		MachineName: "LAB-07",
		TargetOU:    "OU=Lab,DC=corp,DC=example",
		ImageID:     &img.ID,
	}); err != nil {
		t.Fatal(err)
	}

	svc := &Service{
		Blobs:     blobs,
		ISOs:      isos,
		Resolver:  resolve.New(images, isos, unattendRepo),
		Inventory: inv,
	}
	mux := http.NewServeMux()
	svc.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Without uuid -> generic XML (ComputerName=*).
	resp, err := http.Get(srv.URL + "/payload/unattend/" + idStr2(img.ID))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<ComputerName>*</ComputerName>") {
		t.Errorf("expected wildcard ComputerName, got:\n%s", body)
	}

	// With uuid -> binding's LAB-07 wins.
	resp, err = http.Get(srv.URL + "/payload/unattend/" + idStr2(img.ID) + "?uuid=uuid-lab07")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<ComputerName>LAB-07</ComputerName>") {
		t.Errorf("expected LAB-07 ComputerName from binding, got:\n%s", body)
	}
}

// idStr2 is a tiny int64 to string helper local to the test (the
// production helper lives in serve.go but isn't exported).
func idStr2(id model.ID) string {
	var buf [20]byte
	i := len(buf)
	n := int64(id)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
