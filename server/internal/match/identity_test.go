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
	f := Filter{"system_manufacturer": "Dell Inc."}
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
	f := Filter{"system_manufacturer": "dell inc."}
	id := Identity{SystemManufacturer: "DELL INC."}
	if !f.Matches(id) {
		t.Error("case-insensitive compare failed")
	}
}

func TestAllConstraintsRequired(t *testing.T) {
	f := Filter{
		"system_manufacturer": "Dell Inc.",
		"system_product":      "Latitude 5520",
	}
	id := Identity{SystemManufacturer: "Dell Inc.", SystemProduct: "OptiPlex 7090"}
	if f.Matches(id) {
		t.Error("partial match must not be a match")
	}
}

func TestWildcardValue(t *testing.T) {
	f := Filter{"system_manufacturer": "*"}
	id := Identity{SystemManufacturer: "Dell Inc."}
	if !f.Matches(id) {
		t.Error("wildcard should match any non-empty value")
	}
	id.SystemManufacturer = ""
	if f.Matches(id) {
		t.Error("wildcard should not match empty value")
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
