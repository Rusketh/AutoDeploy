package main

import (
	"os"
	"strings"
	"testing"
)

func TestWriteSetupCompleteProvisionsRegistryAndService(t *testing.T) {
	work := t.TempDir()
	path, err := writeSetupComplete(work, "https://deploy.example:8443", "abc-123-uuid")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{
		// Copies the agent into Program Files.
		`copy /y "%SRC%" "%DEST%\autodeploy-agent.exe"`,
		// Provisions server URL + the server-minted agent_id into the registry.
		`reg add "HKLM\SOFTWARE\AutoDeploy" /v ServerURL /t REG_SZ /d "https://deploy.example:8443" /f`,
		`reg add "HKLM\SOFTWARE\AutoDeploy" /v AgentID /t REG_SZ /d "abc-123-uuid" /f`,
		// Installs the agent as a real service (no scheduled task, no
		// --server/--image-id flags -- the agent reads the registry).
		`"%DEST%\autodeploy-agent.exe" install-service`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("SetupComplete.cmd missing %q\n---\n%s", want, got)
		}
	}
	// The old broken bootstrap must be gone.
	if strings.Contains(got, "%AUTODEPLOY_SERVER%") || strings.Contains(got, "schtasks") {
		t.Errorf("SetupComplete.cmd still uses the old bootstrap:\n%s", got)
	}
	// Batch files need CRLF.
	if !strings.Contains(got, "\r\n") {
		t.Error("SetupComplete.cmd should use CRLF line endings")
	}
}
