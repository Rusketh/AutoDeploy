package api

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Log ingest limits. Tight enough that a single misbehaving client
// can't fill the table, loose enough that a normal Boot Client end-of-
// run batch (~hundreds of events) goes through in one POST.
const (
	logIngestMaxBytes      = 256 * 1024 // 256 KiB per request
	logIngestMaxEvents     = 500        // events per request
	logIngestRateBurst     = 50         // requests
	logIngestRateRefillSec = 10         // tokens added per second
)

// RegisterLogs mounts the centralised log endpoints.
func RegisterLogs(mux *http.ServeMux, r Repos) {
	// Client-side log ingest (Boot Client, agent). Open — clients
	// identify themselves through the actor field; this is for fact-
	// shipping, not authorisation. The endpoint refuses anything that
	// looks like a secret in a field (best-effort tripwire), caps the
	// request body to keep a misbehaving client from flooding the DB,
	// and rate-limits per source IP.
	lim := newIngestLimiter(logIngestRateBurst, logIngestRateRefillSec)
	mux.HandleFunc("POST /api/v1/logs/ingest", handleLogIngest(r, lim))

	// Portal-side search (authenticated).
	mux.HandleFunc("GET /api/v1/logs", handleLogSearch(r))
}

type ingestReq struct {
	Events []model.LogEvent `json:"events"`
}

func handleLogIngest(r Repos, lim *ingestLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !lim.allow(clientIP(req)) {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		// Body size cap. A regular Boot Client end-of-run batch fits
		// comfortably in 256 KiB; anything larger is either a bug or a
		// flood attempt.
		req.Body = http.MaxBytesReader(w, req.Body, logIngestMaxBytes)
		var in ingestReq
		if err := decodeJSON(req, &in); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if len(in.Events) > logIngestMaxEvents {
			http.Error(w, "too many events in one request", http.StatusBadRequest)
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

// ingestLimiter is a tiny per-IP token bucket. Tokens refill at a
// fixed rate per second, capped at the burst limit. Not a global
// limiter -- one slow site shouldn't block log shipping from another.
type ingestLimiter struct {
	mu       sync.Mutex
	tokens   map[string]float64
	lastSeen map[string]time.Time
	burst    float64
	refill   float64
}

func newIngestLimiter(burst, refillPerSec int) *ingestLimiter {
	return &ingestLimiter{
		tokens:   map[string]float64{},
		lastSeen: map[string]time.Time{},
		burst:    float64(burst),
		refill:   float64(refillPerSec),
	}
}

func (l *ingestLimiter) allow(ip string) bool {
	if ip == "" {
		ip = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	tok, ok := l.tokens[ip]
	if !ok {
		tok = l.burst
	} else if last, ok := l.lastSeen[ip]; ok {
		tok += now.Sub(last).Seconds() * l.refill
		if tok > l.burst {
			tok = l.burst
		}
	}
	// Occasional sweep so old entries don't accumulate forever.
	if len(l.tokens) > 4096 {
		for k, t := range l.lastSeen {
			if now.Sub(t) > 1*time.Hour {
				delete(l.tokens, k)
				delete(l.lastSeen, k)
			}
		}
	}
	l.lastSeen[ip] = now
	if tok < 1 {
		l.tokens[ip] = tok
		return false
	}
	l.tokens[ip] = tok - 1
	return true
}

func clientIP(req *http.Request) string {
	if h := req.Header.Get("X-Forwarded-For"); h != "" {
		// First entry is the originating client when the chain is
		// trustworthy. We don't enforce trust here; the limiter is a
		// soft rate cap, not an auth boundary.
		for i := 0; i < len(h); i++ {
			if h[i] == ',' {
				return h[:i]
			}
		}
		return h
	}
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
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
		// Resolve each agent event's actor (an SMBIOS UUID) to a machine
		// name so the live tail and the API can show a name, not a raw UUID.
		if r.Inventory != nil {
			if names, nerr := r.Inventory.NamesByUUID(req.Context()); nerr == nil {
				model.EnrichMachineNames(ev, names)
			}
		}
		writeJSON(w, http.StatusOK, ev)
	}
}
