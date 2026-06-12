package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// TestBootMenuStoresFullIdentity: the menu boot is usually a machine's first
// contact with the server, and the Boot Client now posts its complete SMBIOS
// identity there. The inventory record must carry the base-board (and BIOS /
// SKU / family) facts from that first boot so an operator can build a driver
// filter on e.g. board_product before the machine has ever been deployed.
func TestBootMenuStoresFullIdentity(t *testing.T) {
	ctx := context.Background()
	db, _ := storage.Open(ctx, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	isos := model.NewISORepo(db)
	una := model.NewUnattendRepo(db)
	imgs := model.NewImageRepo(db)
	inv := model.NewInventoryRepo(db)
	repos := Repos{
		ISOs: isos, Unattend: una, Images: imgs, Inventory: inv,
		Resolver: resolve.New(imgs, isos, una),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"system_uuid":         "menu-full-1",
		"system_manufacturer": "Dell Inc.",
		"system_product":      "OptiPlex 7090",
		"system_serial":       "SER123",
		"system_sku":          "0A5E",
		"system_family":       "OptiPlex",
		"bios_vendor":         "Dell Inc.",
		"bios_version":        "1.2.3",
		"board_manufacturer":  "Dell Inc.",
		"board_product":       "0K240Y",
		"board_serial":        "BSER456",
	})
	resp, err := http.Post(srv.URL+"/api/v1/clients/menu", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("menu post: status %d", resp.StatusCode)
	}

	m, err := inv.GetByUUID(ctx, "menu-full-1")
	if err != nil {
		t.Fatalf("machine not upserted from menu: %v", err)
	}
	if m.BoardProduct != "0K240Y" {
		t.Errorf("board_product not stored from menu boot: got %q", m.BoardProduct)
	}
	if m.BoardManufacturer != "Dell Inc." || m.BoardSerial != "BSER456" {
		t.Errorf("board identity incomplete: %+v", m)
	}
	if m.BIOSVendor != "Dell Inc." || m.BIOSVersion != "1.2.3" {
		t.Errorf("bios identity incomplete: %+v", m)
	}

	// Older boot clients post only the original four fields. That must
	// still decode, and the absent fields must NOT wipe the stored ones.
	legacy, _ := json.Marshal(map[string]any{
		"system_uuid":         "menu-full-1",
		"system_manufacturer": "Dell Inc.",
		"system_product":      "OptiPlex 7090",
		"system_serial":       "SER123",
	})
	resp2, err := http.Post(srv.URL+"/api/v1/clients/menu", "application/json", bytes.NewReader(legacy))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("legacy menu post: status %d", resp2.StatusCode)
	}
	m2, err := inv.GetByUUID(ctx, "menu-full-1")
	if err != nil {
		t.Fatal(err)
	}
	if m2.BoardProduct != "0K240Y" {
		t.Errorf("legacy menu post wiped board_product: got %q", m2.BoardProduct)
	}
}
