package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rusketh/autodeploy/agent/internal/httpc"
	"github.com/rusketh/autodeploy/agent/internal/steps"
)

// Bulk-job journal. The server marks a job "running" when it hands it to the
// agent (claim), and only the agent's result moves it on. If the agent never
// reports -- the machine restarted mid-job (a step rebooted it, or a power
// cut), or the agent restarted to apply a rename / domain join right after
// claiming -- the job would sit "running" forever: its operation never
// finishes, a Tool Kit's Run As password override is never purged, the kit
// can't be deleted, and "skip machines where it already succeeded" excludes
// the machine for good.
//
// So the agent writes a small marker per job before running it and removes it
// once the result is posted. On startup, a leftover marker means the job was
// interrupted: its saved result is re-posted, or -- if it never finished -- it
// is reported FAILED. It is deliberately never re-run: a job whose step
// restarts the machine would otherwise run, restart, and run again forever
// (the same failure mode as the earlier rename boot loop).

type journalEntry struct {
	JobID  int64  `json:"job_id"`
	Action string `json:"action"`
	// Status/Result are set once the job finished but its result POST
	// failed, so the real outcome can be re-sent instead of "interrupted".
	Status string `json:"status,omitempty"`
	Result string `json:"result,omitempty"`
}

const interruptedJobResult = `{"error":"Interrupted: the agent or machine restarted before this job reported a result. A step may have restarted the machine; use the tool kit's restart setting instead of a restart step. The job was not run again."}`

func journalDir(f agentFlags) string { return filepath.Join(f.workDir, "jobs-inflight") }

func journalPath(f agentFlags, id int64) string {
	return filepath.Join(journalDir(f), fmt.Sprintf("job-%d.json", id))
}

func writeJournal(f agentFlags, e journalEntry) {
	if err := os.MkdirAll(journalDir(f), 0o755); err != nil {
		return
	}
	b, _ := json.Marshal(e)
	_ = os.WriteFile(journalPath(f, e.JobID), b, 0o600)
}

// runAndReportJob executes one claimed bulk job and posts its result,
// journalling it so an interruption is reported rather than lost.
func runAndReportJob(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, runner steps.Runner, id int64, action, payload string) {
	writeJournal(f, journalEntry{JobID: id, Action: action})
	status, result := executeBulkJob(ctx, log, c, f, runner, id, action, payload)
	if err := postJobResult(ctx, c, f.agentID, id, status, result); err != nil {
		log.Warn("bulk.result.post", slog.Int64("job", id), slog.String("error", err.Error()))
		// Keep the outcome so the next start re-sends it.
		writeJournal(f, journalEntry{JobID: id, Action: action, Status: status, Result: result})
		return
	}
	_ = os.Remove(journalPath(f, id))
}

// postJobResult reports a job's outcome. The agent_id authenticates it (the
// server also accepts the machine-UUID header for agents without one).
func postJobResult(ctx context.Context, c *httpc.Client, agentID string, id int64, status, result string) error {
	body := map[string]any{"status": status, "result_json": result}
	if agentID != "" {
		body["agent_id"] = agentID
	}
	return c.PostJSON(ctx, fmt.Sprintf("/api/v1/agent/jobs/%d/result", id), body, nil)
}

// reportInterruptedJobs settles every journalled job left by a previous run
// of the agent. Called once when a resident loop starts (the agent is a
// single service instance, so nothing else can be mid-job then).
func reportInterruptedJobs(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) {
	entries, err := os.ReadDir(journalDir(f))
	if err != nil {
		return
	}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		p := filepath.Join(journalDir(f), de.Name())
		b, err := os.ReadFile(p)
		var e journalEntry
		if err != nil || json.Unmarshal(b, &e) != nil || e.JobID <= 0 {
			_ = os.Remove(p)
			continue
		}
		status, result := e.Status, e.Result
		switch {
		case status != "":
		case e.Action == "rename" || e.Action == "reimage":
			// Restarting the machine is what these actions do; a restart
			// that beat the result POST isn't a failure.
			status, result = "ok", `{"note":"The machine restarted as this action requires, before the agent could report; the outcome was not confirmed."}`
		default:
			status, result = "failed", interruptedJobResult
		}
		if err := postJobResult(ctx, c, f.agentID, e.JobID, status, result); err != nil && !isClientError(err) {
			log.Warn("bulk.interrupted.post", slog.Int64("job", e.JobID), slog.String("error", err.Error()))
			continue // server unreachable: try again on the next start
		}
		log.Warn("bulk.interrupted", slog.Int64("job", e.JobID), slog.String("action", e.Action),
			slog.String("status", status))
		_ = os.Remove(p)
	}
}

// isClientError reports a 4xx from the server (e.g. the job no longer
// exists): retrying won't help, so the marker is dropped.
func isClientError(err error) bool {
	s := err.Error()
	for _, code := range []string{": 400 ", ": 403 ", ": 404 ", ": 409 ", ": 422 "} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}

// abandonClaimedJobs reports jobs this poll claimed but won't run because the
// agent is about to restart (rename / domain join). They are failed with a
// clear reason rather than left "running" for good.
func abandonClaimedJobs(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, ids []int64, reason string) {
	for _, id := range ids {
		result, _ := json.Marshal(map[string]string{
			"error": "Not run: the machine restarted " + reason + " before this job started. Run the operation again.",
		})
		if err := postJobResult(ctx, c, f.agentID, id, "failed", string(result)); err != nil {
			log.Warn("bulk.abandon.post", slog.Int64("job", id), slog.String("error", err.Error()))
		}
	}
}
