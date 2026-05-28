package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	repos := Repos{
		ISOs: isos, Unattend: unattend, Drivers: drivers,
		Software: software, Images: images,
		Resolver: resolve.New(images, isos, unattend),
	}
	mux := http.NewServeMux()
	Register(mux, repos)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, repos
}

func doJSON(t *testing.T, method, url string, body any) (*http.Response, []byte) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestISOCRUDOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)

	// Create.
	resp, body := doJSON(t, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
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
	resp, body = doJSON(t, http.MethodGet, srv.URL+"/api/v1/isos", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Win11") {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, body)
	}

	// Duplicate name should be 409.
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
		"name": "Win11", "os_type": "windows-11",
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d", resp.StatusCode)
	}

	// Missing os_type should be 400.
	resp, _ = doJSON(t, http.MethodPost, srv.URL+"/api/v1/isos", map[string]any{
		"name": "Bad",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 on missing os_type, got %d", resp.StatusCode)
	}

	// Delete.
	url := srv.URL + "/api/v1/isos/" + jsonInt(created.ID)
	resp, _ = doJSON(t, http.MethodDelete, url, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete status = %d", resp.StatusCode)
	}
}

func TestImageResolveOverHTTP(t *testing.T) {
	srv, _ := newTestServer(t)

	mustCreate := func(path string, payload any) []byte {
		t.Helper()
		resp, body := doJSON(t, http.MethodPost, srv.URL+path, payload)
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
	resp, body := doJSON(t, http.MethodGet,
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
