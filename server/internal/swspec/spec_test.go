package swspec

import (
	"strings"
	"testing"
)

func TestParseDetectionAllTypes(t *testing.T) {
	raw := `[
		{"type":"file","file_path":"C:\\Windows\\notepad.exe","file_version":"10.0"},
		{"type":"registry","registry_hive":"HKLM","registry_key":"Software\\Acme","registry_value":"Installed","registry_equals":"1"},
		{"type":"msi","msi_product_code":"{12345678-90AB-CDEF-0123-456789ABCDEF}"},
		{"type":"script","script_shell":"powershell","script_body":"if (Test-Path C:\\foo) { exit 0 } else { exit 1 }"}
	]`
	rules, err := ParseDetection(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 4 {
		t.Fatalf("got %d rules", len(rules))
	}
}

func TestParseDetectionRejectsUnknownType(t *testing.T) {
	_, err := ParseDetection(`[{"type":"bogus"}]`)
	if err == nil || !strings.Contains(err.Error(), "unknown detection type") {
		t.Errorf("expected unknown-type error, got %v", err)
	}
}

func TestParseStepsAllTypes(t *testing.T) {
	raw := `[
		{"type":"copy","source_path":"C:\\src\\a","destination_path":"C:\\dst\\a"},
		{"type":"msi","msi_path":"C:\\pkg\\a.msi","msi_args":["TARGETDIR=C:\\Foo"]},
		{"type":"appx","appx_path":"C:\\pkg\\a.appx"},
		{"type":"cmd","script_body":"echo hello"},
		{"type":"powershell","script_body":"Write-Host hi"},
		{"type":"exe","exe_path":"C:\\pkg\\a.exe","exe_args":["/S"]}
	]`
	steps, err := ParseSteps(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 6 {
		t.Fatalf("got %d steps", len(steps))
	}
}

// A per-step filter_os parses, survives a round-trip, and doesn't interfere
// with the step's own required-field validation.
func TestParseStepsCarriesFilterOS(t *testing.T) {
	raw := `[
		{"type":"unzip","filter_os":"Windows 11","source_path":"win11.zip","destination_path":"C:\\App"},
		{"type":"unzip","filter_os":"Windows 10","source_path":"win10.zip","destination_path":"C:\\App"},
		{"type":"cmd","script_body":"echo done"}
	]`
	steps, err := ParseSteps(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 3 {
		t.Fatalf("got %d steps", len(steps))
	}
	if steps[0].FilterOS != "Windows 11" {
		t.Errorf("step 0 filter_os = %q, want %q", steps[0].FilterOS, "Windows 11")
	}
	if steps[1].FilterOS != "Windows 10" {
		t.Errorf("step 1 filter_os = %q, want %q", steps[1].FilterOS, "Windows 10")
	}
	if steps[2].FilterOS != "" {
		t.Errorf("step 2 filter_os = %q, want empty", steps[2].FilterOS)
	}
}

// An appx bundle step carries dependency packages, an offline licence, and the
// machine-wide provision flag, and they survive a JSON round-trip.
func TestParseStepsCarriesAppxBundleFields(t *testing.T) {
	raw := `[
		{"type":"appx","appx_path":"App.msixbundle","appx_dependencies":["VCLibs.appx","UIXaml.appx"],"appx_license":"App_License1.xml","appx_provision":true}
	]`
	steps, err := ParseSteps(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps", len(steps))
	}
	s := steps[0]
	if s.APPXPath != "App.msixbundle" {
		t.Errorf("appx_path = %q", s.APPXPath)
	}
	if len(s.APPXDependencies) != 2 || s.APPXDependencies[0] != "VCLibs.appx" {
		t.Errorf("appx_dependencies = %v", s.APPXDependencies)
	}
	if s.APPXLicense != "App_License1.xml" {
		t.Errorf("appx_license = %q", s.APPXLicense)
	}
	if !s.APPXProvision {
		t.Error("appx_provision should be true")
	}
}

// A licence without provisioning is rejected: Add-AppxPackage (per-user) has no
// place to apply an offline Store licence, so it only makes sense machine-wide.
func TestParseStepsRejectsLicenseWithoutProvision(t *testing.T) {
	raw := `[{"type":"appx","appx_path":"App.msixbundle","appx_license":"App_License1.xml"}]`
	_, err := ParseSteps(raw)
	if err == nil || !strings.Contains(err.Error(), "appx_provision") {
		t.Errorf("expected appx_license-needs-appx_provision error, got %v", err)
	}
}

func TestStepValidationRequiresFields(t *testing.T) {
	cases := []struct {
		name, raw string
	}{
		{"copy needs both paths", `[{"type":"copy","source_path":"a"}]`},
		{"msi needs path", `[{"type":"msi"}]`},
		{"appx needs path", `[{"type":"appx"}]`},
		{"cmd needs body", `[{"type":"cmd"}]`},
		{"exe needs path", `[{"type":"exe"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSteps(tc.raw); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}
