// Package match evaluates driver-package SMBIOS filters against a reported
// hardware identity. Matching is global — a driver package applies to ANY
// image's deployment on a machine whose SMBIOS matches one of the package's
// filters. Image inheritance does not scope drivers (per the design doc).
//
// Match semantics:
//   - A DriverPackage carries a list of Filters.
//   - The package applies when ANY of its filters matches the reported
//     hardware (logical OR across filters).
//   - A Filter matches when EVERY one of its key/value constraints matches
//     the reported hardware (logical AND within a filter).
//   - An empty filter (no constraints) matches NOTHING. This is deliberate:
//     the design calls out that filter specificity matters, and an
//     accidental "matches everything" would inject drivers everywhere.
//   - String comparison is case-insensitive on the value, exact on the key.
//   - A "*" or "" value matches any non-empty reported value for that
//     attribute. Use sparingly — that is the only wildcard.
package match

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Identity mirrors the SMBIOS-derived facts the Boot Client reports. Field
// names match the JSON keys the Boot Client posts and the keys allowed in
// driver filters.
type Identity struct {
	SystemManufacturer string `json:"system_manufacturer"`
	SystemProduct      string `json:"system_product"`
	SystemSerial       string `json:"system_serial"`
	SystemUUID         string `json:"system_uuid"`
	SystemSKU          string `json:"system_sku"`
	SystemFamily       string `json:"system_family"`
	BIOSVendor         string `json:"bios_vendor"`
	BIOSVersion        string `json:"bios_version"`
	BoardManufacturer  string `json:"board_manufacturer"`
	BoardProduct       string `json:"board_product"`
	BoardSerial        string `json:"board_serial"`
}

// AllowedKeys is the set of attribute names a filter may reference. Anything
// else is rejected by ParseFilter so a typo cannot silently never-match.
var AllowedKeys = map[string]bool{
	"system_manufacturer": true,
	"system_product":      true,
	"system_serial":       true,
	"system_uuid":         true,
	"system_sku":          true,
	"system_family":       true,
	"bios_vendor":         true,
	"bios_version":        true,
	"board_manufacturer":  true,
	"board_product":       true,
	"board_serial":        true,
}

// Filter is one matching predicate: a map of allowed SMBIOS keys to required
// values. Empty filter matches nothing.
type Filter map[string]string

// ParseFilter decodes a JSON object into a Filter and validates its keys.
func ParseFilter(raw string) (Filter, error) {
	if strings.TrimSpace(raw) == "" {
		return Filter{}, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("invalid filter JSON: %w", err)
	}
	for k := range m {
		if !AllowedKeys[k] {
			return nil, fmt.Errorf("unknown filter key %q (allowed: %v)", k, allowedKeyList())
		}
	}
	return Filter(m), nil
}

func allowedKeyList() []string {
	out := make([]string, 0, len(AllowedKeys))
	for k := range AllowedKeys {
		out = append(out, k)
	}
	return out
}

// Matches reports whether id satisfies f. Empty filter never matches.
func (f Filter) Matches(id Identity) bool {
	if len(f) == 0 {
		return false
	}
	for k, want := range f {
		got := identityField(id, k)
		if got == "" {
			return false
		}
		if want == "" || want == "*" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
			return false
		}
	}
	return true
}

func identityField(id Identity, key string) string {
	switch key {
	case "system_manufacturer":
		return id.SystemManufacturer
	case "system_product":
		return id.SystemProduct
	case "system_serial":
		return id.SystemSerial
	case "system_uuid":
		return id.SystemUUID
	case "system_sku":
		return id.SystemSKU
	case "system_family":
		return id.SystemFamily
	case "bios_vendor":
		return id.BIOSVendor
	case "bios_version":
		return id.BIOSVersion
	case "board_manufacturer":
		return id.BoardManufacturer
	case "board_product":
		return id.BoardProduct
	case "board_serial":
		return id.BoardSerial
	}
	return ""
}
