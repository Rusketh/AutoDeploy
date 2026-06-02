package portal

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
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
