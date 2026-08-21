package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/resolve"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func newTestServer(t *testing.T) (*httptest.Server, Repos) {
	t.Helper()
	db, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	isos := model.NewISORepo(db)
	unattend := model.NewUnattendRepo(db)
	drivers := model.NewDriverPackageRepo(db)
	software := model.NewSoftwarePackageRepo(db)
	images := model.NewImageRepo(db)
	users := auth.New(db)
	repos := Repos{
		ISOs: isos, Unattend: unattend, Drivers: drivers,
		Software: software, Images: images,
		Resolver: resolve.New(images, isos, unattend),
		Users:    users,
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func authedClient(t *testing.T, srv *httptest.Server, repos Repos) *http.Client {
	t.Helper()
	// The login rate limiter is a package-global keyed by client IP; every
	// test authenticates from 127.0.0.1, so without a reset the cumulative
	// login count across the package's tests eventually trips the 429 cap.
	// Clear it per authenticated client so login attempts never accumulate
	// across tests.
	loginLimiter.mu.Lock()
	loginLimiter.counts = map[string]int{}
	loginLimiter.resetAt = time.Time{}
	loginLimiter.mu.Unlock()

	ctx := context.Background()
	if _, err := repos.Users.CreateUser(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	body := `{"username":"admin","password":"admin"}`
	resp, err := client.Post(srv.URL+"/api/v1/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}
	return client
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// CSRF defence: state-mutating requests require X-Requested-With.
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete:
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestISOCRUDOverHTTP(t *testing.T) {
	srv, repos := newTestServer(t)
	client := authedClient(t, srv, repos)

	// Create.
	resp, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
		"name":    "Win11",
		"os_type": "windows-11",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d body=%s", resp.StatusCode, body)
	}
	var created model.ISO
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "Win11" {
		t.Errorf("unexpected create body: %+v", created)
	}

	// List.
	resp, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/v1/isos", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Win11") {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, body)
	}

	// Duplicate name should be 409.
	resp, _ = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
		"name": "Win11", "os_type": "windows-11",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d", resp.StatusCode)
	}

	// Missing os_type should be 400.
	resp, _ = doJSON(t, client, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
		"name": "Bad",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 on missing os_type, got %d", resp.StatusCode)
	}

	// Delete.
	url := srv.URL + "/api/v1/isos/" + jsonInt(created.ID)
	resp, _ = doJSON(t, client, http.MethodDelete, url, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
}

func TestImageResolveOverHTTP(t *testing.T) {
	srv, repos := newTestServer(t)
	client := authedClient(t, srv, repos)

	mustCreate := func(path string, payload any) []byte {
		t.Helper()
		resp, body := doJSON(t, client, http.MethodPost, srv.URL+path, payload)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s status=%d body=%s", path, resp.StatusCode, body)
		}
		return body
	}
	var iso model.ISO
	_ = json.Unmarshal(mustCreate("/api/v1/isos", map[string]any{"name": "Win11", "os_type": "windows-11"}), &iso)
	var ua model.Unattend
	_ = json.Unmarshal(mustCreate("/api/v1/unattends", map[string]any{"name": "default-ua"}), &ua)

	rootBody := mustCreate("/api/v1/images", map[string]any{
		"name":        "root",
		"iso_id":      iso.ID,
		"unattend_id": ua.ID,
	})
	var root model.Image
	_ = json.Unmarshal(rootBody, &root)

	_ = mustCreate("/api/v1/images", map[string]any{
		"name":      "child",
		"parent_id": root.ID,
	})

	// Resolve the child: should inherit ISO and unattend from root.
	resp, body := doJSON(t, client, http.MethodGet,
		srv.URL+"/api/v1/images/"+jsonInt(root.ID+1)+"/resolved", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"Win11"`) {
		t.Errorf("resolved missing inherited ISO: %s", body)
	}
}

func jsonInt(id model.ID) string {
	b, _ := json.Marshal(id)
	return strings.Trim(string(b), `"`)
}
