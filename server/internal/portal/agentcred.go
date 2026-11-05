package portal

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Agent credentials on the machine page: the machine's secret state, any
// requests for a secret awaiting an operator, and approve / deny / revoke.

func init() {
	registerAgentCredentialRoutes = func(get, post func(string, http.HandlerFunc), r Repos) {
		post("/portal/machines/{id}/credential/requests/{rid}/approve", credentialDecide(r, true))
		post("/portal/machines/{id}/credential/requests/{rid}/deny", credentialDecide(r, false))
		post("/portal/machines/{id}/credential/revoke", credentialRevoke(r))
		post("/portal/images/{id}/media-enrolment/revoke", mediaEnrolmentRevoke(r))
		post("/portal/machines/credential/approve-all", credentialApproveAll(r))
	}
}

// credentialView is what the machine page's "Agent secret" panel shows.
type credentialView struct {
	model.AgentCredential
	Requests []model.CredentialRequest
	Pending  int
}

func machineCredentialView(ctx context.Context, r Repos, machineID model.ID) *credentialView {
	if r.Credentials == nil {
		return nil
	}
	c, err := r.Credentials.Get(ctx, machineID)
	if err != nil {
		return nil
	}
	v := &credentialView{AgentCredential: c}
	v.Requests, _ = r.Credentials.Requests(ctx, machineID, 5)
	for _, q := range v.Requests {
		if q.Status == model.CredRequestPending {
			v.Pending++
		}
	}
	return v
}

func credentialAnchor(id model.ID) string {
	return fmt.Sprintf("/portal/machines/%d#agent-credential", int64(id))
}

func credentialDecide(r Repos, approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		rid, _ := strconv.ParseInt(req.PathValue("rid"), 10, 64)
		if r.Credentials == nil {
			failPage(w, req, r, http.StatusNotFound, fmt.Errorf("agent credentials are not configured"))
			return
		}
		cr, err := r.Credentials.Decide(req.Context(), model.ID(rid), approve, portalUsername(req))
		switch {
		case err != nil:
			flash(w, "err", "Couldn't record the decision: "+friendlyError("machine", http.StatusConflict, err))
		case cr.MachineID != id:
			flash(w, "err", "That request belongs to a different machine.")
		case approve:
			flash(w, "ok", "Approved. The agent picks up its new secret at its next check-in; any secret it had before stops working.")
		default:
			flash(w, "ok", "Denied. That requester won't get a secret.")
		}
		http.Redirect(w, req, credentialAnchor(id), http.StatusFound)
	}
}

func credentialRevoke(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if r.Credentials == nil {
			failPage(w, req, r, http.StatusNotFound, fmt.Errorf("agent credentials are not configured"))
			return
		}
		if err := r.Credentials.Revoke(req.Context(), id); err != nil {
			flash(w, "err", "Couldn't revoke: "+err.Error())
		} else {
			flash(w, "ok", "Secret revoked. The agent is refused until you approve its next request for a secret.")
		}
		http.Redirect(w, req, credentialAnchor(id), http.StatusFound)
	}
}

// mediaEnrolmentRevoke invalidates every USB/ISO exported from the image (a
// lost stick); the next export carries a new enrolment.
func mediaEnrolmentRevoke(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, _ := pathID(req)
		if r.Credentials == nil {
			failPage(w, req, r, http.StatusNotFound, fmt.Errorf("agent credentials are not configured"))
			return
		}
		if err := r.Credentials.RevokeImageEnrollToken(req.Context(), id); err != nil {
			flash(w, "err", "Couldn't revoke: "+err.Error())
		} else {
			flash(w, "ok", "Media enrolment revoked. Sticks exported earlier can no longer enrol machines; export again to make new ones.")
		}
		http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/resolved#media-enrolment", int64(id)), http.StatusFound)
	}
}

// credentialApproveAll approves every machine's single pending request
// (machines with competing requests are left for a per-machine decision).
func credentialApproveAll(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if r.Credentials == nil {
			failPage(w, req, r, http.StatusNotFound, fmt.Errorf("agent credentials are not configured"))
			return
		}
		n, skipped, err := r.Credentials.ApproveAllPending(req.Context(), portalUsername(req))
		switch {
		case err != nil:
			flash(w, "err", "Couldn't approve: "+err.Error())
		case skipped > 0:
			flash(w, "warn", fmt.Sprintf("Approved %d request%s. %d machine%s have more than one request: open each to choose which to approve.",
				n, plural(n), skipped, plural(skipped)))
		default:
			flash(w, "ok", fmt.Sprintf("Approved %d request%s. The agents pick up their secrets at their next check-in.", n, plural(n)))
		}
		http.Redirect(w, req, "/portal/machines", http.StatusFound)
	}
}
