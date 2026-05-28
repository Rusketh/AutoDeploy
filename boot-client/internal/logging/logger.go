// Package logging is the Boot Client's structured logger. Mirrors the server
// logger contract (actor, action, component, target) so events ship back
// uniformly when centralised collection is wired up in Phase 14.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// New returns a JSON slog.Logger tagged with the given component name.
func New(w io.Writer, component string) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With(slog.String("component", component))
}
