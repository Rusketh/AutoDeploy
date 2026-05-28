// Package logging is the Boot Client's structured logger. Mirrors the server
// logger contract (actor, action, component, target) so events ship back
// uniformly via the Shipper to /api/v1/logs/ingest.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// New returns a JSON slog.Logger tagged with the given component name.
// Use this for the basic stdout logger when log shipping is not wired
// up (e.g. unit tests).
func New(w io.Writer, component string) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With(slog.String("component", component))
}

// NewWithShipper returns a JSON slog.Logger that ALSO buffers every
// record into the returned Shipper for later upload via the
// /api/v1/logs/ingest endpoint. Use this for live deployments where
// the server should see what happened on the client.
func NewWithShipper(w io.Writer, component string, bufferCap int) (*slog.Logger, *Shipper) {
	if w == nil {
		w = os.Stdout
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	sh := NewShipper(base, bufferCap)
	return slog.New(sh).With(slog.String("component", component)), sh
}
