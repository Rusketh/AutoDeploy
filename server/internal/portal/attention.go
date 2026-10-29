package portal

import (
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// attentionItem is one line of the dashboard's "Needs attention" panel: a
// count of things an operator should look at, and a link to exactly those
// (the machine list pre-filtered the same way, so the numbers agree).
type attentionItem struct {
	Key    string
	Count  int
	Label  string // "failed deployments", singular when Count == 1
	Detail string // one short sentence on what it means / what to do
	URL    string
	Dot    string
}

// attentionFailedWithin is how far back the dashboard counts failed
// deployments (the list link carries the same window as ?since=7d).
const attentionFailedWithin = 7 * 24 * time.Hour

// needsAttention counts, over the whole fleet: machines whose latest
// deployment failed in the last 7 days, stalled deployments, machines
// offline for over machineOfflineAfter, queued re-images and AD join
// problems — each by the same derived status / filter the machine list
// uses — plus ISOs whose media preparation failed. Items with a zero
// count are left out; an empty result is the calm "All clear" state.
func needsAttention(ms []model.MachineRecord, f fleetFacts, isoPrepFailed int, now time.Time) []attentionItem {
	var failed, stalled, offline, reimage, ad int
	for _, m := range ms {
		switch f.Status[m.ID].Key {
		case statusFailed:
			if d, ok := f.Latest[m.ID]; ok && now.Sub(d.StartedAt) <= attentionFailedWithin {
				failed++
			}
		case statusStalled:
			stalled++
		case statusOffline:
			offline++
		case statusReimage:
			reimage++
		}
		if model.ADJoinFailure(m.ADJoinStatus) {
			ad++
		}
	}
	pick := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	all := []attentionItem{
		{Key: "failed", Count: failed, Dot: "bad",
			Label:  pick(failed, "failed deployment", "failed deployments"),
			Detail: "Latest deployment failed in the last 7 days. Open a machine to see the first error and what to try.",
			URL:    "/portal/machines?status=failed&since=7d"},
		{Key: "stalled", Count: stalled, Dot: "warn",
			Label:  pick(stalled, "stalled deployment", "stalled deployments"),
			Detail: "Deploying, but no check-in for over " + humanDuration(deployStallAfter) + ". Setup may have failed or the machine is off.",
			URL:    "/portal/machines?status=stalled"},
		{Key: "ad", Count: ad, Dot: "bad",
			Label:  pick(ad, "machine with an AD join problem", "machines with AD join problems"),
			Detail: "Joining the domain failed, or the machine's trust with the domain is broken.",
			URL:    "/portal/machines?ad=problem"},
		{Key: "reimage", Count: reimage, Dot: "warn",
			Label:  pick(reimage, "re-image pending", "re-images pending"),
			Detail: "Queued and waiting for the machine to reboot into AutoDeploy.",
			URL:    "/portal/machines?status=reimage"},
		{Key: "iso", Count: isoPrepFailed, Dot: "bad",
			Label:  pick(isoPrepFailed, "ISO failed to prepare", "ISOs failed to prepare"),
			Detail: "Images built from it can't deploy until the ISO is fixed or re-uploaded.",
			URL:    "/portal/isos"},
		{Key: "offline", Count: offline, Dot: "off",
			Label:  pick(offline, "machine offline", "machines offline"),
			Detail: "No check-in for over " + humanDuration(machineOfflineAfter) + ". Retired machines can be deleted from inventory.",
			URL:    "/portal/machines?status=offline"},
	}
	out := make([]attentionItem, 0, len(all))
	for _, it := range all {
		if it.Count > 0 {
			out = append(out, it)
		}
	}
	return out
}
