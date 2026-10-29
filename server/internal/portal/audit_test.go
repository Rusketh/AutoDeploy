package portal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// The audit wrapper records who changed what, reads success/failure from the
// handler's redirect and flash, names deleted rows by their pre-delete name
// and finds a created row's ID in the redirect.
func TestAuditedRecordsPortalChanges(t *testing.T) {
	ctx := context.Background()
	db, inv := namesTestDB(t)
	images := model.NewImageRepo(db)
	img, err := images.Create(ctx, model.Image{Name: "Kiosk"})
	if err != nil {
		t.Fatal(err)
	}
	r := Repos{Inventory: inv, Logs: model.NewLogRepo(db)}
	mux := http.NewServeMux()
	reg := func(path string, h http.HandlerFunc) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, req *http.Request) {
			c := context.WithValue(req.Context(), ctxKeyUser{}, auth.User{Username: "alice"})
			audited(r, path, h)(w, req.WithContext(c))
		})
	}
	reg("/portal/images/{id}/delete", func(w http.ResponseWriter, req *http.Request) {
		_ = images.Delete(req.Context(), img.ID)
		flash(w, "ok", "Deleted.")
		http.Redirect(w, req, "/portal/images", http.StatusFound)
	})
	reg("/portal/images", func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		if req.PostFormValue("name") == "" {
			flash(w, "err", "name is required")
			http.Redirect(w, req, "/portal/images/new", http.StatusFound)
			return
		}
		out, _ := images.Create(req.Context(), model.Image{Name: req.PostFormValue("name")})
		http.Redirect(w, req, fmt.Sprintf("/portal/images/%d/edit", int64(out.ID)), http.StatusFound)
	})
	do := func(path string, form url.Values) {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}
	do("/portal/images/1/delete", nil)
	do("/portal/images", url.Values{"name": {""}})
	do("/portal/images", url.Values{"name": {"Office"}})

	evs, err := r.Logs.Search(ctx, model.LogSearch{ByID: true})
	if err != nil || len(evs) != 3 {
		t.Fatalf("audit rows: %v %+v", err, evs)
	}
	created, rejected, deleted := evs[0], evs[1], evs[2]
	if deleted.Action != "image.delete" || deleted.User != "alice" || deleted.Message != "alice deleted image “Kiosk”" || deleted.Outcome != "ok" {
		t.Errorf("delete row: %+v", deleted)
	}
	if rejected.Level != "WARN" || rejected.Outcome != "failed" || !strings.Contains(rejected.Message, "not saved: name is required") {
		t.Errorf("rejected row: %+v", rejected)
	}
	if created.Action != "image.create" || created.Target != "image:2" || created.Message != "alice created image Office" {
		t.Errorf("create row: %+v", created)
	}
	// Unaudited routes are passed through untouched.
	h := func(http.ResponseWriter, *http.Request) {}
	if got := audited(r, "/portal/drivers/{id}/preview", h); got == nil {
		t.Fatal("nil handler")
	}
}

func TestAuditFailureMessageReadsAsEnglish(t *testing.T) {
	for _, tc := range []struct{ kind, verb, want string }{
		{"machine", "binding.update", `admin tried to update the binding of machine “LAB-01” — not saved`},
		{"machine", "reimage.cancel", `admin tried to cancel the re-image of machine “LAB-01” — not saved`},
		{"machine", "some_new.verb", `admin tried to some new verb machine “LAB-01” — not saved`},
	} {
		got := auditFailureMessage("admin", tc.kind, tc.verb, "LAB-01", "")
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("auditFailureMessage(%q, %q) = %q, want prefix %q", tc.kind, tc.verb, got, tc.want)
		}
	}
}
