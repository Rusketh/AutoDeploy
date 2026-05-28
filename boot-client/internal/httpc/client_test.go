package httpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostJSON(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["uuid_header"] = r.Header.Get("X-AutoDeploy-Machine-UUID")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "uuid-1234", false)
	var out map[string]any
	if err := c.PostJSON(context.Background(), "/echo",
		map[string]any{"hello": "world"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["hello"] != "world" {
		t.Errorf("server didn't see request body: %+v", out)
	}
	if out["uuid_header"] != "uuid-1234" {
		t.Errorf("UUID header missing: %+v", out)
	}
}

func TestPostJSONErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	err := c.PostJSON(context.Background(), "/anything", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 error, got %v", err)
	}
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello-payload"))
	}))
	defer srv.Close()
	c := New(srv.URL, "x", false)
	var buf strings.Builder
	if err := c.Download(context.Background(), srv.URL+"/x", &buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "hello-payload" {
		t.Errorf("body = %q", buf.String())
	}
}
