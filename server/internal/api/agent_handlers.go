package api

import (
	"net/http"

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
	PackageID     model.ID                 `json:"package_id"`
	Name          string                   `json:"name"`
	OrderValue    int64                    `json:"order_value"`
	PayloadURL    string                   `json:"payload_url"`
	DetectionRules []swspec.DetectionRule  `json:"detection_rules"`
	InstallSteps   []swspec.InstallStep    `json:"install_steps"`
}

// AgentSoftwareResponse carries the effective ordered software set.
type AgentSoftwareResponse struct {
	Items    []AgentSoftwareItem `json:"items"`
	Warnings []string            `json:"warnings,omitempty"`
}

// RegisterAgent mounts the agent endpoints.
func RegisterAgent(mux *http.ServeMux, r Repos) {
	mux.HandleFunc("POST /api/v1/agent/software", handleAgentSoftware(r))
}

func handleAgentSoftware(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in AgentSoftwareRequest
		if err := decodeJSON(req, &in); err != nil {
			writeError(w, err)
			return
		}
		res, err := r.Resolver.Resolve(req.Context(), in.ImageID)
		if err != nil {
			writeError(w, err)
			return
		}
		resp := AgentSoftwareResponse{Warnings: res.Diagnostics}
		base := "/payload/software/"
		for _, link := range res.Software {
			pkg, err := r.Software.Get(req.Context(), link.PackageID)
			if err != nil {
				resp.Warnings = append(resp.Warnings, err.Error())
				continue
			}
			det, derr := swspec.ParseDetection(pkg.DetectionJSON)
			if derr != nil {
				resp.Warnings = append(resp.Warnings, "package "+pkg.Name+": "+derr.Error())
			}
			steps, serr := swspec.ParseSteps(pkg.StepsJSON)
			if serr != nil {
				resp.Warnings = append(resp.Warnings, "package "+pkg.Name+": "+serr.Error())
			}
			resp.Items = append(resp.Items, AgentSoftwareItem{
				PackageID:      pkg.ID,
				Name:           pkg.Name,
				OrderValue:     link.OrderValue,
				PayloadURL:     base + idStr(pkg.ID),
				DetectionRules: det,
				InstallSteps:   steps,
			})
		}
		writeJSON(w, http.StatusOK, resp)
	}
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
