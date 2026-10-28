package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/logging"
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
	mc := newIngestMachineCache(r.Inventory)
	mux.HandleFunc("POST /api/v1/logs/ingest", handleLogIngest(r, lim, mc))

	// Portal-side search (authenticated).
	mux.HandleFunc("GET /api/v1/logs", handleLogSearch(r))
}

type ingestReq struct {
	Events []model.LogEvent `json:"events"`
}

// machineUUIDHeader is the header every newer client request carries with
// the machine's SMBIOS UUID. Older clients don't send it.
const machineUUIDHeader = "X-AutoDeploy-Machine-UUID"

func handleLogIngest(r Repos, lim *ingestLimiter, mc *ingestMachineCache) http.HandlerFunc {
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
		prepareIngestedEvents(req, r, mc, in.Events)
		if err := r.Logs.AppendBatch(req.Context(), in.Events); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// prepareIngestedEvents derives the server-side columns for a client batch:
// the machine (from the X-AutoDeploy-Machine-UUID header, else the event's
// actor when it is a known SMBIOS UUID / agent ID), and the deployment,
// error, message, hint and outcome (model.PrepareIngestedLog). Whatever a
// client put in those columns is discarded — the endpoint is open, so an
// audit "user" or a machine attribution must never be taken from the body.
// Old clients (no header, no extra fields) get the same treatment from the
// actor alone; nothing they send is rejected.
func prepareIngestedEvents(req *http.Request, r Repos, mc *ingestMachineCache, evs []model.LogEvent) {
	ctx := req.Context()
	headerMachine := mc.resolve(ctx, req.Header.Get(machineUUIDHeader))
	var names *model.NameLookup
	if r.Inventory != nil {
		names = model.NewNameLookup(ctx, r.Inventory)
	}
	for i := range evs {
		ev := &evs[i]
		ev.Message, ev.User, ev.Outcome, ev.Error, ev.Hint = "", "", "", "", ""
		ev.DeploymentID = 0
		ev.MachineID = headerMachine
		if ev.MachineID == 0 {
			ev.MachineID = mc.resolve(ctx, ev.Actor)
		}
		model.PrepareIngestedLog(ev, names)
	}
}

// ingestMachineCache maps the UUIDs / agent IDs clients log under to machine
// row IDs, so ingest costs at most one indexed lookup per new identifier
// rather than one per event. Misses are cached too (briefly), so a stream
// of events from an unknown actor doesn't query on every batch.
type ingestMachineCache struct {
	inv *model.InventoryRepo
	mu  sync.Mutex
	m   map[string]ingestMachineEntry
}

type ingestMachineEntry struct {
	id  model.ID
	exp time.Time
}

const (
	ingestMachineTTL     = 10 * time.Minute
	ingestMachineMissTTL = time.Minute
	ingestMachineMax     = 20000
)

func newIngestMachineCache(inv *model.InventoryRepo) *ingestMachineCache {
	return &ingestMachineCache{inv: inv, m: map[string]ingestMachineEntry{}}
}

// resolve returns the machine ID for an SMBIOS UUID or agent ID, 0 if none.
func (c *ingestMachineCache) resolve(ctx context.Context, key string) model.ID {
	key = strings.TrimSpace(key)
	if c == nil || c.inv == nil || key == "" || len(key) > 128 || key == "system" {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.m[key]; ok && now.Before(e.exp) {
		c.mu.Unlock()
		return e.id
	}
	c.mu.Unlock()
	id, err := c.inv.MachineIDByActor(ctx, key)
	ttl := ingestMachineTTL
	if err != nil {
		id, ttl = 0, ingestMachineMissTTL
	}
	c.mu.Lock()
	if len(c.m) >= ingestMachineMax {
		c.m = map[string]ingestMachineEntry{}
	}
	c.m[key] = ingestMachineEntry{id: id, exp: now.Add(ttl)}
	c.mu.Unlock()
	return id
}

// LogEventSink returns a logging.Sink that persists server-side
// logging.Event calls to the central log, so failures the server itself
// hits (an upload that couldn't be written, …) show up on the Logs page.
// Best-effort: a write error is ignored.
func LogEventSink(logs *model.LogRepo) logging.Sink {
	return func(ctx context.Context, level slog.Level, action, actor, target string, attrs []slog.Attr) {
		if logs == nil {
			return
		}
		f := map[string]any{}
		for _, a := range attrs {
			v := a.Value.Resolve()
			if err, ok := v.Any().(error); ok {
				f[a.Key] = err.Error()
				continue
			}
			f[a.Key] = v.Any()
		}
		fb, _ := json.Marshal(f)
		ev := model.LogEvent{
			Component: "server",
			Level:     level.String(),
			Actor:     actor,
			Action:    action,
			Target:    target,
			Fields:    string(fb),
		}
		if actor != "" && actor != "system" {
			ev.User = actor
		}
		model.ApplyLogDescription(&ev, model.LogNames{})
		ev.Level, ev.Category = level.String(), ""
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = logs.Append(wctx, ev)
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
		// The same filters as the Logs page (level, machine, user, category,
		// deployment, from/to, q, before/after), plus the original
		// component/actor/action/since/until/limit. Results are newest first;
		// ordered by id when paging with before/after (the live tail polls
		// with after=<last id seen>).
		var loc *time.Location
		if r.Runtime != nil {
			loc = r.Runtime.DisplayLocation()
		}
		lq := model.ParseLogQuery(req.Context(), req.URL.Query(), r.Inventory, loc)
		s := lq.Search
		if req.URL.Query().Get("limit") == "" {
			s.Limit = 0 // the API's historic default (1000)
		}
		ev, err := r.Logs.Search(req.Context(), s)
		if err != nil {
			writeError(w, err)
			return
		}
		if ev == nil {
			ev = []model.LogEvent{}
		}
		// Resolve each event's actor (an SMBIOS UUID, or the agent ID older
		// rows carry) to machine_name and its target ("machine:12",
		// "software/3") to target_name, so the live tail and API clients can
		// show names, not raw IDs. Both fields are additive and omitted when
		// nothing resolves.
		// Catalogue fields (message, category, hint) are filled for rows
		// stored before those columns existed.
		if r.Inventory != nil {
			model.NewNameLookup(req.Context(), r.Inventory).DescribeLogs(ev)
		} else {
			for i := range ev {
				model.ApplyLogDescription(&ev[i], model.LogNames{})
			}
		}
		writeJSON(w, http.StatusOK, ev)
	}
}
