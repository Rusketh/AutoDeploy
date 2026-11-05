package api

import (
	"github.com/rusketh/autodeploy/server/internal/agentauth"
)

// agentAuth returns the configured agent authenticator. A Repos wired without
// one (tests, tools) gets a legacy-accepting authenticator over its
// inventory and credentials, which behaves exactly like the pre-secret
// protocol for machines that hold no secret.
func agentAuth(r Repos) *agentauth.Authenticator {
	if r.AgentAuth != nil {
		return r.AgentAuth
	}
	return &agentauth.Authenticator{Inventory: r.Inventory, Creds: r.Credentials, AllowLegacy: true}
}
