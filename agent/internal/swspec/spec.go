// Package swspec mirrors the server's swspec types. Kept as a separate
// package because the server and agent are independent modules; the wire
// contract is what they share, not Go code.
package swspec

// DetectionRule mirrors server/internal/swspec.DetectionRule.
type DetectionRule struct {
	Type           string `json:"type"`
	FilePath       string `json:"file_path,omitempty"`
	FileVersion    string `json:"file_version,omitempty"`
	FileSHA256     string `json:"file_sha256,omitempty"`
	RegistryHive   string `json:"registry_hive,omitempty"`
	RegistryKey    string `json:"registry_key,omitempty"`
	RegistryValue  string `json:"registry_value,omitempty"`
	RegistryEquals string `json:"registry_equals,omitempty"`
	MSIProductCode string `json:"msi_product_code,omitempty"`
	ScriptShell    string `json:"script_shell,omitempty"`
	ScriptBody     string `json:"script_body,omitempty"`
	WingetID       string `json:"winget_id,omitempty"`
}

// InstallStep mirrors server/internal/swspec.InstallStep.
type InstallStep struct {
	Type              string   `json:"type"`
	Description       string   `json:"description,omitempty"`
	FilterOS          string   `json:"filter_os,omitempty"`
	SuccessCodes      []int    `json:"success_codes,omitempty"`
	ContinueOnFailure bool     `json:"continue_on_failure,omitempty"`
	SourcePath        string   `json:"source_path,omitempty"`
	DestinationPath   string   `json:"destination_path,omitempty"`
	MSIPath           string   `json:"msi_path,omitempty"`
	MSIArgs           []string `json:"msi_args,omitempty"`
	APPXPath          string   `json:"appx_path,omitempty"`
	ScriptBody        string   `json:"script_body,omitempty"`
	ExePath           string   `json:"exe_path,omitempty"`
	ExeArgs           []string `json:"exe_args,omitempty"`
	WingetID          string   `json:"winget_id,omitempty"`
	WingetArgs        []string `json:"winget_args,omitempty"`
}
