package main

import (
	"regexp"
	"strings"
)

// Pure parsing of `bcdedit /enum firmware` output. Kept tag-free (no
// Windows dependency) so it's unit-tested on any OS; the exec that feeds it
// lives in nextboot_windows.go.

var (
	// {GUID} or a well-known id like {bootmgr}.
	bcdIdentifierRe  = regexp.MustCompile(`(?i)^identifier\s+(\{[0-9a-z-]+\})`)
	bcdDescriptionRe = regexp.MustCompile(`(?i)^description\s+(.+)$`)
	// Network/PXE boot entries the firmware exposes. Vendors phrase these
	// many ways: "UEFI: PXE IPv4 ...", "IBA GE Slot ...", "EFI Network",
	// "Onboard NIC", etc. Match the common signals.
	netDescRe = regexp.MustCompile(`(?i)\b(pxe|ipv4|ipv6|ip4|ip6|network|nic|lan|ethernet|iba)\b`)
)

// findNetworkFirmwareEntry parses `bcdedit /enum firmware` output and
// returns the identifier of the first firmware application whose
// description looks like a network/PXE boot entry. Empty if none.
func findNetworkFirmwareEntry(enum string) string {
	var curID string
	for _, raw := range strings.Split(enum, "\n") {
		line := strings.TrimSpace(raw)
		if m := bcdIdentifierRe.FindStringSubmatch(line); m != nil {
			curID = m[1]
			continue
		}
		if m := bcdDescriptionRe.FindStringSubmatch(line); m != nil {
			desc := m[1]
			// Skip the Windows boot manager / OS loader descriptions.
			if strings.Contains(strings.ToLower(desc), "windows") {
				continue
			}
			if netDescRe.MatchString(desc) && curID != "" {
				return curID
			}
		}
	}
	return ""
}
