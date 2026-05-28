// Package logging is the Deployment Client (agent)'s structured logger.
// Mirrors the server and Boot Client logger contract.
package logging

import (
	"io"
	"log/slog"
	"os"
)

func New(w io.Writer, component string) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With(slog.String("component", component))
}

// NewWithShipper returns a JSON slog.Logger that ALSO buffers every
// record into the returned Shipper for later upload via the
// /api/v1/logs/ingest endpoint.
func NewWithShipper(w io.Writer, component string, bufferCap int) (*slog.Logger, *Shipper) {
	if w == nil {
		w = os.Stdout
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	sh := NewShipper(base, bufferCap)
	return slog.New(sh).With(slog.String("component", component)), sh
}
