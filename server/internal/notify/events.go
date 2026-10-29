// Package notify is the notification event bus. It fans out events to
// three channels — in-portal (DB insert), email (SMTP), and webhooks
// (HTTP POST) — in background goroutines so callers are never blocked.
package notify

import "time"

// Event is the payload emitted by callers and routed to channels.
type Event struct {
	Name       string         `json:"event"`
	Severity   string         `json:"severity"`
	OccurredAt time.Time      `json:"occurred_at"`
	Title      string         `json:"title"`
	Body       string         `json:"body"`
	Link       string         `json:"link"`
	Actor      string         `json:"actor"`
	Fields     map[string]any `json:"fields,omitempty"`
}

// Severity levels, ordered by increasing urgency.
const (
	SevInfo    = "info"
	SevSuccess = "success"
	SevWarning = "warning"
	SevError   = "error"
)

// Event names — stable identifiers used in preference/webhook filters.
const (
	EventDeployStarted   = "deploy.started"
	EventDeployCompleted = "deploy.completed"
	EventDeployFailed    = "deploy.failed"
	EventDeployReimage   = "deploy.reimage"

	EventBulkCreated   = "bulk.created"
	EventBulkCompleted = "bulk.completed"
	EventBulkFailed    = "bulk.failed"
	EventBulkPartial   = "bulk.partial"

	EventSoftwareInstallOK     = "software.install_ok"
	EventSoftwareInstallFailed = "software.install_failed"

	EventMachineFirstSeen      = "machine.first_seen"
	EventMachineOffline        = "machine.offline"
	EventMachineHardwareChange = "machine.hardware_change"

	EventADJoinFailed  = "ad.join_failed"
	EventADTrustFailed = "ad.trust_failed"

	EventUpdateDeployed       = "update.deployed"
	EventUpdateComplianceFail = "update.compliance_fail"

	EventAuthLoginFailed = "auth.login_failed"

	EventSystemStorageLow    = "system.storage_low"
	EventSystemAgentOutdated = "system.agent_outdated"
)

// EventCategories groups events for the preferences UI.
var EventCategories = []struct {
	Label  string
	Events []string
}{
	{"Deployments", []string{EventDeployStarted, EventDeployCompleted, EventDeployFailed, EventDeployReimage}},
	{"Bulk operations", []string{EventBulkCreated, EventBulkCompleted, EventBulkFailed, EventBulkPartial}},
	{"Software installs", []string{EventSoftwareInstallOK, EventSoftwareInstallFailed}},
	{"Machine inventory", []string{EventMachineFirstSeen, EventMachineOffline, EventMachineHardwareChange}},
	{"Active Directory", []string{EventADJoinFailed, EventADTrustFailed}},
	{"Windows Updates", []string{EventUpdateDeployed, EventUpdateComplianceFail}},
	{"Security", []string{EventAuthLoginFailed}},
	{"System health", []string{EventSystemStorageLow, EventSystemAgentOutdated}},
}

// eventLabels are the plain-English names the portal shows for each event.
var eventLabels = map[string]string{
	EventDeployStarted:         "Deployment started",
	EventDeployCompleted:       "Deployment finished",
	EventDeployFailed:          "Deployment failed",
	EventDeployReimage:         "Re-image requested",
	EventBulkCreated:           "Bulk operation created",
	EventBulkCompleted:         "Bulk operation finished",
	EventBulkFailed:            "Bulk operation failed",
	EventBulkPartial:           "Bulk operation partly failed",
	EventSoftwareInstallOK:     "Software installed",
	EventSoftwareInstallFailed: "Software install failed",
	EventMachineFirstSeen:      "New machine seen",
	EventMachineOffline:        "Machine went offline",
	EventMachineHardwareChange: "Machine hardware changed",
	EventADJoinFailed:          "Domain join failed",
	EventADTrustFailed:         "Domain trust broken",
	EventUpdateDeployed:        "Windows Update deployed",
	EventUpdateComplianceFail:  "Windows Update compliance below target",
	EventAuthLoginFailed:       "Failed sign-in",
	EventSystemStorageLow:      "Server storage low",
	EventSystemAgentOutdated:   "Agent out of date",
	"system.test":              "Test message",
}

// EventLabel returns the plain-English name of an event, or the event name
// itself when it has none.
func EventLabel(event string) string {
	if l, ok := eventLabels[event]; ok {
		return l
	}
	return event
}

// AllEventNames returns every known event name.
func AllEventNames() []string {
	var out []string
	for _, cat := range EventCategories {
		out = append(out, cat.Events...)
	}
	return out
}

// SeverityAtLeast returns true if sev is >= min in the ordering
// info < success < warning < error.
func SeverityAtLeast(sev, min string) bool {
	return sevRank(sev) >= sevRank(min)
}

func sevRank(s string) int {
	switch s {
	case SevInfo:
		return 0
	case SevSuccess:
		return 1
	case SevWarning:
		return 2
	case SevError:
		return 3
	default:
		return 0
	}
}
