package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// RegisterLogs mounts the centralised log endpoints.
func RegisterLogs(mux *http.ServeMux, r Repos) {
	// Client-side log ingest (Boot Client, agent). Open — clients
	// identify themselves through the actor field; this is for fact-
	// shipping, not authorisation. The endpoint refuses anything that
	// looks like a secret in a field (best-effort tripwire).
	mux.HandleFunc("POST /api/v1/logs/ingest", handleLogIngest(r))

	// Portal-side search (authenticated).
	mux.HandleFunc("GET /api/v1/logs", handleLogSearch(r))
}

type ingestReq struct {
	Events []model.LogEvent `json:"events"`
}

func handleLogIngest(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in ingestReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		for i := range in.Events {
			// Refuse events whose fields look like they leaked a secret.
			if containsSecretShape(in.Events[i].Fields) {
				http.Error(w, "log event appears to contain a secret value", http.StatusBadRequest)
				return
			}
		}
		if err := r.Logs.AppendBatch(req.Context(), in.Events); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// containsSecretShape is a best-effort tripwire that rejects log events
// containing JSON keys that look like cleartext secrets. It is NOT
// authoritative — emitters are responsible for not shipping secrets in
// the first place.
func containsSecretShape(fields string) bool {
	low := []byte(fields)
	for _, k := range []string{
		`"password":"`, `"pin":"`, `"recovery_key":"`, `"recoveryKey":"`,
	} {
		if containsCI(low, []byte(k)) {
			return true
		}
	}
	return false
}

func containsCI(haystack, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			h := haystack[i+j]
			if h >= 'A' && h <= 'Z' {
				h += 'a' - 'A'
			}
			n := needle[j]
			if n >= 'A' && n <= 'Z' {
				n += 'a' - 'A'
			}
			if h != n {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func handleLogSearch(r Repos) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if _, ok := UserFromRequest(req, r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		q := req.URL.Query()
		s := model.LogSearch{
			Component: q.Get("component"),
			Actor:     q.Get("actor"),
			Action:    q.Get("action"),
		}
		if v := q.Get("since"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				s.Since = t
			}
		}
		if v := q.Get("until"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				s.Until = t
			}
		}
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				s.Limit = n
			}
		}
		ev, err := r.Logs.Search(req.Context(), s)
		if err != nil {
			writeError(w, err)
			return
		}
		if ev == nil {
			ev = []model.LogEvent{}
		}
		writeJSON(w, http.StatusOK, ev)
	}
}
