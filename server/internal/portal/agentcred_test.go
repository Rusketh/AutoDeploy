package portal

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestMachineAgentSecretPanelAndApproval(t *testing.T) {
	ctx := context.Background()
	tp := newTestPortal(t, func(r *Repos, db *storage.DB, _ *secrets.Box) {
		r.Credentials = model.NewAgentCredentialRepo(db)
	})
	m, _ := tp.r.Inventory.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "PORTAL-CRED"})
	mid := strconv.FormatInt(int64(m.ID), 10)

	if page := tp.get("/portal/machines/" + mid); !strings.Contains(page, "Legacy") {
		t.Fatal("legacy machine not shown as legacy")
	}
	if list := tp.get("/portal/machines"); !strings.Contains(list, "legacy agent authentication") {
		t.Fatal("machine list doesn't count legacy agents")
	}

	secret, _ := tp.r.Credentials.Issue(ctx, m.ID, "first-contact", false)
	page := tp.get("/portal/machines/" + mid)
	if !strings.Contains(page, "Secured") || strings.Contains(page, secret) {
		t.Fatal("secured panel wrong (or leaked the secret)")
	}

	// A second requester: pending, listed with approve/deny, counted on the list.
	req, _, _ := tp.r.Credentials.RequestCredential(ctx, m.ID, "nonce-portal-000000000000000000000000000", "this machine already has a secret", "10.1.2.3")
	page = tp.get("/portal/machines/" + mid)
	if !strings.Contains(page, "10.1.2.3") || !strings.Contains(page, "/credential/requests/"+strconv.FormatInt(int64(req.ID), 10)+"/approve") {
		t.Fatal("pending request not offered for approval")
	}
	if list := tp.get("/portal/machines"); !strings.Contains(list, "your approval") {
		t.Fatal("machine list doesn't flag the pending request")
	}
	rec := tp.do("POST", "/portal/machines/"+mid+"/credential/requests/"+strconv.FormatInt(int64(req.ID), 10)+"/approve", url.Values{}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("approve: %d", rec.Code)
	}
	got, _, _ := tp.r.Credentials.RequestCredential(ctx, m.ID, "nonce-portal-000000000000000000000000000", "", "")
	if got.Status != model.CredRequestApproved || got.DecidedBy != "admin" {
		t.Fatalf("decision not recorded: %+v", got)
	}

	// Revoke: the secret stops working; the panel says approval is required.
	rec = tp.do("POST", "/portal/machines/"+mid+"/credential/revoke", url.Values{}, nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if ok, _ := tp.r.Credentials.Verify(ctx, m.ID, secret); ok {
		t.Fatal("revoked secret still valid")
	}
	if page := tp.get("/portal/machines/" + mid); !strings.Contains(page, "Approval required") {
		t.Fatal("panel doesn't show approval required")
	}
}
