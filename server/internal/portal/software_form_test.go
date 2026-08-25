package portal

import (
	"bytes"
	"html/template"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// renderSoftwareForm parses and executes the software_form.html "body" block in
// isolation with the funcs that template needs, returning the rendered HTML.
func renderSoftwareForm(t *testing.T, data map[string]any) string {
	t.Helper()
	funcs := template.FuncMap{
		"int64":      func(id model.ID) int64 { return int64(id) },
		"humanBytes": func(int64) string { return "X" },
		"relTime":    func(time.Time) string { return "now" },
		"dict": func(args ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(args); i += 2 {
				k, _ := args[i].(string)
				m[k] = args[i+1]
			}
			return m
		},
	}
	tmpl, err := template.New("").Funcs(funcs).ParseFS(assetsFS, "templates/software_form.html")
	if err != nil {
		t.Fatalf("parse software_form.html: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "body", map[string]any{"Data": data}); err != nil {
		t.Fatalf("exec software_form.html: %v", err)
	}
	return buf.String()
}

// TestSoftwareFormRendersOSFilter confirms the step editor renders the per-step
// OS filter: the input pre-filled with a saved FilterOS, the shared datalist of
// OS suggestions, and the JS that adds the field to newly-created steps.
func TestSoftwareFormRendersOSFilter(t *testing.T) {
	out := renderSoftwareForm(t, map[string]any{
		"Pkg":     model.SoftwarePackage{ID: 1, Name: "MyApp"},
		"IsNew":   false,
		"Steps":   []swspec.InstallStep{{Type: "unzip", FilterOS: "Windows 11", SourcePath: "win11.zip", DestinationPath: "dst"}},
		"Rules":   []swspec.DetectionRule{},
		"Files":   []string{},
		"Bundles": []string{},
	})
	for _, want := range []string{
		`name="step_0_filter_os"`,           // the field is emitted for the step
		`value="Windows 11"`,                // pre-filled with the saved value
		`<datalist id="os-filter-options">`, // suggestions list present
		`list="os-filter-options"`,          // input is wired to the datalist
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered software form missing %q", want)
		}
	}
}

// TestBuildSoftwareFromFormCarriesOSFilter covers the motivating case: a single
// package carries a Windows 11 unzip step and a Windows 10 unzip step, each
// gated by an OS filter, plus an unfiltered step. The per-step filter_os field
// must survive the form round-trip into steps_json so the agent can apply it.
func TestBuildSoftwareFromFormCarriesOSFilter(t *testing.T) {
	form := strings.NewReader(strings.Join([]string{
		"name=MyApp",
		"step_index[]=0",
		"step_0_type=unzip",
		"step_0_filter_os=Windows+11",
		"step_0_source_path=win11.zip",
		"step_0_destination_path=dst",
		"step_index[]=1",
		"step_1_type=unzip",
		"step_1_filter_os=Windows+10",
		"step_1_source_path=win10.zip",
		"step_1_destination_path=dst",
		"step_index[]=2",
		"step_2_type=cmd",
		"step_2_cmd_script_body=echo+done",
	}, "&"))
	req := httptest.NewRequest("POST", "/portal/software", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	pkg, err := buildSoftwareFromForm(req)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := swspec.ParseSteps(pkg.StepsJSON)
	if err != nil {
		t.Fatalf("steps_json should parse: %v (json=%s)", err, pkg.StepsJSON)
	}
	if len(steps) != 3 {
		t.Fatalf("expected 3 steps, got %d (json=%s)", len(steps), pkg.StepsJSON)
	}
	if steps[0].FilterOS != "Windows 11" {
		t.Errorf("step 0 FilterOS = %q, want %q", steps[0].FilterOS, "Windows 11")
	}
	if steps[1].FilterOS != "Windows 10" {
		t.Errorf("step 1 FilterOS = %q, want %q", steps[1].FilterOS, "Windows 10")
	}
	// An unfiltered step must stay unfiltered (and omit the field from JSON).
	if steps[2].FilterOS != "" {
		t.Errorf("step 2 FilterOS = %q, want empty", steps[2].FilterOS)
	}
	if strings.Contains(pkg.StepsJSON, `"filter_os":""`) {
		t.Errorf("empty filter_os should be omitted from JSON, got %s", pkg.StepsJSON)
	}
}

// A powershell step's body must survive the form round-trip. cmd and powershell
// have separate textareas that both submit (the hidden one with display:none is
// still posted); when they shared a field name the empty cmd textarea, first in
// the form, clobbered the powershell body and the step failed to save. Post
// both fields with only the powershell one filled, mimicking the real form, and
// confirm the body lands in steps_json.
func TestBuildSoftwareFromFormSavesPowershellBody(t *testing.T) {
	form := strings.NewReader(strings.Join([]string{
		"name=MyApp",
		"step_index[]=0",
		"step_0_type=powershell",
		"step_0_cmd_script_body=", // the inactive cmd textarea posts empty
		"step_0_ps_script_body=Write-Host+hi",
	}, "&"))
	req := httptest.NewRequest("POST", "/portal/software", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	pkg, err := buildSoftwareFromForm(req)
	if err != nil {
		t.Fatalf("powershell step should save: %v", err)
	}
	steps, err := swspec.ParseSteps(pkg.StepsJSON)
	if err != nil {
		t.Fatalf("steps_json should parse: %v (json=%s)", err, pkg.StepsJSON)
	}
	if len(steps) != 1 || steps[0].Type != "powershell" {
		t.Fatalf("expected 1 powershell step, got %+v", steps)
	}
	if steps[0].ScriptBody != "Write-Host hi" {
		t.Errorf("ScriptBody = %q, want %q", steps[0].ScriptBody, "Write-Host hi")
	}
}

// The appx bundle fields (dependencies, offline licence, machine-wide provision)
// survive the form round-trip into steps_json.
func TestBuildSoftwareFromFormCarriesAppxBundleFields(t *testing.T) {
	form := strings.NewReader(strings.Join([]string{
		"name=MyApp",
		"step_index[]=0",
		"step_0_type=appx",
		"step_0_appx_path=App.msixbundle",
		"step_0_appx_dependencies=VCLibs.appx+UIXaml.appx",
		"step_0_appx_license=App_License1.xml",
		"step_0_appx_provision=1",
	}, "&"))
	req := httptest.NewRequest("POST", "/portal/software", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	pkg, err := buildSoftwareFromForm(req)
	if err != nil {
		t.Fatalf("appx bundle step should save: %v", err)
	}
	steps, err := swspec.ParseSteps(pkg.StepsJSON)
	if err != nil {
		t.Fatalf("steps_json should parse: %v (json=%s)", err, pkg.StepsJSON)
	}
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}
	s := steps[0]
	if s.APPXPath != "App.msixbundle" || len(s.APPXDependencies) != 2 ||
		s.APPXDependencies[1] != "UIXaml.appx" || s.APPXLicense != "App_License1.xml" || !s.APPXProvision {
		t.Errorf("appx bundle fields not round-tripped: %+v", s)
	}
}
