// Package unattend turns a structured Unattend settings document into a
// complete Windows unattend.xml answer file. Operators configure the
// settings in the portal; this package owns the XML shape so the rest of
// the system never has to deal with the Microsoft schema directly.
//
// Resolution rule (from the design): unattend is NEAREST-WINS and USED IN
// FULL. There is no merging up the image chain. Each Unattend row must be
// complete on its own; a "base" unattend is a template that is cloned and
// customised.
//
// Security: every password field (local accounts, auto-logon, domain join)
// is treated as a secret. Values live in the row's settings_json so the
// generator can emit them into the XML (Windows needs them) but never
// appear in any log line. CONVENTIONS.md §4 enforces the rule statically.
package unattend

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Settings is the operator-facing unattend configuration. JSON-shaped to
// match what the portal stores on the unattend row.
type Settings struct {
	// Target OS — drives generator behaviour. windows-11 enables the OOBE
	// online-account bypass (BypassNRO) and exposes the optional Windows-
	// 11-requirements bypass for unsupported hardware (TPM/Secure
	// Boot/RAM/CPU/Storage). Older Windows generators do not emit those.
	TargetOS string `json:"target_os"` // "windows-11" | "windows-10" | "windows-server-2022" | …

	// --- Regional ---
	Locale     string `json:"locale"`
	UILanguage string `json:"ui_language"`
	Keyboard   string `json:"keyboard"`
	TimeZone   string `json:"time_zone"`

	// --- OS edition ---
	Edition    string `json:"edition"`
	ProductKey string `json:"product_key"` // legacy single-edition key
	// SkipProductKey emits an empty <ProductKey> with a no-UI flag so Setup
	// does not stop on the "enter your product key" screen when no key is
	// supplied (e.g. volume/KMS or "I'll activate later" deployments).
	SkipProductKey bool `json:"skip_product_key"`

	// --- Licensing (new in this catalog expansion) ---
	// KMSServer activates the OS against a private KMS host instead of
	// Microsoft Activation Servers. Empty leaves Windows on its default
	// activation path.
	KMSServer string `json:"kms_server,omitempty"`
	KMSPort   int    `json:"kms_port,omitempty"` // default 1688
	// AVMAKey is the Automatic Virtual Machine Activation key — set this
	// on Server VMs whose Hyper-V host is properly licensed and AVMA
	// kicks in transparently.
	AVMAKey string `json:"avma_key,omitempty"`
	// SkipAutoActivation suppresses Windows' first-boot activation
	// attempt. Use when you intend to activate later via slmgr.
	SkipAutoActivation bool `json:"skip_auto_activation,omitempty"`

	// AdditionalActivations is a list of supplementary product keys to
	// install during the specialize pass (e.g. Windows 10 ESU, Server
	// CALs). Each runs slmgr.vbs /ipk and optionally /ato.
	AdditionalActivations []AdditionalActivation `json:"additional_activations,omitempty"`

	// --- Local accounts ---
	// LocalAccounts is the list of local accounts to create. Each has a
	// group (Administrators or Users), name, password (secret), display
	// name and description. The portal seeds this from the legacy
	// AdminUser/AdminPassword fields on first edit if it's empty so old
	// unattend rows keep working.
	LocalAccounts []LocalAccount `json:"local_accounts,omitempty"`

	// Legacy single-admin fields. New unattends should use LocalAccounts
	// instead, but these stay for backwards compatibility with existing
	// rows; the generator collapses them into LocalAccounts at emit time.
	AdminUser     string `json:"admin_user,omitempty"`
	AdminPassword string `json:"admin_password,omitempty"`
	HideAdmin     bool   `json:"hide_admin,omitempty"`

	// --- Auto-logon ---
	// AutoLogon, if set, logs the named account in automatically for N
	// boots. Useful for post-imaging setup that needs a user session
	// before the operator/user takes over.
	AutoLogon *AutoLogonSetting `json:"auto_logon,omitempty"`

	// --- Naming ---
	// NameTemplate is the computer-name template with placeholders expanded
	// server-side per machine: %random(N)%, %serial%, %serial(N)%, %uuid%,
	// %uuid(N)%, %agent%/%agent(N)%, plus literal text. Empty falls back to
	// a fully-random Windows name. See unattend.ExpandName.
	NameTemplate string `json:"name_template"`
	// NameStrategy/ComputerName are the legacy naming fields, kept so old
	// unattend rows still parse; Parse() migrates them into NameTemplate.
	NameStrategy string `json:"name_strategy,omitempty"` // "literal" | "prefix" | "random"
	ComputerName string `json:"computer_name,omitempty"`
	// NameIdentity is the per-machine identity used to expand NameTemplate
	// placeholders. It is NOT persisted (json:"-"); serve.go fills it from
	// the machine record before generation. Empty in the portal preview.
	NameIdentity NameIdentity `json:"-"`

	// ServerURL is the reachable AutoDeploy base URL (scheme+host) that the
	// install-status callback posts to. It is NOT persisted (json:"-");
	// serve.go fills it from the incoming request before generation. Empty
	// in the portal preview, which suppresses the callback.
	ServerURL string `json:"-"`
	// CallbackUUID is the SMBIOS UUID the install-status callback reports as
	// its identity. NOT persisted (json:"-"); serve.go fills it from the
	// ?uuid= query param. Empty in the portal preview.
	CallbackUUID string `json:"-"`

	// --- Disk ---
	DiskID int `json:"disk_id"`

	// --- OOBE ---
	SkipMachineOOBE          bool `json:"skip_machine_oobe"`
	SkipUserOOBE             bool `json:"skip_user_oobe"`
	HideEULA                 bool `json:"hide_eula"`
	HideOEMRegistration      bool `json:"hide_oem_registration"`
	HideOnlineAccountScreens bool `json:"hide_online_account_screens"`
	HideWirelessSetup        bool `json:"hide_wireless_setup"`
	ProtectYourPC            int  `json:"protect_your_pc"`

	// --- Windows 11 specifics ---
	BypassNRO       bool `json:"bypass_nro"`
	BypassWin11Reqs bool `json:"bypass_win11_reqs"`

	// --- Windows policies ---
	// Set the system telemetry level. 0=Security, 1=Basic, 2=Enhanced,
	// 3=Full. Set via the AllowTelemetry registry policy. -1 leaves the
	// system default untouched.
	TelemetryLevel int `json:"telemetry_level,omitempty"`
	// Windows Update behaviour.
	DisableWindowsUpdate    bool `json:"disable_windows_update,omitempty"`
	DeferFeatureUpdatesDays int  `json:"defer_feature_updates_days,omitempty"`
	DeferQualityUpdatesDays int  `json:"defer_quality_updates_days,omitempty"`
	// Networking / remote access.
	EnableRDP   bool `json:"enable_rdp,omitempty"`
	AllowICMPv4 bool `json:"allow_icmpv4,omitempty"`
	// Power: set the active power scheme. "high" sets High Performance.
	// Empty leaves the default ("balanced").
	PowerScheme string `json:"power_scheme,omitempty"`

	// --- Active Directory ---
	DomainJoin *DomainJoin `json:"domain_join,omitempty"`

	// --- Commands ---
	// FirstLogonCommands run after the first user logs in. They have a
	// user session so they can interact with the profile.
	FirstLogonCommands []FirstLogonCommand `json:"first_logon_commands,omitempty"`
	// SetupCompleteCommands run BEFORE OOBE finishes (writes a script
	// that Windows executes from %WINDIR%\Setup\Scripts\SetupComplete.cmd).
	// They have no user session — use them for system-level work that
	// must complete before the first user logon.
	SetupCompleteCommands []SetupCompleteCommand `json:"setup_complete_commands,omitempty"`
	// SpecializeCommands are RunSynchronousCommand entries injected into
	// the specialize pass — run BEFORE the OOBE pages even render. Use
	// for environment prep (registry tweaks, driver injection helpers).
	SpecializeCommands []SpecializeCommand `json:"specialize_commands,omitempty"`
}

// AdditionalActivation is a supplementary product key installed via
// slmgr.vbs during the specialize pass. The optional ActivationID is
// needed for products like Windows 10 ESU that require /ato with a
// specific activation GUID.
type AdditionalActivation struct {
	Label        string `json:"label"`                   // e.g. "Windows 10 ESU Year 1"
	ProductKey   string `json:"product_key"`             // XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
	ActivationID string `json:"activation_id,omitempty"` // GUID for /ato; empty = skip
}

// LocalAccount is a single user account to create on the deployed
// machine.
type LocalAccount struct {
	Name        string `json:"name"`
	Password    string `json:"password"`
	Group       string `json:"group"` // "Administrators" or "Users"
	FullName    string `json:"full_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// AutoLogonSetting is the unattend's <AutoLogon> block.
type AutoLogonSetting struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Count    int    `json:"count"` // 1 - 9999; how many logons to auto-fire
}

// DomainJoin is the AD join section. Credentials are short-lived and high-
// value; treat them as secrets.
type DomainJoin struct {
	Domain       string `json:"domain"`
	OU           string `json:"ou"`
	JoinUser     string `json:"join_user"`
	JoinPassword string `json:"join_password"`
}

// FirstLogonCommand is one item in the FirstLogonCommands sequence.
type FirstLogonCommand struct {
	Order       int    `json:"order"`
	Description string `json:"description"`
	CommandLine string `json:"command_line"`
}

// SetupCompleteCommand is one line in the generated SetupComplete.cmd.
type SetupCompleteCommand struct {
	Order       int    `json:"order"`
	Description string `json:"description"`
	CommandLine string `json:"command_line"`
}

// SpecializeCommand is a RunSynchronousCommand in the specialize pass.
type SpecializeCommand struct {
	Order       int    `json:"order"`
	Description string `json:"description"`
	CommandLine string `json:"command_line"`
}

// Parse decodes the operator-stored JSON. Empty input yields a defaulted
// Settings so an unconfigured unattend still generates a sensible XML.
func Parse(raw string) (Settings, error) {
	s := Defaults()
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return s, nil
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return Settings{}, fmt.Errorf("invalid unattend settings JSON: %w", err)
	}
	if s.TargetOS == "" {
		s.TargetOS = "windows-11"
	}
	if s.Locale == "" {
		s.Locale = "en-US"
	}
	if s.UILanguage == "" {
		s.UILanguage = s.Locale
	}
	if s.Keyboard == "" {
		s.Keyboard = "0409:00000409"
	}
	if s.TimeZone == "" {
		s.TimeZone = "GMT Standard Time"
	}
	// Migrate the legacy naming fields into NameTemplate so old rows keep
	// working. literal -> the name verbatim; prefix -> "<name>%random(4)%"
	// (a concrete random suffix, NOT the invalid "<name>*"); random/empty
	// -> empty template (= fully-random Windows name). Only migrate when
	// NameTemplate isn't already set.
	if s.NameTemplate == "" {
		switch s.NameStrategy {
		case "literal":
			s.NameTemplate = s.ComputerName
		case "prefix":
			s.NameTemplate = s.ComputerName + "%random(4)%"
		}
	}
	// Legacy fields are fully captured by NameTemplate now; clear them so
	// the row re-serialises in the new shape.
	s.NameStrategy = ""
	s.ComputerName = ""
	if s.ProtectYourPC == 0 {
		s.ProtectYourPC = 3
	}
	if s.KMSPort == 0 && s.KMSServer != "" {
		s.KMSPort = 1688
	}
	if s.TelemetryLevel == 0 {
		// Treat 0 as "untouched" UNLESS the operator explicitly stored
		// the value (the JSON had the key). We can't distinguish here
		// without a *int; for now, 0 means "do not emit any telemetry
		// policy" which matches the historical behaviour. -1 in the
		// portal form represents the same.
	}
	// Collapse the legacy admin fields into a LocalAccounts entry so
	// the generator only has one path. If LocalAccounts already
	// contains entries, the legacy values are ignored.
	if len(s.LocalAccounts) == 0 && s.AdminUser != "" {
		s.LocalAccounts = []LocalAccount{{
			Name:     s.AdminUser,
			Password: s.AdminPassword,
			Group:    "Administrators",
		}}
	}
	return s, nil
}

// Defaults returns a sensible baseline Settings suitable for a development
// lab build: en-US, skip OOBE, no domain, install the agent on first boot,
// one Administrator account.
func Defaults() Settings {
	return Settings{
		TargetOS:                 "windows-11",
		Locale:                   "en-US",
		UILanguage:               "en-US",
		Keyboard:                 "0409:00000409",
		TimeZone:                 "GMT Standard Time",
		Edition:                  "Windows 11 Pro",
		NameTemplate:             "", // empty = fully-random Windows name
		SkipMachineOOBE:          true,
		SkipUserOOBE:             true,
		HideEULA:                 true,
		HideOEMRegistration:      true,
		HideOnlineAccountScreens: true,
		HideWirelessSetup:        true,
		ProtectYourPC:            3,
		BypassNRO:                true,
		// LocalAccounts is intentionally empty here. The portal seeds a
		// default Administrator entry on first edit; Parse migrates
		// legacy admin_user/admin_password rows. Keeping the default
		// empty lets that migration distinguish "operator chose nothing"
		// from "operator inherited the default".
	}
}
