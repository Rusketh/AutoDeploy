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
