package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// AgentSoftwareRequest is what the agent POSTs to /api/v1/agent/software.
// The agent reports its identity (so the server can look it up in
// inventory once Phase 8 lands) and the image id it was deployed from.
// Loadout resolution (Phase 7) extends the response; in Phase 6 the
// effective set is the direct image links plus all matched drivers'
// implicit software (none yet).
type AgentSoftwareRequest struct {
	ImageID  model.ID       `json:"image_id"`
	Identity match.Identity `json:"identity"`
}

// AgentSoftwareItem is one software package the agent should evaluate.
type AgentSoftwareItem struct {
	PackageID  model.ID `json:"package_id"`
	Name       string   `json:"name"`
	OrderValue int64    `json:"order_value"`
	// PayloadURL is the legacy single-file payload endpoint. Kept
	// for backward compat with agents that pre-date multi-file
	// support; the file-list form (Files) is the new path.
	PayloadURL string `json:"payload_url"`
	// Files lists every file the agent should download for this
	// package, addressed by name relative to a per-package work
	// directory. install_steps that contain bare filenames are
	// resolved against this set on the agent.
	Files          []AgentPackageFile     `json:"files,omitempty"`
	DetectionRules []swspec.DetectionRule `json:"detection_rules"`
	InstallSteps   []swspec.InstallStep   `json:"install_steps"`
}

// AgentPackageFile is one downloadable file in a multi-file package.
type AgentPackageFile struct {
	Name      string `json:"name"` // the filename the operator uploaded
	URL       string `json:"url"`  // GET this for the bytes
	SizeBytes int64  `json:"size_bytes"`
}

// AgentSoftwareResponse carries the effective ordered software set.
type AgentSoftwareResponse struct {
	Items    []AgentSoftwareItem `json:"items"`
	Warnings []string            `json:"warnings,omitempty"`
}

// AgentSelfResponse is the machine's desired state, resolved server-side
// from its AutoDeploy agent_id. The resident agent polls GET
// /api/v1/agent/self?id=<agent_id> for this -- it carries everything the
// machine should act on (software set for its bound image + pending jobs),
// so the agent needs nothing but the server address and its own id.
type AgentSelfResponse struct {
	MachineID model.ID            `json:"machine_id"`
	AgentID   string              `json:"agent_id"`
	ImageID   model.ID            `json:"image_id,omitempty"`
	Software  []AgentSoftwareItem `json:"software"`
	Jobs      []model.BulkJob     `json:"jobs"`
	Warnings  []string            `json:"warnings,omitempty"`
	// PollIntervalSeconds is the operator-configured check-in cadence; the
	// agent resets its poll ticker to this on each response. Omitted (0) when
	// unset, in which case the agent keeps its current interval.
	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty"`
}

// RegisterAgent mounts the agent endpoints.
func RegisterAgent(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/agent/software", handleAgentSoftware(r))
	mux.HandleFunc("GET /api/v1/agent/self", handleAgentSelf(r))
	mux.HandleFunc("GET /api/v1/agent/package-items", handleAgentPackageItems(r))
}

// handleAgentPackageItems expands a single software package (plus its
// transitive dependencies, deps first) into install items. Used by the
// agent when it runs a bulk "software_push" job, which references a
// package by id rather than carrying raw steps.
func handleAgentPackageItems(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		pid, err := strconv.ParseInt(req.URL.Query().Get("package"), 10, 64)
		if err != nil || pid <= 0 {
			http.Error(w, "package query parameter required", http.StatusBadRequest)
			return
		}
		items, warnings := expandSoftwareItems(req.Context(), r, []model.ID{model.ID(pid)})
		writeJSON(w, http.StatusOK, AgentSoftwareResponse{Items: items, Warnings: warnings})
	}
}

func handleAgentSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentSoftwareRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		items, warnings, err := buildSoftwareItems(req, r, in.ImageID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, AgentSoftwareResponse{Items: items, Warnings: warnings})
	}
}

// handleAgentSelf resolves a machine's full desired state from its
// server-minted agent_id: the software set for its bound image plus any
// pending bulk jobs. This is the single endpoint the resident agent polls.
func handleAgentSelf(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		agentID := strings.TrimSpace(req.URL.Query().Get("id"))
		if agentID == "" {
			http.Error(w, "id query parameter required", http.StatusBadRequest)
			return
		}
		m, err := r.Inventory.GetByAgentID(req.Context(), agentID)
		if err != nil {
			writeError(w, err) // 404 for an unknown id
			return
		}
		resp := AgentSelfResponse{
			MachineID: m.ID,
			AgentID:   m.AgentID,
			Software:  []AgentSoftwareItem{},
			Jobs:      []model.BulkJob{},
		}
		// Advertise the operator-configured check-in cadence so the agent
		// adjusts its poll interval live (no reinstall needed to speed it up
		// for testing or slow it down for a large fleet).
		if r.Runtime != nil {
			resp.PollIntervalSeconds = r.Runtime.AgentPollIntervalSeconds()
		}
		// Software comes from the machine's bound image. No binding (or no
		// image) just means "nothing to install" -- not an error.
		if b, berr := r.Inventory.GetBinding(req.Context(), m.ID); berr == nil && b.ImageID != nil {
			resp.ImageID = *b.ImageID
			items, warnings, serr := buildSoftwareItems(req, r, *b.ImageID)
			if serr != nil {
				resp.Warnings = append(resp.Warnings, serr.Error())
			} else {
				resp.Software = items
				resp.Warnings = append(resp.Warnings, warnings...)
			}
		}
		// Pending jobs, claimed the same way checkin does.
		jobs, jerr := r.Bulk.ClaimJobsFor(req.Context(), m.ID, 8)
		if jerr != nil {
			writeError(w, jerr)
			return
		}
		if jobs != nil {
			resp.Jobs = jobs
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// buildSoftwareItems resolves an image's effective software set into agent
// items, expanding package dependencies. The returned error is the hard
// resolver failure; per-package issues are returned as warnings.
func buildSoftwareItems(req *http.Request, r Repos, imageID model.ID) ([]AgentSoftwareItem, []string, error) {
	res, err := r.Resolver.Resolve(req.Context(), imageID)
	if err != nil {
		return nil, nil, err
	}
	seeds := make([]model.ID, 0, len(res.Software))
	for _, link := range res.Software {
		seeds = append(seeds, link.PackageID)
	}
	items, warnings := expandSoftwareItems(req.Context(), r, seeds)
	return items, append(res.Diagnostics, warnings...), nil
}

// expandSoftwareItems turns seed package IDs into ordered install items:
// every seed plus its transitive dependencies (deps first), built into the
// agent's AgentSoftwareItem shape. The slice order is the install order.
func expandSoftwareItems(ctx context.Context, r Repos, seeds []model.ID) ([]AgentSoftwareItem, []string) {
	ordered, warnings := r.Software.ResolveOrder(ctx, seeds)
	base := "/payload/software/"
	items := make([]AgentSoftwareItem, 0, len(ordered))
	for i, id := range ordered {
		pkg, err := r.Software.Get(ctx, id)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		det, derr := swspec.ParseDetection(pkg.DetectionJSON)
		if derr != nil {
			warnings = append(warnings, "package "+pkg.Name+": "+derr.Error())
		}
		steps, serr := swspec.ParseSteps(pkg.StepsJSON)
		if serr != nil {
			warnings = append(warnings, "package "+pkg.Name+": "+serr.Error())
		}
		// Per-package files: software/<id>/files/<name>, downloaded by the
		// agent and resolved against its workdir. None = scriptless.
		files := listPackageFiles(r, pkg.ID, base)
		items = append(items, AgentSoftwareItem{
			PackageID:      pkg.ID,
			Name:           pkg.Name,
			OrderValue:     int64(i), // dependency-aware install order
			PayloadURL:     base + idStr(pkg.ID),
			Files:          files,
			DetectionRules: det,
			InstallSteps:   steps,
		})
	}
	return items, warnings
}

// listPackageFiles enumerates a software package's files directory
// and produces AgentPackageFile entries the agent can download. The
// URL points at the public payload endpoint (served by the payload
// package); the filename round-trips intact so the agent can save it
// back under the same name in its workdir.
func listPackageFiles(r Repos, id model.ID, base string) []AgentPackageFile {
	if r.Blobs == nil {
		return nil
	}
	rel := "software/" + idStr(id) + "/files"
	entries, err := r.Blobs.ListDir(rel)
	if err != nil || len(entries) == 0 {
		return nil
	}
	out := make([]AgentPackageFile, 0, len(entries))
	for _, e := range entries {
		out = append(out, AgentPackageFile{
			Name:      e.Name,
			URL:       base + idStr(id) + "/files/" + e.Name,
			SizeBytes: e.Size,
		})
	}
	return out
}

func idStr(id model.ID) string {
	// fmt.Sprint would do; tiny helper to avoid the fmt import.
	if id == 0 {
		return "0"
	}
	n := int64(id)
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
