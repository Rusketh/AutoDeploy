// Package logging is the Deployment Client (agent)'s structured logger.
// Mirrors the server and Boot Client logger contract.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
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
// /api/v1/logs/ingest endpoint. A rotating log file is written to
// the platform's standard log directory for local debugging.
func NewWithShipper(w io.Writer, component string, bufferCap int) (*slog.Logger, *Shipper) {
	if w == nil {
		w = os.Stdout
	}
	// Open a rotating file logger alongside stdout so admins can
	// inspect agent logs on the local machine even when the service
	// manager discards stdout.
	if rf, err := OpenRotatingFile(defaultLogPath(), 0); err == nil {
		w = io.MultiWriter(w, rf)
	}
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	sh := NewShipper(base, bufferCap)
	return slog.New(sh).With(slog.String("component", component)), sh
}

func defaultLogPath() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\logs\agent.log`
	}
	return filepath.Join("/var/lib/autodeploy/logs", "agent.log")
}
