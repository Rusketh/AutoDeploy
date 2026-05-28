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
// Security: the local-administrator password is a secret. It is wrapped in
// logging.Secret throughout the in-memory model so it cannot leak through
// fmt.Sprintf or slog.String. The generated XML necessarily contains the
// value (that is the whole point of unattended setup); the server never
// logs it.
package unattend

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Settings is the operator-facing unattend configuration. JSON-shaped to
// match what the portal stores on the unattend row.
type Settings struct {
	// Regional.
	Locale     string `json:"locale"`         // "en-US"
	UILanguage string `json:"ui_language"`    // "en-US"
	Keyboard   string `json:"keyboard"`       // "0409:00000409"
	TimeZone   string `json:"time_zone"`      // "GMT Standard Time"

	// OS edition.
	Edition    string `json:"edition"`      // "Windows 11 Pro"
	ProductKey string `json:"product_key"`  // optional; KMS clients omit

	// Local administrator account.
	AdminUser     string `json:"admin_user"`             // "Administrator" by default
	AdminPassword string `json:"admin_password"`         // secret; never logged
	HideAdmin     bool   `json:"hide_admin"`             // skip OOBE local-account creation if AD-join is used

	// Computer naming.
	// "literal":   use ComputerName exactly
	// "prefix":    ComputerName + last-4 of system serial
	// "random":    "WIN-" + 7 random alphanumerics (the Windows default)
	NameStrategy string `json:"name_strategy"`
	ComputerName string `json:"computer_name"`

	// Disk layout. Boot Client already partitions; this controls the
	// settings inside the applied image (BCD, ESP letter). Phase 5 keeps
	// the defaults Windows expects.
	DiskID int `json:"disk_id"` // 0

	// OOBE.
	SkipMachineOOBE  bool `json:"skip_machine_oobe"`
	SkipUserOOBE     bool `json:"skip_user_oobe"`
	HideEULA         bool `json:"hide_eula"`
	HideOEMRegistration bool `json:"hide_oem_registration"`
	HideOnlineAccountScreens bool `json:"hide_online_account_screens"`
	HideWirelessSetup bool `json:"hide_wireless_setup"`
	ProtectYourPC    int  `json:"protect_your_pc"` // 1=express settings, 3=skip

	// Active Directory.
	DomainJoin    *DomainJoin `json:"domain_join,omitempty"`

	// First-logon commands run as Administrator (or NT AUTHORITY\SYSTEM via
	// SynchronousCommand) on first boot. The agent installer is appended
	// automatically by the generator; operator-provided commands run after
	// it.
	FirstLogonCommands []FirstLogonCommand `json:"first_logon_commands,omitempty"`
}

// DomainJoin is the AD join section. Credentials are short-lived and high-
// value; treat them as secrets.
type DomainJoin struct {
	Domain      string `json:"domain"`
	OU          string `json:"ou"`
	JoinUser    string `json:"join_user"`
	JoinPassword string `json:"join_password"` // secret; never logged
}

// FirstLogonCommand is one item in the FirstLogonCommands sequence.
type FirstLogonCommand struct {
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
	if s.AdminUser == "" {
		s.AdminUser = "Administrator"
	}
	if s.NameStrategy == "" {
		s.NameStrategy = "random"
	}
	if s.ProtectYourPC == 0 {
		s.ProtectYourPC = 3 // skip — operators can decide later
	}
	return s, nil
}

// Defaults returns a sensible baseline Settings suitable for a development
// lab build: en-US, skip OOBE, no domain, install the agent on first boot.
func Defaults() Settings {
	return Settings{
		Locale:                   "en-US",
		UILanguage:               "en-US",
		Keyboard:                 "0409:00000409",
		TimeZone:                 "GMT Standard Time",
		Edition:                  "Windows 11 Pro",
		AdminUser:                "Administrator",
		NameStrategy:             "random",
		SkipMachineOOBE:          true,
		SkipUserOOBE:             true,
		HideEULA:                 true,
		HideOEMRegistration:      true,
		HideOnlineAccountScreens: true,
		HideWirelessSetup:        true,
		ProtectYourPC:            3,
	}
}
