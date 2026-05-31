package match

import "testing"

func TestFilterMatchesSystemSKU(t *testing.T) {
	id := Identity{SystemManufacturer: "Dell Inc.", SystemProduct: "Latitude 5520", SystemSKU: "0A1B"}
	if !(Filter{"system_sku": "0A1B"}).Matches(id) {
		t.Error("system_sku should match")
	}
	if (Filter{"system_sku": "ZZZZ"}).Matches(id) {
		t.Error("wrong system_sku should not match")
	}
	// system_sku must be an allowed filter key (else ParseFilter rejects it).
	if _, err := ParseFilter(`{"system_sku":"0A1B","system_family":"Latitude"}`); err != nil {
		t.Errorf("system_sku/system_family should be allowed keys: %v", err)
	}
}
