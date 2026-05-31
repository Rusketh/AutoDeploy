package unattend

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
)

// decodeFirstBase64Blob extracts the first base64 payload that the
// SetupComplete writer embeds in its PowerShell one-liner (the value
// assigned to the $d variable) and returns its decoded bytes as a
// string. Returns an error if no payload is found or decode fails.
func decodeFirstBase64Blob(x string) (string, error) {
	// The XML emitter HTML-escapes the embedded PowerShell, so the
	// single quotes around the payload appear as &#39; not '.
	const startMarker = "$d=&#39;"
	const endMarker = "&#39;"
	i := strings.Index(x, startMarker)
	if i < 0 {
		// Fall back to unescaped form just in case the emitter changes.
		i = strings.Index(x, "$d='")
		if i < 0 {
			return "", fmt.Errorf("no $d='...' payload found")
		}
		rest := x[i+len("$d='"):]
		j := strings.Index(rest, "'")
		if j < 0 {
			return "", fmt.Errorf("unterminated $d payload")
		}
		return decodeB64(rest[:j])
	}
	rest := x[i+len(startMarker):]
	j := strings.Index(rest, endMarker)
	if j < 0 {
		return "", fmt.Errorf("unterminated $d payload")
	}
	return decodeB64(rest[:j])
}

func decodeB64(s string) (string, error) {
	dec, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(dec), nil
}

func TestGenerateDrivesUnattendedInstall(t *testing.T) {
	s := Defaults()
	s.Edition = "Windows 11 Pro"
	x, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	out := string(x)
	// Without these, Setup drops to the interactive disk/driver screens.
	for _, want := range []string{
		`<DiskConfiguration>`,
		`<WillWipeDisk>false</WillWipeDisk>`, // coexist with the media partition
		`<ImageInstall>`,
		`<Key>/IMAGE/NAME</Key><Value>Windows 11 Pro</Value>`,
		`<InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated unattend missing %q", want)
		}
	}
	// Must still be valid XML.
	var root struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(x, &root); err != nil {
		t.Fatalf("invalid XML: %v\n%s", err, out)
	}
}

func TestGenerateDefaultsIsValidXML(t *testing.T) {
	x, err := Generate(Defaults())
	if err != nil {
		t.Fatal(err)
	}
	var any struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(x, &any); err != nil {
		t.Fatalf("generated XML failed to parse: %v\n---\n%s", err, x)
	}
	if any.XMLName.Local != "unattend" {
		t.Errorf("root = %q, want unattend", any.XMLName.Local)
	}
}

func TestGenerateIncludesAdminPassword(t *testing.T) {
	s := Defaults()
	s.LocalAccounts = []LocalAccount{{
		Name: "Administrator", Password: "Hunter2-NotReal", Group: "Administrators",
	}}
	x, _ := Generate(s)
	if !XMLContainsSecret(x, "Hunter2-NotReal") {
		t.Errorf("admin password missing from XML; unattended setup would fail")
	}
	// And the legacy <AdministratorPassword> block (built-in account)
	// should also carry it.
	if !strings.Contains(string(x), "<AdministratorPassword>") {
		t.Error("expected <AdministratorPassword> block for the first admin")
	}
}

func TestGenerateLegacyAdminFieldsStillWork(t *testing.T) {
	// Existing unattend rows that have admin_user / admin_password in
	// their settings_json must still produce a working XML after the
	// LocalAccounts migration.
	s, err := Parse(`{"admin_user":"Admin","admin_password":"Old1!"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.LocalAccounts) != 1 {
		t.Fatalf("Parse should migrate legacy admin to LocalAccounts, got %+v", s.LocalAccounts)
	}
	x, _ := Generate(s)
	if !XMLContainsSecret(x, "Old1!") {
		t.Error("legacy admin_password lost during migration")
	}
}

func TestGenerateOmitsAdminWhenHidden(t *testing.T) {
	s := Defaults()
	s.HideAdmin = true
	s.LocalAccounts = nil
	s.AdminUser = ""
	s.AdminPassword = "should-not-appear"
	x, _ := Generate(s)
	if strings.Contains(string(x), "should-not-appear") {
		t.Error("hidden admin: password leaked into XML")
	}
	if strings.Contains(string(x), "<AdministratorPassword>") {
		t.Error("hidden admin: AdministratorPassword block should be absent")
	}
}

func TestGenerateMultipleLocalAccounts(t *testing.T) {
	s := Defaults()
	s.LocalAccounts = []LocalAccount{
		{Name: "Administrator", Password: "AdminPw1!", Group: "Administrators",
			FullName: "Local Administrator", Description: "Lab admin"},
		{Name: "operator", Password: "OperatorPw1!", Group: "Users",
			FullName: "Lab Operator"},
		{Name: "guest-svc", Password: "SvcPw1!", Group: "Users"},
	}
	x, _ := Generate(s)
	for _, want := range []string{
		"<Name>Administrator</Name>",
		"<Name>operator</Name>",
		"<Name>guest-svc</Name>",
		"<Group>Administrators</Group>",
		"<Group>Users</Group>",
		"AdminPw1!",
		"OperatorPw1!",
		"SvcPw1!",
		"<DisplayName>Local Administrator</DisplayName>",
		"<Description>Lab admin</Description>",
	} {
		if !strings.Contains(string(x), want) {
			t.Errorf("expected %q in XML; got:\n%s", want, x)
		}
	}
}

func TestGenerateAutoLogon(t *testing.T) {
	s := Defaults()
	s.LocalAccounts = []LocalAccount{{
		Name: "deployadmin", Password: "AutoLogPw1!", Group: "Administrators",
	}}
	s.AutoLogon = &AutoLogonSetting{
		Username: "deployadmin", Password: "AutoLogPw1!", Count: 3,
	}
	x, _ := Generate(s)
	if !strings.Contains(string(x), "<AutoLogon>") {
		t.Error("expected <AutoLogon> block")
	}
	if !strings.Contains(string(x), "<Username>deployadmin</Username>") {
		t.Error("AutoLogon: username missing")
	}
	if !strings.Contains(string(x), "<LogonCount>3</LogonCount>") {
		t.Error("AutoLogon: count missing")
	}
}

func TestGenerateAutoLogonOmittedWhenUnset(t *testing.T) {
	s := Defaults()
	s.AutoLogon = nil
	x, _ := Generate(s)
	if strings.Contains(string(x), "<AutoLogon>") {
		t.Error("AutoLogon should not appear when nil")
	}
}

func TestGenerateAlwaysIncludesAgentBootstrap(t *testing.T) {
	x, _ := Generate(Defaults())
	if !strings.Contains(string(x), "autodeploy-agent") {
		t.Errorf("expected agent bootstrap in FirstLogonCommands; got\n%s", x)
	}
}

func TestGenerateWin11AddsBypassNRO(t *testing.T) {
	s := Defaults()
	s.TargetOS = "windows-11"
	s.BypassNRO = true
	x, _ := Generate(s)
	if !strings.Contains(string(x), "BypassNRO") {
		t.Errorf("Windows 11 unattend should set BypassNRO; got\n%s", x)
	}
	if !strings.Contains(string(x), "Microsoft-Windows-Deployment") {
		t.Errorf("Windows 11 BypassNRO should be in Deployment component; got\n%s", x)
	}
}

func TestGenerateWin10OmitsBypassNRO(t *testing.T) {
	s := Defaults()
	s.TargetOS = "windows-10"
	s.BypassNRO = true
	x, _ := Generate(s)
	if strings.Contains(string(x), "BypassNRO") {
		t.Errorf("Windows 10 unattend should NOT emit BypassNRO; got\n%s", x)
	}
}

func TestGenerateBypassWin11ReqsOnlyWhenOpted(t *testing.T) {
	s := Defaults()
	s.TargetOS = "windows-11"
	s.BypassWin11Reqs = false
	x, _ := Generate(s)
	if strings.Contains(string(x), "BypassTPMCheck") {
		t.Error("BypassWin11Reqs default should NOT include LabConfig keys")
	}
	s.BypassWin11Reqs = true
	x, _ = Generate(s)
	for _, want := range []string{"BypassTPMCheck", "BypassSecureBootCheck", "BypassRAMCheck", "BypassCPUCheck", "BypassStorageCheck"} {
		if !strings.Contains(string(x), want) {
			t.Errorf("BypassWin11Reqs=true should emit %s", want)
		}
	}
}

func TestGenerateLicensingKMS(t *testing.T) {
	s := Defaults()
	s.KMSServer = "kms.corp.example"
	x, _ := Generate(s)
	if !strings.Contains(string(x), "slmgr.vbs /skms kms.corp.example:1688") {
		t.Errorf("expected slmgr /skms with default port; got\n%s", x)
	}
	s.KMSPort = 1234
	x, _ = Generate(s)
	if !strings.Contains(string(x), "kms.corp.example:1234") {
		t.Errorf("expected explicit port; got\n%s", x)
	}
}

func TestGenerateLicensingAVMA(t *testing.T) {
	s := Defaults()
	s.AVMAKey = "TMJ3Y-NTRTM-FJYXT-T22BY-CWG4J"
	x, _ := Generate(s)
	if !strings.Contains(string(x), "slmgr.vbs /ipk TMJ3Y-NTRTM-FJYXT-T22BY-CWG4J") {
		t.Errorf("expected AVMA slmgr /ipk; got\n%s", x)
	}
}

func TestGenerateSkipAutoActivation(t *testing.T) {
	s := Defaults()
	s.SkipAutoActivation = true
	x, _ := Generate(s)
	if !strings.Contains(string(x), "<SkipAutoActivation>true</SkipAutoActivation>") {
		t.Error("expected <SkipAutoActivation>")
	}
}

func TestGeneratePolicies(t *testing.T) {
	s := Defaults()
	s.TelemetryLevel = 1
	s.DisableWindowsUpdate = true
	s.DeferFeatureUpdatesDays = 90
	s.DeferQualityUpdatesDays = 14
	s.EnableRDP = true
	s.AllowICMPv4 = true
	s.PowerScheme = "high"
	x, _ := Generate(s)
	checks := []string{
		`AllowTelemetry /t REG_DWORD /d 1`,
		`NoAutoUpdate /t REG_DWORD /d 1`,
		`DeferFeatureUpdatesPeriodInDays /t REG_DWORD /d 90`,
		`DeferQualityUpdatesPeriodInDays /t REG_DWORD /d 14`,
		`fDenyTSConnections /t REG_DWORD /d 0`,
		`remote desktop`,
		`ICMPv4-In`,
		PowerSchemeHighPerformance,
	}
	for _, want := range checks {
		if !strings.Contains(string(x), want) {
			t.Errorf("policy: expected %q in XML", want)
		}
	}
}

func TestGenerateSpecializeCommands(t *testing.T) {
	s := Defaults()
	s.SpecializeCommands = []SpecializeCommand{
		{Order: 10, Description: "Lab tweak A", CommandLine: `reg add HKLM\Software\Lab /v A /d 1 /f`},
		{Order: 20, Description: "Lab tweak B", CommandLine: `reg add HKLM\Software\Lab /v B /d 2 /f`},
	}
	x, _ := Generate(s)
	if !strings.Contains(string(x), "Lab tweak A") || !strings.Contains(string(x), "Lab tweak B") {
		t.Errorf("specialize commands missing; got\n%s", x)
	}
}

func TestGenerateSetupCompleteCommands(t *testing.T) {
	s := Defaults()
	s.SetupCompleteCommands = []SetupCompleteCommand{
		{Order: 10, Description: "Pre-OOBE: enable BitLocker pre-provisioning",
			CommandLine: `manage-bde -on C: -used`},
		{Order: 20, Description: "Pre-OOBE: schedule reboot",
			CommandLine: `shutdown /r /t 0`},
	}
	x, _ := Generate(s)
	// The setup-complete writer should be a single specialize
	// RunSynchronousCommand using PowerShell + base64.
	if !strings.Contains(string(x), "SetupComplete: write Scripts\\SetupComplete.cmd") {
		t.Errorf("expected SetupComplete writer command; got\n%s", x)
	}
	if !strings.Contains(string(x), "powershell -NoProfile") {
		t.Errorf("expected PowerShell decoder; got\n%s", x)
	}
	if !strings.Contains(string(x), "SetupComplete.cmd") {
		t.Errorf("expected reference to SetupComplete.cmd; got\n%s", x)
	}
	// The operator's commands are base64-encoded so they shouldn't
	// appear verbatim — and the secret-handling check would trip on
	// any plaintext leak. (manage-bde isn't a secret but verifying
	// the encoded form is the easiest assertion.)
	if strings.Contains(string(x), "manage-bde -on C: -used") {
		t.Error("SetupComplete payload should be base64-encoded, not inline")
	}
	// Decode the base64 portion and verify the operator's command is
	// inside.
	out, err := decodeFirstBase64Blob(string(x))
	if err != nil {
		t.Fatalf("decoding base64 from XML failed: %v", err)
	}
	if !strings.Contains(out, "manage-bde -on C: -used") {
		t.Errorf("decoded SetupComplete.cmd missing operator command; got\n%s", out)
	}
	if !strings.Contains(out, "shutdown /r /t 0") {
		t.Errorf("decoded SetupComplete.cmd missing second operator command; got\n%s", out)
	}
}

func TestGenerateBypassWin11ReqsIgnoredForWin10(t *testing.T) {
	s := Defaults()
	s.TargetOS = "windows-10"
	s.BypassWin11Reqs = true
	x, _ := Generate(s)
	if strings.Contains(string(x), "BypassTPMCheck") {
		t.Error("Win10 should not get the LabConfig bypass even when the flag is on")
	}
}

func TestTargetOSCommentInHeader(t *testing.T) {
	for _, os := range []string{"windows-11", "windows-10", "windows-server-2022"} {
		s := Defaults()
		s.TargetOS = os
		x, _ := Generate(s)
		if !strings.Contains(string(x), "Generated by AutoDeploy for "+os) {
			t.Errorf("expected target OS comment for %s", os)
		}
	}
}

func TestNameStrategies(t *testing.T) {
	cases := []struct {
		strat, name, want string
	}{
		{"random", "ignored", "<ComputerName>*</ComputerName>"},
		{"literal", "LAB-01", "<ComputerName>LAB-01</ComputerName>"},
		{"prefix", "LAB", "<ComputerName>LAB*</ComputerName>"},
	}
	for _, tc := range cases {
		s := Defaults()
		s.NameStrategy = tc.strat
		s.ComputerName = tc.name
		x, _ := Generate(s)
		if !strings.Contains(string(x), tc.want) {
			t.Errorf("strategy %s: missing %q in XML\n%s", tc.strat, tc.want, x)
		}
	}
}

func TestDomainJoinIncluded(t *testing.T) {
	s := Defaults()
	s.DomainJoin = &DomainJoin{
		Domain:       "corp.example",
		OU:           "OU=Lab,DC=corp,DC=example",
		JoinUser:     "joiner@corp.example",
		JoinPassword: "join-password-not-real",
	}
	x, _ := Generate(s)
	for _, w := range []string{
		"<JoinDomain>corp.example</JoinDomain>",
		"<MachineObjectOU>OU=Lab,DC=corp,DC=example</MachineObjectOU>",
		"joiner@corp.example",
		"join-password-not-real",
	} {
		if !strings.Contains(string(x), w) {
			t.Errorf("domain join: missing %q", w)
		}
	}
}

func TestParseAppliesDefaults(t *testing.T) {
	s, err := Parse(`{"computer_name":"LAB-01","name_strategy":"literal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if s.Locale != "en-US" {
		t.Errorf("default locale not applied: %+v", s)
	}
	if s.ComputerName != "LAB-01" {
		t.Errorf("ComputerName lost: %+v", s)
	}
}
