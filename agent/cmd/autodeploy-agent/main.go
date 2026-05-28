// Command autodeploy-agent is the resident, silent in-OS Deployment Client.
// Cross-compiles to Windows; runs on Linux too for development convenience.
//
// Phase 0 skeleton: starts up, identifies itself in the log, and exits.
// The real lifecycle (software install at deploy time, periodic check-in,
// queued bulk-job execution, BitLocker management) lands from Phase 6.
package main

import (
	"flag"
	"log/slog"
	"os"
	"runtime"

	"github.com/rusketh/autodeploy/agent/internal/logging"
)

func main() {
	server := flag.String("server", "", "AutoDeploy server base URL")
	once := flag.Bool("once", true, "Run once and exit (Phase 0 default; resident mode comes in Phase 13)")
	flag.Parse()

	log := logging.New(os.Stdout, "agent")
	log.Info("agent.start",
		slog.String("actor", "system"),
		slog.String("target", "self"),
		slog.String("os", runtime.GOOS),
		slog.String("arch", runtime.GOARCH),
		slog.String("server", *server),
		slog.Bool("once", *once),
	)
	if *server == "" {
		log.Info("agent.idle", slog.String("reason", "no server configured; exiting"))
		return
	}
	log.Info("agent.exit", slog.String("reason", "phase-0 skeleton has no work to do"))
}
