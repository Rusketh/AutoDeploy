//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// registryPath is where the Boot Client's SetupComplete.cmd provisions the
// server URL and the machine's server-minted agent_id at deploy time.
const registryPath = `SOFTWARE\AutoDeploy`

// loadConfig reads ServerURL and AgentID from HKLM\SOFTWARE\AutoDeploy.
// Missing values come back empty so the caller can fall back to flags.
func loadConfig() (serverURL, agentID string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, registryPath,
		registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return "", ""
	}
	defer k.Close()
	serverURL, _, _ = k.GetStringValue("ServerURL")
	agentID, _, _ = k.GetStringValue("AgentID")
	return serverURL, agentID
}
