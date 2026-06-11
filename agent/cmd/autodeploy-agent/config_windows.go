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

// saveConfig persists ServerURL and AgentID to HKLM\SOFTWARE\AutoDeploy —
// the same values SetupComplete.cmd provisions at deploy time — so a
// manually run agent keeps its identity across runs and an installed
// service finds its config. Needs an elevated process (HKLM write).
func saveConfig(serverURL, agentID string) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, registryPath,
		registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("ServerURL", serverURL); err != nil {
		return err
	}
	return k.SetStringValue("AgentID", agentID)
}
