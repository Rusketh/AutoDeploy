// Package logging is the shared structured-logging surface for the server.
//
// Every event records the four required fields per docs/design/CONVENTIONS.md:
// actor, action, component and target. The package also exposes a Secret
// wrapper type that redacts on any String/Marshal call so secret values
// cannot accidentally leak into logs, errors or HTTP responses.
package logging

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
)

type ctxKey int

const loggerKey ctxKey = 0

// New returns a JSON slog.Logger writing to the given writer. Pass nil to
// log to os.Stdout.
func New(w io.Writer, component string) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(h).With(slog.String("component", component))
}

// With attaches a logger to ctx so deep call sites can retrieve it without
// threading it through every function.
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// From returns the logger attached to ctx, or the default slog logger if
// none has been attached.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// Event emits a structured event. Required fields are passed by name so they
// cannot be forgotten by accident.
func Event(ctx context.Context, action, actor, target string, attrs ...slog.Attr) {
	l := From(ctx)
	all := make([]slog.Attr, 0, len(attrs)+2)
	all = append(all,
		slog.String("actor", actor),
		slog.String("target", target),
	)
	all = append(all, attrs...)
	l.LogAttrs(ctx, slog.LevelInfo, action, all...)
}

// Secret wraps a sensitive string. Its String and MarshalJSON methods always
// return "[REDACTED]" so secret values cannot accidentally appear in logs,
// error messages, JSON responses or stack traces.
//
// The value is accessible only via the explicit Reveal method, which exists
// for the narrow places that genuinely need it (writing the value to the
// crypto subsystem, returning it to an authenticated portal user).
type Secret struct {
	v string
}

// NewSecret wraps v.
func NewSecret(v string) Secret { return Secret{v: v} }

// String redacts.
func (s Secret) String() string { return "[REDACTED]" }

// MarshalJSON redacts.
func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal("[REDACTED]")
}

// Reveal returns the wrapped value. Call sites using Reveal are the audit
// boundary: every call should be accompanied by a logged event recording
// the actor and the reason.
func (s Secret) Reveal() string { return s.v }
