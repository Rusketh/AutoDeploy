// Package model defines the domain types and the repository operations that
// the API and portal layers call. Storage is SQL; this package does the
// translation and enforces the cross-cutting invariants (cycle prevention,
// reference-count guards, name uniqueness).
package model

import "time"

// ID is the primary-key type used by all artifact and image rows.
type ID int64

// ISO is uploaded OS media. On upload the server extracts contents so the
// WIM/ESD can be served individually over HTTP.
type ISO struct {
	ID          ID        `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OSType      string    `json:"os_type"`
	StoragePath string    `json:"storage_path"`
	SizeBytes   int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Unattend holds the configured settings from which a complete unattend.xml
// is generated. The settings shape is captured as raw JSON for now; the
// settings model lands in Phase 5.
type Unattend struct {
	ID           ID        `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	SettingsJSON string    `json:"settings_json"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DriverPackage models an ingested SCCM driver payload plus one or more
// SMBIOS filters. The filter expression is captured as JSON for now;
// structured matching lands in Phase 4.
type DriverPackage struct {
	ID          ID             `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	StoragePath string         `json:"storage_path"`
	SizeBytes   int64          `json:"size_bytes"`
	Filters     []DriverFilter `json:"filters"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// DriverFilter is one SMBIOS-attribute filter on a driver package. A package
// applies when ANY of its filters matches the reported hardware.
type DriverFilter struct {
	ID         ID     `json:"id"`
	FilterJSON string `json:"filter_json"`
}

// SoftwarePackage is an installable item modelled like an SCCM application:
// an installer payload, an ordered detection rule set, and an ordered
// install-step list. The rule and step shapes are captured as JSON for now;
// structured types land in Phase 6.
type SoftwarePackage struct {
	ID            ID        `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	StoragePath   string    `json:"storage_path"`
	// PayloadFilename is the original filename the operator uploaded
	// (e.g. "office-365-installer.exe"). Stored separately so the
	// portal can show something meaningful when the on-disk blob has
	// been renamed to a canonical name. Empty for pre-existing rows
	// uploaded before this column existed.
	PayloadFilename string    `json:"payload_filename"`
	SizeBytes       int64     `json:"size_bytes"`
	DetectionJSON   string    `json:"detection_json"`
	StepsJSON       string    `json:"steps_json"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Image is a composition object linking an ISO, an unattend and software,
// with an optional parent pointer for arbitrary-depth inheritance. Resolution
// rules (nearest-wins for ISO and unattend, additive union for software) are
// applied by the resolver in internal/resolve.
type Image struct {
	ID            ID                  `json:"id"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	ParentID      *ID                 `json:"parent_id,omitempty"`
	ISOID         *ID                 `json:"iso_id,omitempty"`
	UnattendID    *ID                 `json:"unattend_id,omitempty"`
	LoadoutID     *ID                 `json:"loadout_id,omitempty"`
	SoftwareLinks []ImageSoftwareLink `json:"software_links"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

// SoftwareLoadout is a named, ordered, inheritable collection of software
// packages. A child loadout inherits its parent's packages and may add
// more, override an inherited package's order, or opt out of an inherited
// package via Packages[].OptOut.
type SoftwareLoadout struct {
	ID          ID                       `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	ParentID    *ID                      `json:"parent_id,omitempty"`
	Packages    []SoftwareLoadoutPackage `json:"packages"`
	CreatedAt   time.Time                `json:"created_at"`
	UpdatedAt   time.Time                `json:"updated_at"`
}

// SoftwareLoadoutPackage is one package membership in a loadout. OptOut
// removes the package from the resolved set even if a parent loadout
// included it.
type SoftwareLoadoutPackage struct {
	PackageID  ID    `json:"package_id"`
	OrderValue int64 `json:"order_value"`
	OptOut     bool  `json:"opt_out,omitempty"`
}

// ImageSoftwareLink is one direct image -> software package link with an
// explicit order value. Software loadouts (Phase 7) add an additional source
// of links; the resolver unions both and de-duplicates by package ID, with
// direct-link ordering taking precedence.
type ImageSoftwareLink struct {
	PackageID  ID    `json:"package_id"`
	OrderValue int64 `json:"order_value"`
}
