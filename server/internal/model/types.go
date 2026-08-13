// Package model defines the domain types and the repository operations that
// the API and portal layers call. Storage is SQL; this package does the
// translation and enforces the cross-cutting invariants (cycle prevention,
// reference-count guards, name uniqueness).
package model

import (
	"strings"
	"time"
)

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

	// Boot-media prep status (descriptive; set by PrepareBootMedia at
	// ingest, reported by the portal). See migration 0010.
	InstallImageFormat string     `json:"install_image_format"` // wim|esd|swm|""
	InstallImageBytes  int64      `json:"install_image_bytes"`  // size before any split
	SWMParts           int        `json:"swm_parts"`            // 0 = not split
	BootloaderPresent  bool       `json:"bootloader_present"`   // efi/boot/bootx64.efi found
	PrepError          string     `json:"prep_error,omitempty"` // non-empty => not deploy-ready
	MediaPreparedAt    *time.Time `json:"media_prepared_at,omitempty"`

	// Media summary (descriptive): the whole extracted ISO tree, so the
	// portal can show the full media was ingested, not just the install
	// image. See migration 0011.
	MediaFileCount  int   `json:"media_file_count"`
	MediaTotalBytes int64 `json:"media_total_bytes"`
	BootWimPresent  bool  `json:"boot_wim_present"` // sources/boot.wim (WinPE)
	// Editions are the Windows edition names (WIM image names) inside the
	// install image, enumerated at prep time. The portal shows them so an
	// operator knows which /IMAGE/NAME an unattend can target. See
	// migration 0016.
	Editions []string `json:"editions"`
}

// Extracted reports whether the ISO has been unpacked into its files/
// tree (storage_path points inside it).
func (i ISO) Extracted() bool {
	return strings.Contains(i.StoragePath, "/files/")
}

// DeployReady reports whether the ISO can be staged as boot media: it
// has been extracted, carries a UEFI bootloader, and prepare hit no
// error. The portal gates deploy selection on this.
func (i ISO) DeployReady() bool {
	return i.Extracted() && i.BootloaderPresent && i.PrepError == ""
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
	ID          ID     `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	StoragePath string `json:"storage_path"`
	// PayloadFilename is the original filename the operator uploaded
	// (e.g. "office-365-installer.exe"). Stored separately so the
	// portal can show something meaningful when the on-disk blob has
	// been renamed to a canonical name. Empty for pre-existing rows
	// uploaded before this column existed.
	PayloadFilename string `json:"payload_filename"`
	SizeBytes       int64  `json:"size_bytes"`
	DetectionJSON   string `json:"detection_json"`
	StepsJSON       string `json:"steps_json"`
	// DependsOn lists other package IDs this package requires. Resolving a
	// package auto-includes its dependencies (transitively) and installs
	// them first. See SoftwarePackageRepo.ResolveOrder.
	DependsOn []ID `json:"depends_on"`
	// SucceedsID, when set, is the package this one supersedes. During
	// resolve, if both this package and the package it succeeds end up in a
	// machine's install set, the superseded (older) package is dropped so
	// only the successor installs (transitively, following the succeeds
	// chain). nil supersedes nothing. See SoftwarePackageRepo.ResolveOrder.
	SucceedsID *ID       `json:"succeeds_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ImageGroup is a named bucket for organising images on the PXE boot menu.
// Each image may belong to at most one group. Groups are purely presentational
// — no inheritance or filtering — and are managed from the Images portal page.
type ImageGroup struct {
	ID          ID        `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
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
	GroupID       *ID                 `json:"group_id,omitempty"`
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

// Machine-group kinds. Both resolve to a flat machine set the same way for
// every consumer; the kind only changes how membership is authored.
const (
	MachineGroupManual  = "manual"
	MachineGroupDynamic = "dynamic"
)

// MachineGroup is an AutoDeploy-local named set of machines, independent of
// Active Directory. A manual group has explicit machine members and may nest
// child groups; a dynamic group is defined by Filter, evaluated against current
// inventory at read time. Children lets groups NEST: a group's resolved
// membership includes the resolved members of each child group (cycles are
// prevented in the repository). MemberCount is derived (not persisted) and
// populated by ListWithCounts for list/sidebar rendering.
type MachineGroup struct {
	ID          ID            `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Kind        string        `json:"kind"`
	Filter      MachineFilter `json:"filter"`
	Children    []ID          `json:"children,omitempty"`
	MemberCount int           `json:"member_count"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// MachineFilter is the dynamic-group selection, also reusable anywhere a "match
// these machines" predicate is needed. Empty fields are ignored; all set fields
// AND together. NameRegex matches the machine's display name (reported name,
// binding name fallback) case-insensitively; OS is a case-insensitive substring
// of the reported OS caption; OU matches the desired or observed AD location
// (exact, or the OU and everything beneath it when OUSubtree); MemberOf tests AD
// group membership.
type MachineFilter struct {
	NameRegex string `json:"name_regex,omitempty"`
	OS        string `json:"os,omitempty"`
	OU        string `json:"ou,omitempty"`
	OUSubtree bool   `json:"ou_subtree,omitempty"`
	MemberOf  string `json:"member_of,omitempty"`
}
