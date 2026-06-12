package portal

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// TestBuildDriverFromFormGroupsRepeatedKey covers the "three models share one
// driver package" case: a single filter block lists the same field (Model)
// three times. Previously a map collapsed these to the last value; now they
// are grouped into one constraint with several acceptable values.
func TestBuildDriverFromFormGroupsRepeatedKey(t *testing.T) {
	form := strings.NewReader(strings.Join([]string{
		"name=Dell-Latitude",
		"filter_index[]=0",
		"filter_keys_0[]=system_manufacturer",
		"filter_vals_0[]=Dell+Inc.",
		"filter_keys_0[]=system_product",
		"filter_vals_0[]=Latitude+5520",
		"filter_keys_0[]=system_product",
		"filter_vals_0[]=Latitude+5530",
		"filter_keys_0[]=system_product",
		"filter_vals_0[]=Latitude+5540",
	}, "&"))
	req := httptest.NewRequest("POST", "/portal/drivers", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	pkg, err := buildDriverFromForm(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(pkg.Filters))
	}

	f, err := match.ParseFilter(pkg.Filters[0].FilterJSON)
	if err != nil {
		t.Fatalf("filter json should parse: %v (json=%s)", err, pkg.Filters[0].FilterJSON)
	}
	if got := len(f["system_product"]); got != 3 {
		t.Errorf("expected 3 product values, got %d (json=%s)", got, pkg.Filters[0].FilterJSON)
	}

	// All three models, with the right manufacturer, must match.
	for _, p := range []string{"Latitude 5520", "Latitude 5530", "Latitude 5540"} {
		id := match.Identity{SystemManufacturer: "Dell Inc.", SystemProduct: p}
		if !f.Matches(id) {
			t.Errorf("expected match for %q", p)
		}
	}
}

// TestDriverFormRenderBoardFilterHelper: the "use a known machine as a
// filter" helper must expose each machine's base-board identity (so a
// filter can be seeded from board_manufacturer/board_product, not just
// system make + model) alongside the "Match on" source selector, and the
// preview form must offer every identity field driver filters can match,
// base board included.
func TestDriverFormRenderBoardFilterHelper(t *testing.T) {
	funcs := template.FuncMap{
		"int64": func(id model.ID) int64 { return int64(id) },
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/driver_form.html")
	if err != nil {
		t.Fatalf("parse driver_form.html: %v", err)
	}
	var buf bytes.Buffer
	err = tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": map[string]any{
		"Driver":      model.DriverPackage{ID: 1, Name: "lab"},
		"Filters":     []uiFilter{{ID: 1, Pairs: []uiPair{{Key: "board_product", Value: "0K240Y"}}}},
		"AllowedKeys": allowedKeysList(),
		"IsNew":       false,
		"Machines": []machineFilterChoice{{
			ID: 7, Label: "Dell Inc. OptiPlex 7090",
			Manufacturer: "Dell Inc.", Product: "OptiPlex 7090",
			BoardManufacturer: "Dell Inc.", BoardProduct: "0K240Y",
		}},
	}})
	if err != nil {
		t.Fatalf("exec driver_form.html: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`data-board-manufacturer="Dell Inc."`,
		`data-board-product="0K240Y"`,
		`id="machine-filter-source"`,
		`Base board make + product`,
		`name="p_board_product"`,
		`name="p_board_serial"`,
		`name="p_system_sku"`,
		`name="p_system_family"`,
		`name="p_bios_vendor"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("driver form missing %q", want)
		}
	}
	// The filter-key dropdown itself must offer the base-board product.
	if !strings.Contains(out, `value="board_product"`) {
		t.Error("filter key dropdown missing board_product")
	}
}

// TestBuildDriverFromFormMultipleFilters confirms several independent filter
// blocks all survive the round-trip (OR across filters).
func TestBuildDriverFromFormMultipleFilters(t *testing.T) {
	form := strings.NewReader(strings.Join([]string{
		"name=mixed",
		"filter_index[]=0",
		"filter_keys_0[]=system_manufacturer",
		"filter_vals_0[]=Dell+Inc.",
		"filter_index[]=1",
		"filter_keys_1[]=system_manufacturer",
		"filter_vals_1[]=HP",
	}, "&"))
	req := httptest.NewRequest("POST", "/portal/drivers", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	pkg, err := buildDriverFromForm(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkg.Filters) != 2 {
		t.Fatalf("expected 2 filters, got %d", len(pkg.Filters))
	}
}
