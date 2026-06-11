package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A manual install (--server only) must acquire the machine's
// server-minted agent_id from /api/v1/agent/enroll.
func TestEnsureEnrolled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent/enroll" {
			http.NotFound(w, r)
			return
		}
		var in struct {
			Identity struct {
				SystemUUID string `json:"system_uuid"`
			} `json:"identity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if in.Identity.SystemUUID != "test-uuid" {
			t.Errorf("enroll identity = %q", in.Identity.SystemUUID)
		}
		json.NewEncoder(w).Encode(map[string]any{"machine_id": 7, "agent_id": "minted-agent-id"})
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := agentFlags{server: srv.URL, uuid: "test-uuid"}
	ensureEnrolled(context.Background(), log, &f)
	if f.agentID != "minted-agent-id" {
		t.Errorf("agentID = %q, want minted-agent-id", f.agentID)
	}
}

// An unreachable or pre-enroll server must leave the agent_id empty (the
// deploy path's report response is the fallback) without failing.
func TestEnsureEnrolledOldServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := agentFlags{server: srv.URL, uuid: "test-uuid"}
	ensureEnrolled(context.Background(), log, &f)
	if f.agentID != "" {
		t.Errorf("agentID = %q, want empty on 404", f.agentID)
	}
}
