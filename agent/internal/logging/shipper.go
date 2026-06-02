// Buffered slog handler + log shipper for the agent. The
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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
}

// NewShipper wraps base. cap is the maximum number of events kept in
// memory; once full, the oldest events are dropped (so a failing
// upload can't OOM the boot environment).
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

// Handle queues a LogEvent for shipment AND writes the record through
// the underlying handler (so stdout/file JSON still streams).
//
// The event is buffered for shipment FIRST, before the local write.
// Shipping records back to the server is the Shipper's entire reason to
// exist, so a failure writing to the local sink must never cost us the
// event. This matters under the Windows SCM: a service has no console,
// so writes to os.Stdout fail -- if we buffered only on a successful
// local write, every agent record would be dropped and the portal would
// show no logs at all.
func (s *Shipper) Handle(ctx context.Context, r slog.Record) error {
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
	cap := len(st.buffer)
	if st.count < cap {
		// Space available: write at the next free slot.
		st.buffer[(st.head+st.count)%cap] = ev
		st.count++
	} else {
		// Full: overwrite the oldest slot and advance head.
		st.buffer[st.head] = ev
		st.head = (st.head + 1) % cap
	}
	st.mu.Unlock()

	// Write through to the local sink (stdout + rotating file) last. Its
	// error is intentionally ignored: slog discards Handle errors anyway,
	// and a dead local sink must not look like a logging failure now that
	// the event is safely queued for shipment.
	_ = s.base.Handle(ctx, r)
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

// Drain returns and clears every queued event in FIFO order.
func (s *Shipper) Drain() []LogEvent {
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.count == 0 {
		return nil
	}
	out := make([]LogEvent, st.count)
	cap := len(st.buffer)
	for i := 0; i < st.count; i++ {
		out[i] = st.buffer[(st.head+i)%cap]
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
// preserving order. If the buffer is at capacity the oldest of the
// re-queued events drop -- the live-tail events are more useful for
// diagnosis than ancient ones anyway.
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
	combined := append(events, existing...)
	if len(combined) > cap {
		// Keep the newest (tail) entries, drop the oldest (head).
		combined = combined[len(combined)-cap:]
	}
	st.head = 0
	st.count = len(combined)
	copy(st.buffer, combined)
}

// promote folds a single slog.Attr into either a LogEvent header
// field (component/actor/target) or the generic fields map.
func promote(ev *LogEvent, fields map[string]any, a slog.Attr) {
	switch a.Key {
	case "component":
		ev.Component = a.Value.String()
	case "actor":
		ev.Actor = a.Value.String()
	case "target":
		ev.Target = a.Value.String()
	default:
		fields[a.Key] = a.Value.Any()
	}
}
