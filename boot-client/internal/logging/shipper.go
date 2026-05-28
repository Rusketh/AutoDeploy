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
	state *shipperState
	base  slog.Handler
	attrs []slog.Attr
}

type shipperState struct {
	mu     sync.Mutex
	buffer []LogEvent
	cap    int
}

// NewShipper wraps base. cap is the maximum number of events kept in
// memory; once full, the oldest events are dropped (so a failing
// upload can't OOM the boot environment).
func NewShipper(base slog.Handler, cap int) *Shipper {
	if cap <= 0 {
		cap = 1024
	}
	return &Shipper{
		state: &shipperState{cap: cap},
		base:  base,
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
	if len(st.buffer) >= st.cap {
		// Drop the oldest so a stuck upload can't grow the buffer.
		copy(st.buffer, st.buffer[1:])
		st.buffer = st.buffer[:len(st.buffer)-1]
	}
	st.buffer = append(st.buffer, ev)
	st.mu.Unlock()
	return nil
}

// WithAttrs returns a new handler whose Records get attrs prepended.
// Critically, the new handler shares the buffer state pointer so
// child loggers still feed the same queue.
func (s *Shipper) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := append(append([]slog.Attr(nil), s.attrs...), attrs...)
	return &Shipper{
		state: s.state,
		base:  s.base.WithAttrs(attrs),
		attrs: merged,
	}
}

// WithGroup is a no-op for the shipper (we flatten groups into the
// fields JSON via promote). The underlying base handler still tracks
// groups for its own output.
func (s *Shipper) WithGroup(name string) slog.Handler {
	return &Shipper{
		state: s.state,
		base:  s.base.WithGroup(name),
		attrs: s.attrs,
	}
}

// Drain returns and clears every queued event.
func (s *Shipper) Drain() []LogEvent {
	st := s.state
	st.mu.Lock()
	defer st.mu.Unlock()
	out := st.buffer
	st.buffer = nil
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
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureTLS},
		},
	}
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
	combined := append([]LogEvent{}, events...)
	combined = append(combined, st.buffer...)
	if len(combined) > st.cap {
		combined = combined[len(combined)-st.cap:]
	}
	st.buffer = combined
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
