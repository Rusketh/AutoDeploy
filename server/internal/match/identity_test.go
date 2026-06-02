package match

import (
	"testing"
)

func TestEmptyFilterNeverMatches(t *testing.T) {
	f := Filter{}
	id := Identity{SystemManufacturer: "Dell Inc."}
	if f.Matches(id) {
		t.Error("empty filter must not match")
	}
}

func TestSingleKeyMatch(t *testing.T) {
	f := Filter{"system_manufacturer": {"Dell Inc."}}
	id := Identity{SystemManufacturer: "Dell Inc.", SystemProduct: "Latitude 5520"}
	if !f.Matches(id) {
		t.Error("expected match")
	}
	id.SystemManufacturer = "HP"
	if f.Matches(id) {
		t.Error("expected no match")
	}
}

func TestCaseInsensitive(t *testing.T) {
	f := Filter{"system_manufacturer": {"dell inc."}}
	id := Identity{SystemManufacturer: "DELL INC."}
	if !f.Matches(id) {
		t.Error("case-insensitive compare failed")
	}
}

func TestAllConstraintsRequired(t *testing.T) {
	f := Filter{
		"system_manufacturer": {"Dell Inc."},
		"system_product":      {"Latitude 5520"},
	}
	id := Identity{SystemManufacturer: "Dell Inc.", SystemProduct: "OptiPlex 7090"}
	if f.Matches(id) {
		t.Error("partial match must not be a match")
	}
}

func TestWildcardValue(t *testing.T) {
	f := Filter{"system_manufacturer": {"*"}}
	id := Identity{SystemManufacturer: "Dell Inc."}
	if !f.Matches(id) {
		t.Error("wildcard should match any non-empty value")
	}
	id.SystemManufacturer = ""
	if f.Matches(id) {
		t.Error("wildcard should not match empty value")
	}
}

// TestMultipleValuesPerKey covers the "several models share one driver
// package" case: one filter lists three products and matches any of them
// (OR within a key) while still requiring the manufacturer (AND across keys).
func TestMultipleValuesPerKey(t *testing.T) {
	f, err := ParseFilter(`{"system_manufacturer":"Dell Inc.","system_product":["Latitude 5520","Latitude 5530","Latitude 5540"]}`)
	if err != nil {
		t.Fatal(err)
	}
	base := Identity{SystemManufacturer: "Dell Inc."}
	for _, p := range []string{"Latitude 5520", "Latitude 5530", "Latitude 5540"} {
		id := base
		id.SystemProduct = p
		if !f.Matches(id) {
			t.Errorf("expected match for product %q", p)
		}
	}
	// A product not in the list must not match.
	miss := base
	miss.SystemProduct = "OptiPlex 7090"
	if f.Matches(miss) {
		t.Error("product outside the list must not match")
	}
	// Right product but wrong manufacturer must not match (AND across keys).
	wrongMake := Identity{SystemManufacturer: "HP", SystemProduct: "Latitude 5520"}
	if f.Matches(wrongMake) {
		t.Error("wrong manufacturer must not match")
	}
}

// TestParseFilterArrayAndStringEquivalent confirms a single string and a
// one-element array decode to the same single-value filter.
func TestParseFilterArrayAndStringEquivalent(t *testing.T) {
	a, err := ParseFilter(`{"system_product":"Latitude 5520"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseFilter(`{"system_product":["Latitude 5520"]}`)
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{SystemProduct: "Latitude 5520"}
	if !a.Matches(id) || !b.Matches(id) {
		t.Error("string and one-element array forms should both match")
	}
}

func TestParseFilterRejectsUnknownKey(t *testing.T) {
	_, err := ParseFilter(`{"systme_manfacturer":"Dell"}`)
	if err == nil {
		t.Error("expected error on unknown key")
	}
}

func TestParseFilterValid(t *testing.T) {
	f, err := ParseFilter(`{"system_manufacturer":"Dell Inc.","system_product":"Latitude 5520"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 2 {
		t.Errorf("got %d keys, want 2", len(f))
	}
}
