// Buffered slog handler + log shipper for the Boot Client. The
// handler mirrors every record it sees into an in-memory buffer
// (bounded; oldest dropped when full); Ship POSTs the buffered batch
// to the server's /api/v1/logs/ingest endpoint. The design's §11.2
// "components ship their logs back to the AutoDeploy server" is what
// this exists to satisfy -- without it the portal can't diagnose a
// failed deploy.
//
// Mirrors the slog handler contract (Enabled/Handle/WithAttrs/
// WithGroup). State is shared across cloned handlers so attrs added
// via With(...) still land in the same buffer.

package logging

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// LogEvent is the wire shape the server's /api/v1/logs/ingest
// endpoint expects. Field names match server/internal/model.LogEvent
// so the same JSON works for both.
type LogEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	Component  string    `json:"component"`
	Level      string    `json:"level"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Fields     string    `json:"fields"`
}

// Shipper is the buffered slog handler. Share state across clones so
// every With(...) descendant feeds the same queue.
type Shipper struct {
	state  *shipperState
	base   slog.Handler
	attrs  []slog.Attr
	client *http.Client
}

type shipperState struct {
	mu     sync.Mutex
	buffer []LogEvent // fixed-size ring buffer (allocated once in NewShipper)
	head   int        // index of the oldest element
	count  int        // number of valid elements in the ring
	// dropped counts events evicted from a full buffer since the last
	// Drain; Drain reports it as a synthetic logs.buffer.dropped event so
	// the portal shows that (and how much) history is missing.
	dropped int
	// machineUUID, when set, is sent as the X-AutoDeploy-Machine-UUID
	// header on every ingest POST (the same header every other client
	// request carries) so the server can attribute the whole batch to a
	// machine even for events whose actor names something else.
	machineUUID string
}

// NewShipper wraps base. cap is the maximum number of events kept in
// memory; once full, the oldest INFO/DEBUG events are dropped first, then
// the oldest of anything (so a failing upload can't OOM the client, and
// chatty step output can't push the WARN/ERROR events that explain a
// failure out of the buffer).
func NewShipper(base slog.Handler, capacity int) *Shipper {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Shipper{
		state: &shipperState{buffer: make([]LogEvent, capacity)},
		base:  base,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			},
		},
	}
}

// Enabled defers to the underlying handler.
func (s *Shipper) Enabled(ctx context.Context, lvl slog.Level) bool {
	return s.base.Enabled(ctx, lvl)
}

// Handle writes the record through the underlying handler (so stdout
// JSON still streams as before) AND queues a LogEvent for shipment.
func (s *Shipper) Handle(ctx context.Context, r slog.Record) error {
	if err := s.base.Handle(ctx, r); err != nil {
		return err
	}
	ev := LogEvent{
		OccurredAt: r.Time,
		Action:     r.Message,
		Level:      r.Level.String(),
	}
	fields := map[string]any{}
	for _, a := range s.attrs {
		promote(&ev, fields, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		promote(&ev, fields, a)
		return true
	})
	fb, _ := json.Marshal(fields)
	ev.Fields = string(fb)
	if ev.Fields == "" {
		ev.Fields = "{}"
	}

	st := s.state
	st.mu.Lock()
	st.pushLocked(ev)
	st.mu.Unlock()
	return nil
}

// WithAttrs returns a new handler whose Records get attrs prepended.
// Critically, the new handler shares the buffer state pointer so
// child loggers still feed the same queue.
func (s *Shipper) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := append(append([]slog.Attr(nil), s.attrs...), attrs...)
	return &Shipper{
		state:  s.state,
		base:   s.base.WithAttrs(attrs),
		attrs:  merged,
		client: s.client,
	}
}

// WithGroup is a no-op for the shipper (we flatten groups into the
// fields JSON via promote). The underlying base handler still tracks
// groups for its own output.
func (s *Shipper) WithGroup(name string) slog.Handler {
	return &Shipper{
		state:  s.state,
		base:   s.base.WithGroup(name),
		attrs:  s.attrs,
		client: s.client,
	}
}

// SetMachineUUID records the machine's SMBIOS UUID. Every later Ship
// sends it as the X-AutoDeploy-Machine-UUID header, which servers that
// know it use to attribute the batch to the machine (older servers just
// ignore the header). Empty clears it.
func (s *Shipper) SetMachineUUID(uuid string) {
	st := s.state
	st.mu.Lock()
	st.machineUUID = uuid
	st.mu.Unlock()
}

// pushLocked appends ev, evicting one event first when the ring is full.
// Caller holds st.mu.
func (st *shipperState) pushLocked(ev LogEvent) {
	if st.count == len(st.buffer) {
		st.evictLocked()
	}
	st.buffer[(st.head+st.count)%len(st.buffer)] = ev
	st.count++
}

// evictLocked removes one event from a non-empty ring: the oldest
// INFO/DEBUG event if there is one, otherwise the oldest event. WARN and
// ERROR events therefore survive any amount of INFO noise (a step that
// prints thousands of stdout lines) -- they're what explains a failure.
// O(n) in the worst case, but only runs when the buffer is full.
// Caller holds st.mu.
func (st *shipperState) evictLocked() {
	if st.count == 0 {
		return
	}
	n := len(st.buffer)
	victim := 0
	for i := 0; i < st.count; i++ {
		if lowPriority(st.buffer[(st.head+i)%n].Level) {
			victim = i
			break
		}
	}
	// Shift everything older than the victim one slot newer (overwriting
	// it), then drop the now-duplicated head slot.
	for i := victim; i > 0; i-- {
		st.buffer[(st.head+i)%n] = st.buffer[(st.head+i-1)%n]
	}
	st.buffer[st.head] = LogEvent{}
	st.head = (st.head + 1) % n
	st.count--
	st.dropped++
}

// lowPriority reports whether a slog level string (e.g. "INFO",
// "DEBUG-4", "INFO+2") is below WARN, i.e. evictable first.
func lowPriority(level string) bool {
	return strings.HasPrefix(level, "INFO") || strings.HasPrefix(level, "DEBUG")
}

// Drain returns and clears every queued event in FIFO order. If events
// were evicted from a full buffer since the last Drain, a synthetic
// logs.buffer.dropped WARN event carrying the count leads the batch.
func (s *Shipper) Drain() []LogEvent {
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.count == 0 && st.dropped == 0 {
		return nil
	}
	out := make([]LogEvent, 0, st.count+1)
	if st.dropped > 0 {
		out = append(out, LogEvent{
			OccurredAt: time.Now(),
			Level:      slog.LevelWarn.String(),
			Action:     "logs.buffer.dropped",
			Actor:      st.machineUUID,
			Fields:     fmt.Sprintf(`{"dropped":%d,"capacity":%d}`, st.dropped, len(st.buffer)),
		})
		st.dropped = 0
	}
	cap := len(st.buffer)
	for i := 0; i < st.count; i++ {
		out = append(out, st.buffer[(st.head+i)%cap])
	}
	// Reset the ring without reallocating the backing array.
	st.head = 0
	st.count = 0
	return out
}

// Ship POSTs the drained buffer to baseURL + /api/v1/logs/ingest.
// Returns the number of events shipped. On any error, the events are
// re-queued so a later call can retry.
//
// The endpoint is unauthenticated by design (clients identify
// themselves through the actor field); the server enforces a per-IP
// rate limit and a body size cap. The client respects both by
// chunking large drains into batches.
func (s *Shipper) Ship(ctx context.Context, baseURL string, insecureTLS bool) (int, error) {
	events := s.Drain()
	if len(events) == 0 {
		return 0, nil
	}
	// Update the TLS setting on the shared client's transport in case
	// the caller toggles insecureTLS between calls (unlikely but safe).
	if tr, ok := s.client.Transport.(*http.Transport); ok {
		tr.TLSClientConfig.InsecureSkipVerify = insecureTLS
	}
	client := s.client
	s.state.mu.Lock()
	machineUUID := s.state.machineUUID
	s.state.mu.Unlock()
	// Server cap is 500 events per request; chunk slightly below to
	// leave headroom for JSON framing.
	const chunk = 400
	sent := 0
	for off := 0; off < len(events); off += chunk {
		end := off + chunk
		if end > len(events) {
			end = len(events)
		}
		body := map[string]any{"events": events[off:end]}
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(body); err != nil {
			s.requeue(events[off:])
			return sent, err
		}
		req, err := http.NewRequestWithContext(ctx, "POST",
			baseURL+"/api/v1/logs/ingest", buf)
		if err != nil {
			s.requeue(events[off:])
			return sent, err
		}
		req.Header.Set("Content-Type", "application/json")
		if machineUUID != "" {
			req.Header.Set("X-AutoDeploy-Machine-UUID", machineUUID)
		}
		resp, err := client.Do(req)
		if err != nil {
			s.requeue(events[off:])
			return sent, err
		}
		body2, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			s.requeue(events[off:])
			return sent, fmt.Errorf("logs/ingest: %s: %s", resp.Status, string(body2))
		}
		sent += end - off
	}
	return sent, nil
}

// requeue prepends events back into the buffer after a failed Ship,
// preserving order. If that overflows the buffer, events are evicted
// the same way Handle evicts: oldest INFO/DEBUG first, so the WARN/ERROR
// events that explain a failure outlive the noise around them.
func (s *Shipper) requeue(events []LogEvent) {
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	cap := len(st.buffer)
	// Drain existing items into a temporary slice, prepend the failed
	// events, then refill the ring.
	existing := make([]LogEvent, st.count)
	for i := 0; i < st.count; i++ {
		existing[i] = st.buffer[(st.head+i)%cap]
	}
	combined := append(append([]LogEvent(nil), events...), existing...)
	st.head = 0
	st.count = 0
	for _, ev := range combined {
		st.pushLocked(ev)
	}
}

// promote folds a single slog.Attr into either a LogEvent header
// field (component/actor/target) or the generic fields map. Attrs are
// folded in order (logger With(...) attrs first, then the record's), so
// a per-call actor/target overrides a logger-wide default.
func promote(ev *LogEvent, fields map[string]any, a slog.Attr) {
	a.Value = a.Value.Resolve()
	switch a.Key {
	case "component":
		ev.Component = a.Value.String()
	case "actor":
		ev.Actor = a.Value.String()
	case "target":
		ev.Target = a.Value.String()
	default:
		fields[a.Key] = fieldValue(a.Value)
	}
}

// fieldValue converts an attr value to something encoding/json renders
// usefully. Most kinds pass through; the fix is for KindAny values whose
// JSON form is meaningless: an error (a *errors.errorString or
// *fmt.wrapError has no exported fields, so it marshalled as `{}`) becomes
// its Error() text, and a fmt.Stringer without its own JSON/text encoding
// becomes its String(). Groups become nested objects.
func fieldValue(v slog.Value) any {
	switch v.Kind() {
	case slog.KindGroup:
		m := map[string]any{}
		for _, ga := range v.Group() {
			m[ga.Key] = fieldValue(ga.Value.Resolve())
		}
		return m
	case slog.KindAny:
		return stringifyAny(v.Any())
	default:
		return v.Any()
	}
}

// stringifyAny returns err.Error() / s.String() for errors and plain
// Stringers, x unchanged otherwise (including anything with its own JSON
// or text encoding). A nil interface stays nil (JSON null); a typed-nil
// receiver whose method panics yields a placeholder rather than taking
// the logger down.
func stringifyAny(x any) (out any) {
	switch t := x.(type) {
	case nil:
		return nil
	case json.Marshaler, encoding.TextMarshaler:
		return x
	case error:
		defer func() {
			if recover() != nil {
				out = "<nil error>"
			}
		}()
		return t.Error()
	case fmt.Stringer:
		defer func() {
			if recover() != nil {
				out = "<nil>"
			}
		}()
		return t.String()
	}
	return x
}
