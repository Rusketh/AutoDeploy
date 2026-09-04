package payload

import (
	"strings"
	"testing"
)

func TestExportSetupCompleteScript(t *testing.T) {
	s := exportSetupCompleteScript("https://deploy.example.com:8080", 42)

	for _, want := range []string{
		"@echo off",
		`"%DEST%\autodeploy-agent.exe" --server "https://deploy.example.com:8080" --image-id 42`,
		`"%DEST%\autodeploy-agent.exe" install-service`,
		`reg add "HKLM\SOFTWARE\AutoDeploy" /v ServerURL`,
		"exit /b 0",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q\n---\n%s", want, s)
		}
	}
	// Batch files need CRLF line endings.
	if !strings.Contains(s, "\r\n") {
		t.Error("script must use CRLF line endings")
	}
}

func TestSafeServerBase(t *testing.T) {
	ok := map[string]string{
		"https://deploy.example.com": "https://deploy.example.com",
		"http://10.0.0.5:8080":       "http://10.0.0.5:8080",
		"https://host.local:443/":    "https://host.local:443", // trailing slash trimmed
	}
	for in, want := range ok {
		got, valid := safeServerBase(in)
		if !valid || got != want {
			t.Errorf("safeServerBase(%q) = %q,%v; want %q,true", in, got, valid, want)
		}
	}
	// Anything that could smuggle a cmd metacharacter or a path is rejected.
	for _, bad := range []string{
		"", "ftp://host", "https://", "deploy.example.com",
		"https://host/path", "https://host?q=1", "https://user@host",
		"https://host;calc.exe", "https://ho st",
	} {
		if got, valid := safeServerBase(bad); valid {
			t.Errorf("safeServerBase(%q) accepted as %q, want rejected", bad, got)
		}
	}
}

func TestSanitizeDirName(t *testing.T) {
	cases := map[string]string{
		"Intel RST":     "Intel_RST",
		"weird/\\:name": "weirdname",
		"  ..leading":   "leading",
		"":              "driver-7",
		"###":           "driver-7",
	}
	for in, want := range cases {
		if got := sanitizeDirName(in, "7"); got != want {
			t.Errorf("sanitizeDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
