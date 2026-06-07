package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestReimageFlowResolvesLatestDefinitions verifies the Phase 9 contract:
// a machine with a binding pointing at an image gets a "reimage" menu
// option, and that option resolves to the CURRENT definition of that
// image (not a frozen snapshot at bind time).
func TestReimageFlowResolvesLatestDefinitions(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })

	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	loadouts := model.NewSoftwareLoadoutRepo(db)
	inv := model.NewInventoryRepo(db)

	users := auth.New(db)
	repos := Repos{
		ISOs: isos, Unattend: una, Drivers: drivers,
		Software: software, Loadouts: loadouts, Images: imgs,
		Inventory: inv, Users: users,
		Resolver: resolve.New(imgs, isos, una).WithDrivers(drivers).WithLoadouts(loadouts),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Create an authenticated client for operator endpoints.
	if _, err := users.CreateUser(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	loginResp, err := client.Post(srv.URL+"/api/v1/auth/login", "application/json",
		strings.NewReader(`{"username":"admin","password":"admin"}`))
	if err != nil {
		t.Fatal(err)
	}
	loginResp.Body.Close()

	// Initial ISO and image.
	iso1, _ := isos.Create(ctx, model.ISO{Name: "Win11-original", OSType: "windows-11"})
	iso1ID := iso1.ID
	img, _ := imgs.Create(ctx, model.Image{Name: "lab", ISOID: &iso1ID})

	// First boot from a lab machine: menu, no reimage (no binding yet).
	body, _ := json.Marshal(map[string]any{
		"system_uuid":         "u-aaaa",
		"system_manufacturer": "Dell Inc.",
	})
	resp, err := http.Post(srv.URL+"/api/v1/clients/menu", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), `"reimage"`) {
		t.Errorf("reimage should not appear without a binding: %s", b)
	}

	// Operator binds the machine to the image.
	machine, _ := inv.GetByUUID(ctx, "u-aaaa")
	imgID := img.ID
	bindBody, _ := json.Marshal(model.MachineBinding{
		ImageID: &imgID, MachineName: "LAB-01",
	})
	resp, _ = http.Post(srv.URL+"/api/v1/machines/"+itoa(machine.ID)+"/binding",
		"application/json", bytes.NewReader(bindBody))
	resp.Body.Close()
	// Note: route requires PUT, but we want to update — recreate via PUT.
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/api/v1/machines/"+itoa(machine.ID)+"/binding", bytes.NewReader(bindBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Second boot: menu now offers reimage.
	resp, _ = http.Post(srv.URL+"/api/v1/clients/menu", "application/json", bytes.NewReader(body))
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"reimage"`) {
		t.Fatalf("expected reimage option after binding, got: %s", b)
	}

	// Operator updates the image to point at a fresh ISO.
	iso2, _ := isos.Create(ctx, model.ISO{Name: "Win11-updated", OSType: "windows-11"})
	iso2ID := iso2.ID
	img.ISOID = &iso2ID
	if err := imgs.Update(ctx, img); err != nil {
		t.Fatal(err)
	}

	// Resolve the (still-bound) image now: should reflect the LATEST
	// definition (Win11-updated), not the original. This is the design's
	// "re-imaging is to the latest, not the bound snapshot" rule.
	resp, _ = client.Get(srv.URL + "/api/v1/images/" + itoa(img.ID) + "/resolved")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "Win11-updated") {
		t.Errorf("resolve must use latest ISO, got: %s", b)
	}
	if strings.Contains(string(b), "Win11-original") {
		t.Errorf("resolve must not include the old ISO: %s", b)
	}
}

func itoa(id model.ID) string {
	b, _ := json.Marshal(id)
	return strings.Trim(string(b), `"`)
}
