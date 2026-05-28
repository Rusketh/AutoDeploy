// Package swspec defines the structured types for SoftwarePackage detection
// rules and install steps. These are the on-wire contract between the
// server (which authors them in the portal) and the agent (which evaluates
// detection and executes steps on the target).
//
// Detection rules answer "is this package already installed on this
// machine?". Steps answer "what do we do to install it?".
//
// All rule and step shapes carry a Type discriminator so the JSON is self-
// describing and forward-compatible: unknown types are rejected at parse
// time, not silently skipped.
package swspec

import (
	"encoding/json"
	"fmt"
)

// DetectionRule is one detection predicate. A package is considered already
// installed when ALL of its detection rules report Present == true (a
// conservative AND); zero rules means "always re-install", which the agent
// surfaces as a diagnostic.
type DetectionRule struct {
	Type string `json:"type"` // "file" | "registry" | "msi" | "script"

	// File detection.
	FilePath    string `json:"file_path,omitempty"`
	FileVersion string `json:"file_version,omitempty"` // optional; exact match
	FileSHA256  string `json:"file_sha256,omitempty"`  // optional; hex digest

	// Registry detection.
	RegistryHive  string `json:"registry_hive,omitempty"`  // "HKLM" | "HKCU" | etc.
	RegistryKey   string `json:"registry_key,omitempty"`   // path without hive
	RegistryValue string `json:"registry_value,omitempty"` // value name
	RegistryEquals string `json:"registry_equals,omitempty"` // optional value compare

	// MSI detection.
	MSIProductCode string `json:"msi_product_code,omitempty"` // GUID with braces

	// Script detection. The script is run; non-zero exit => not detected.
	ScriptShell string `json:"script_shell,omitempty"` // "cmd" | "powershell"
	ScriptBody  string `json:"script_body,omitempty"`
}

// Validate checks the rule's fields make sense for its Type.
func (r DetectionRule) Validate() error {
	switch r.Type {
	case "file":
		if r.FilePath == "" {
			return fmt.Errorf("file detection: file_path required")
		}
	case "registry":
		if r.RegistryHive == "" || r.RegistryKey == "" {
			return fmt.Errorf("registry detection: registry_hive and registry_key required")
		}
	case "msi":
		if r.MSIProductCode == "" {
			return fmt.Errorf("msi detection: msi_product_code required")
		}
	case "script":
		if r.ScriptShell == "" || r.ScriptBody == "" {
			return fmt.Errorf("script detection: script_shell and script_body required")
		}
		if r.ScriptShell != "cmd" && r.ScriptShell != "powershell" {
			return fmt.Errorf("script detection: script_shell must be cmd or powershell")
		}
	default:
		return fmt.Errorf("unknown detection type %q (allowed: file, registry, msi, script)", r.Type)
	}
	return nil
}

// InstallStep is one ordered, typed action.
type InstallStep struct {
	Type        string `json:"type"` // "copy" | "msi" | "appx" | "cmd" | "powershell" | "exe"
	Description string `json:"description,omitempty"`

	// Per-step result handling.
	SuccessCodes      []int `json:"success_codes,omitempty"`        // default [0]
	ContinueOnFailure bool  `json:"continue_on_failure,omitempty"`  // default false (abort)

	// copy.
	SourcePath      string `json:"source_path,omitempty"`
	DestinationPath string `json:"destination_path,omitempty"`

	// msi.
	MSIPath string   `json:"msi_path,omitempty"`
	MSIArgs []string `json:"msi_args,omitempty"` // appended after "/quiet /norestart"

	// appx / msix.
	APPXPath string `json:"appx_path,omitempty"`

	// cmd, powershell.
	ScriptBody string `json:"script_body,omitempty"`

	// exe.
	ExePath string   `json:"exe_path,omitempty"`
	ExeArgs []string `json:"exe_args,omitempty"`
}

// Validate checks the step's fields make sense for its Type.
func (s InstallStep) Validate() error {
	switch s.Type {
	case "copy":
		if s.SourcePath == "" || s.DestinationPath == "" {
			return fmt.Errorf("copy step: source_path and destination_path required")
		}
	case "msi":
		if s.MSIPath == "" {
			return fmt.Errorf("msi step: msi_path required")
		}
	case "appx":
		if s.APPXPath == "" {
			return fmt.Errorf("appx step: appx_path required")
		}
	case "cmd", "powershell":
		if s.ScriptBody == "" {
			return fmt.Errorf("%s step: script_body required", s.Type)
		}
	case "exe":
		if s.ExePath == "" {
			return fmt.Errorf("exe step: exe_path required")
		}
	default:
		return fmt.Errorf("unknown step type %q (allowed: copy, msi, appx, cmd, powershell, exe)", s.Type)
	}
	return nil
}

// ParseDetection parses the JSON-array shape stored in
// software_package.detection_json and validates each rule.
func ParseDetection(raw string) ([]DetectionRule, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []DetectionRule
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("detection_json: %w", err)
	}
	for i, r := range out {
		if err := r.Validate(); err != nil {
			return nil, fmt.Errorf("detection rule %d: %w", i, err)
		}
	}
	return out, nil
}

// ParseSteps parses software_package.steps_json and validates each step.
func ParseSteps(raw string) ([]InstallStep, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []InstallStep
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("steps_json: %w", err)
	}
	for i, s := range out {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
	}
	return out, nil
}
