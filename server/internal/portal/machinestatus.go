package portal

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Fleet status. Every machine gets one status, derived on each render
// (never persisted) from how long ago it last checked in and from its
// latest deployment, so it corrects itself the moment the machine checks
// back in. The thresholds live here and only here; the list, the detail
// page, the dashboard and their tooltips all read them.
const (
	// machineOnlineWithin: a check-in this recent counts as Online (the
	// agent polls every few minutes while Windows is running).
	machineOnlineWithin = 15 * time.Minute
	// machineOfflineAfter: no check-in for longer than this is Offline.
	// Between the two the machine is Idle — usually switched off or asleep.
	machineOfflineAfter = 7 * 24 * time.Hour
)

// Status keys, as used in ?status= and the CSS hooks.
const (
	statusOnline    = "online"
	statusIdle      = "idle"
	statusOffline   = "offline"
	statusDeploying = "deploying"
	statusFailed    = "failed"
	statusStalled   = "stalled"
	statusReimage   = "reimage"
)

// machineStatus is one machine's derived fleet status.
type machineStatus struct {
	Key   string // one of the status* keys
	Label string // short plain-English label, always shown next to the dot
	Dot   string // .dot colour class
	Title string // tooltip: what the status means
	Rank  int    // sort order: most urgent first
}

// machineStatuses lists every status in sort (urgency) order. The filter
// chips render in this order too.
var machineStatuses = []machineStatus{
	{Key: statusFailed, Label: "Failed", Dot: "bad",
		Title: "The latest deployment failed. Open the machine to see the first error."},
	{Key: statusStalled, Label: "Stalled", Dot: "warn",
		Title: "A deployment is in progress but the machine hasn't checked in for over " + humanDuration(deployStallAfter) + " — Setup may have failed or the machine is off."},
	{Key: statusReimage, Label: "Re-image pending", Dot: "warn",
		Title: "A re-image is queued and waits for the machine to reboot into AutoDeploy."},
	{Key: statusDeploying, Label: "Deploying", Dot: "accent",
		Title: "A deployment is running now."},
	{Key: statusOffline, Label: "Offline", Dot: "off",
		Title: "No check-in for over " + humanDuration(machineOfflineAfter) + "."},
	{Key: statusIdle, Label: "Idle", Dot: "muted",
		Title: "Last check-in between " + humanDuration(machineOnlineWithin) + " and " + humanDuration(machineOfflineAfter) + " ago — usually switched off or asleep."},
	{Key: statusOnline, Label: "Online", Dot: "ok",
		Title: "Checked in within the last " + humanDuration(machineOnlineWithin) + "."},
}

func init() {
	for i := range machineStatuses {
		machineStatuses[i].Rank = i
	}
}

// statusByKey looks a status up by its key (ok=false for an unknown key).
func statusByKey(key string) (machineStatus, bool) {
	for _, s := range machineStatuses {
		if s.Key == key {
			return s, true
		}
	}
	return machineStatus{}, false
}

// deriveMachineStatus picks a machine's status. Deployment state wins over
// check-in age because it is what the operator has to act on: an active
// run is Deploying (or Stalled when the machine went quiet mid-run), then a
// queued re-image, then a failed latest run; only a machine with nothing
// pending is Online / Idle / Offline by how recently it checked in.
func deriveMachineStatus(now, lastSeen time.Time, latest *model.DeploymentRecord, reimagePending bool) machineStatus {
	quiet := now.Sub(lastSeen)
	key := ""
	switch {
	case latest != nil && latest.Outcome == "in_progress" && quiet > deployStallAfter:
		key = statusStalled
	case latest != nil && latest.Outcome == "in_progress":
		key = statusDeploying
	case reimagePending:
		key = statusReimage
	case latest != nil && latest.Outcome == "failed":
		key = statusFailed
	case quiet > machineOfflineAfter:
		key = statusOffline
	case quiet <= machineOnlineWithin:
		key = statusOnline
	default:
		key = statusIdle
	}
	s, _ := statusByKey(key)
	return s
}

// humanDuration renders a threshold for a tooltip: "15 minutes", "45
// minutes", "7 days".
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour && d%(24*time.Hour) == 0:
		return strconv.Itoa(int(d/(24*time.Hour))) + " days"
	case d >= 2*time.Hour && d%time.Hour == 0:
		return strconv.Itoa(int(d/time.Hour)) + " hours"
	default:
		return strconv.Itoa(int(d/time.Minute)) + " minutes"
	}
}

// fleetFacts is everything the machine list, its CSV export and the
// dashboard need about a set of machines, loaded with one query each.
type fleetFacts struct {
	Bindings   map[model.ID]model.MachineBinding
	ImageNames map[model.ID]string
	OS         map[model.ID]string
	Latest     map[model.ID]model.DeploymentRecord
	Reimage    map[model.ID]bool
	Status     map[model.ID]machineStatus
}

// loadFleetFacts loads bindings, image names, OS captions, latest
// deployments and re-image flags, then derives each machine's status.
func loadFleetFacts(req *http.Request, r Repos, ms []model.MachineRecord, now time.Time) fleetFacts {
	f := fleetFacts{}
	f.Bindings, f.ImageNames = loadMachineBindings(req, r, ms)
	f.OS, _ = r.Inventory.ListOSCaptions(req.Context())
	f.Latest, _ = r.Inventory.LatestDeployments(req.Context())
	f.Reimage, _ = r.Inventory.ReimagePendingSet(req.Context())
	f.Status = make(map[model.ID]machineStatus, len(ms))
	for _, m := range ms {
		var latest *model.DeploymentRecord
		if d, ok := f.Latest[m.ID]; ok {
			latest = &d
		}
		f.Status[m.ID] = deriveMachineStatus(now, m.LastSeen, latest, f.Reimage[m.ID])
	}
	return f
}

// Machine list filters. Each is a plain query parameter, so a filtered
// view is a bookmarkable URL, survives sorting / paging / switching group
// (withQuery keeps them) and rides along into the CSV export.
type machineFilters struct {
	Status string // a status key
	Image  string // an image id, or "none"
	AD     string // joined | join_failed | trust_broken | problem | none
	Model  string // "Manufacturer Product" exactly as shown
	Since  string // latest deployment started within: 24h | 7d | 30d
}

// machineFilterNames are the query parameters machineFilters reads.
var machineFilterNames = []string{"status", "image", "ad", "model", "since"}

// machineSinceWindows maps ?since= to a duration and its label.
var machineSinceWindows = []struct {
	Key   string
	Dur   time.Duration
	Label string
}{
	{"24h", 24 * time.Hour, "Last 24 hours"},
	{"7d", 7 * 24 * time.Hour, "Last 7 days"},
	{"30d", 30 * 24 * time.Hour, "Last 30 days"},
}

// adFilterOptions are the AD join choices, in display order.
var adFilterOptions = []struct{ Value, Label string }{
	{"problem", "Any problem"},
	{model.ADJoinJoined, "Joined"},
	{model.ADJoinFailed, "Join failed"},
	{model.ADTrustBroken, "Trust broken"},
	{"none", "Not reported"},
}

func parseMachineFilters(req *http.Request) machineFilters {
	q := req.URL.Query()
	return machineFilters{
		Status: strings.TrimSpace(q.Get("status")),
		Image:  strings.TrimSpace(q.Get("image")),
		AD:     strings.TrimSpace(q.Get("ad")),
		Model:  strings.TrimSpace(q.Get("model")),
		Since:  strings.TrimSpace(q.Get("since")),
	}
}

// Active reports whether any filter is set.
func (f machineFilters) Active() bool {
	return f.Status != "" || f.Image != "" || f.AD != "" || f.Model != "" || f.Since != ""
}

// machineModel is the make/model string the Model filter matches.
func machineModel(m model.MachineRecord) string {
	return strings.TrimSpace(m.SystemManufacturer + " " + m.SystemProduct)
}

// match reports whether machine m passes every set filter. Unknown filter
// values match nothing rather than erroring, so a stale bookmark shows an
// empty list with a Clear link.
func (f machineFilters) match(m model.MachineRecord, facts fleetFacts, now time.Time) bool {
	if f.Status != "" && facts.Status[m.ID].Key != f.Status {
		return false
	}
	if f.Image != "" {
		b := facts.Bindings[m.ID]
		if f.Image == "none" {
			if b.ImageID != nil {
				return false
			}
		} else if b.ImageID == nil || strconv.FormatInt(int64(*b.ImageID), 10) != f.Image {
			return false
		}
	}
	if f.AD != "" {
		switch f.AD {
		case "problem":
			if !model.ADJoinFailure(m.ADJoinStatus) {
				return false
			}
		case "none":
			if m.ADJoinStatus != "" {
				return false
			}
		default:
			if m.ADJoinStatus != f.AD {
				return false
			}
		}
	}
	if f.Model != "" && !strings.EqualFold(machineModel(m), f.Model) {
		return false
	}
	if f.Since != "" {
		d, ok := facts.Latest[m.ID]
		if !ok {
			return false
		}
		within := time.Duration(-1)
		for _, w := range machineSinceWindows {
			if w.Key == f.Since {
				within = w.Dur
			}
		}
		if within < 0 || now.Sub(d.StartedAt) > within {
			return false
		}
	}
	return true
}

// apply keeps the machines that pass every filter.
func (f machineFilters) apply(ms []model.MachineRecord, facts fleetFacts, now time.Time) []model.MachineRecord {
	if !f.Active() {
		return ms
	}
	out := make([]model.MachineRecord, 0, len(ms))
	for _, m := range ms {
		if f.match(m, facts, now) {
			out = append(out, m)
		}
	}
	return out
}

// filterOption is one <option> of a filter select.
type filterOption struct {
	Value, Label string
	Selected     bool
}

// filterSelect is one filter control under the machine search box.
type filterSelect struct {
	Name, Label, Any string
	Options          []filterOption
	Active           bool
}

// statusChip is one quick status filter link with its count.
type statusChip struct {
	machineStatus
	Count int
	URL   string
	On    bool
}

// machineFilterView builds the status chips (with counts over ms, the
// group before any filter) and the image / AD / model / last-deployment
// selects for the list page.
func machineFilterView(req *http.Request, f machineFilters, ms []model.MachineRecord, facts fleetFacts) ([]statusChip, []filterSelect) {
	counts := map[string]int{}
	models := map[string]int{}
	images := map[model.ID]bool{}
	for _, m := range ms {
		counts[facts.Status[m.ID].Key]++
		if mm := machineModel(m); mm != "" {
			models[mm]++
		}
		if b := facts.Bindings[m.ID]; b.ImageID != nil {
			images[*b.ImageID] = true
		}
	}
	chips := make([]statusChip, 0, len(machineStatuses))
	for _, s := range machineStatuses {
		if counts[s.Key] == 0 && f.Status != s.Key {
			continue
		}
		val := s.Key
		if f.Status == s.Key {
			val = "" // clicking the active chip clears it
		}
		chips = append(chips, statusChip{machineStatus: s, Count: counts[s.Key],
			URL: withQuery(req, req.URL.Path, "status", val), On: f.Status == s.Key})
	}

	imgOpts := make([]filterOption, 0, len(images)+1)
	for id := range images {
		v := strconv.FormatInt(int64(id), 10)
		imgOpts = append(imgOpts, filterOption{Value: v, Label: facts.ImageNames[id], Selected: f.Image == v})
	}
	sort.Slice(imgOpts, func(i, j int) bool { return strings.ToLower(imgOpts[i].Label) < strings.ToLower(imgOpts[j].Label) })
	imgOpts = append(imgOpts, filterOption{Value: "none", Label: "No image bound", Selected: f.Image == "none"})

	adOpts := make([]filterOption, 0, len(adFilterOptions))
	for _, o := range adFilterOptions {
		adOpts = append(adOpts, filterOption{Value: o.Value, Label: o.Label, Selected: f.AD == o.Value})
	}

	modelNames := make([]string, 0, len(models))
	for mm := range models {
		modelNames = append(modelNames, mm)
	}
	sort.Strings(modelNames)
	modelOpts := make([]filterOption, 0, len(modelNames)+1)
	seen := false
	for _, mm := range modelNames {
		sel := strings.EqualFold(f.Model, mm)
		seen = seen || sel
		modelOpts = append(modelOpts, filterOption{Value: mm, Label: mm, Selected: sel})
	}
	if f.Model != "" && !seen { // keep a bookmarked model visible even if none match now
		modelOpts = append(modelOpts, filterOption{Value: f.Model, Label: f.Model, Selected: true})
	}

	sinceOpts := make([]filterOption, 0, len(machineSinceWindows))
	for _, w := range machineSinceWindows {
		sinceOpts = append(sinceOpts, filterOption{Value: w.Key, Label: w.Label, Selected: f.Since == w.Key})
	}

	selects := []filterSelect{
		{Name: "image", Label: "Image", Any: "Any image", Options: imgOpts, Active: f.Image != ""},
		{Name: "ad", Label: "AD join", Any: "Any AD state", Options: adOpts, Active: f.AD != ""},
		{Name: "model", Label: "Model", Any: "Any model", Options: modelOpts, Active: f.Model != ""},
		{Name: "since", Label: "Last deployed", Any: "Any time", Options: sinceOpts, Active: f.Since != ""},
	}
	return chips, selects
}
