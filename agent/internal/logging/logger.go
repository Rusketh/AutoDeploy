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
	// Each sink is wrapped in a bestEffortWriter so a dead one can't
	// abort the others. Under the Windows SCM the service has no console,
	// so writes to os.Stdout fail; without this guard io.MultiWriter would
	// stop at that error and the rotating file (and, before this, log
	// shipping) would silently get nothing.
	sinks := []io.Writer{&bestEffortWriter{w: w}}
	// Open a rotating file logger alongside stdout so admins can inspect
	// agent logs on the local machine even when the service manager
	// discards stdout.
	if rf, err := OpenRotatingFile(defaultLogPath(), 0); err == nil {
		sinks = append(sinks, &bestEffortWriter{w: rf})
	}
	base := slog.NewJSONHandler(io.MultiWriter(sinks...), &slog.HandlerOptions{Level: slog.LevelInfo})
	sh := NewShipper(base, bufferCap)
	return slog.New(sh).With(slog.String("component", component)), sh
}

// bestEffortWriter swallows write errors and short writes from its
// underlying writer, always reporting a full, successful write. It
// keeps one failing sink (a console-less stdout under a Windows service)
// from aborting an io.MultiWriter chain and starving the other sinks.
type bestEffortWriter struct{ w io.Writer }

func (b *bestEffortWriter) Write(p []byte) (int, error) {
	_, _ = b.w.Write(p)
	return len(p), nil
}

func defaultLogPath() string {
	if runtime.GOOS == "windows" {
		return `C:\ProgramData\AutoDeploy\logs\agent.log`
	}
	return filepath.Join("/var/lib/autodeploy/logs", "agent.log")
}
