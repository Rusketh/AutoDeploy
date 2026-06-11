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
	"github.com/rusketh/autodeploy/agent/internal/updates"
)

// updateJob mirrors one entry of the server's update_jobs array. The
// metadata fields (kb_number onward) are sent by newer servers; an older
// server omits them and the legacy update-<id>.msu behavior applies.
type updateJob struct {
	ID           int64  `json:"id"`
	DeploymentID int64  `json:"deployment_id"`
	MachineID    int64  `json:"machine_id"`
	UpdateID     int64  `json:"update_id"`
	Status       string `json:"status"`

	KBNumber        string `json:"kb_number"`
	PayloadFilename string `json:"payload_filename"`
	RebootAfter     bool   `json:"reboot_after"`
	SizeBytes       int64  `json:"size_bytes"`
}

// updateBatchResult summarizes one processUpdateJobs pass.
type updateBatchResult struct {
	// AnyInstalled is true when at least one job reported ok, so the
	// caller can refresh the KB scan immediately.
	AnyInstalled bool
	// RebootNeeded is true when an installed update both demanded a
	// reboot (exit 3010 etc.) and was flagged reboot_after by the
	// operator. The caller schedules the restart at the end of the cycle.
	RebootNeeded bool
}

// updateJobResult is the result_json the agent reports for a job.
type updateJobResult struct {
	ExitCode       int    `json:"exit_code"`
	Reason         string `json:"reason,omitempty"`
	RebootRequired bool   `json:"reboot_required,omitempty"`
	Error          string `json:"error,omitempty"`
	Output         string `json:"output,omitempty"`
}

// updatePayloadName returns the on-disk filename for a job's payload: the
// server-supplied filename — base name only, so a hostile or buggy value
// can't escape the workdir — or the legacy update-<id>.msu fallback. The
// extension matters: it selects wusa (.msu) vs dism (.cab) at install time.
func updatePayloadName(j updateJob) string {
	name := j.PayloadFilename
	// Strip path components by hand: on the Linux test host '\' is not a
	// path separator, so filepath.Base alone would keep it.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return fmt.Sprintf("update-%d.msu", j.UpdateID)
	}
	return name
}

// decideJobOutcome maps an installer outcome to the reported job status
// and whether this job asks for the end-of-cycle reboot.
func decideJobOutcome(o updates.Outcome, rebootAfter bool) (status string, needsReboot bool) {
	if !o.Success {
		return "failed", false
	}
	return "ok", rebootAfter && o.RebootRequired
}

// processUpdateJobs downloads and installs each claimed Windows Update
// job, reporting a per-job result to the server. Failures are isolated:
// one bad update doesn't stop the rest of the batch.
func processUpdateJobs(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags, jobs []updateJob) updateBatchResult {
	var res updateBatchResult
	for _, j := range jobs {
		log.Info("update.job.start",
			slog.Int64("job_id", j.ID),
			slog.Int64("update_id", j.UpdateID),
			slog.String("kb", j.KBNumber))
		path := filepath.Join(f.workDir, updatePayloadName(j))
		url := fmt.Sprintf("/payload/updates/%d", j.UpdateID)
		out, err := os.Create(path)
		if err != nil {
			log.Error("update.download.create", slog.String("error", err.Error()))
			reportUpdateJobResult(ctx, c, j.ID, "failed", updateJobResult{Error: "create file: " + err.Error()})
			continue
		}
		if err := c.Download(ctx, f.server+url, out); err != nil {
			_ = out.Close()
			_ = os.Remove(path)
			log.Error("update.download", slog.String("error", err.Error()))
			reportUpdateJobResult(ctx, c, j.ID, "failed", updateJobResult{Error: "download: " + err.Error()})
			continue
		}
		_ = out.Close()

		exitCode, output, err := updates.Install(ctx, path)
		_ = os.Remove(path)
		if err != nil {
			// The installer could not be run at all (bad extension,
			// missing binary) — distinct from a non-zero exit.
			log.Error("update.install.run", slog.Int64("job_id", j.ID), slog.String("error", err.Error()))
			reportUpdateJobResult(ctx, c, j.ID, "failed", updateJobResult{ExitCode: exitCode, Error: err.Error()})
			continue
		}
		o := updates.ClassifyExitCode(exitCode)
		status, needsReboot := decideJobOutcome(o, j.RebootAfter)
		result := updateJobResult{
			ExitCode:       exitCode,
			Reason:         o.Reason,
			RebootRequired: o.RebootRequired,
		}
		if status != "ok" {
			result.Output = output
			log.Error("update.install.fail",
				slog.Int64("job_id", j.ID),
				slog.Int("exit_code", exitCode),
				slog.String("reason", o.Reason))
		} else {
			res.AnyInstalled = true
			res.RebootNeeded = res.RebootNeeded || needsReboot
			log.Info("update.install.ok",
				slog.Int64("job_id", j.ID),
				slog.Int("exit_code", exitCode),
				slog.String("reason", o.Reason),
				slog.Bool("reboot_required", o.RebootRequired))
		}
		reportUpdateJobResult(ctx, c, j.ID, status, result)
	}
	return res
}

func reportUpdateJobResult(ctx context.Context, c *httpc.Client, jobID int64, status string, result updateJobResult) {
	resultJSON, err := json.Marshal(result)
	if err != nil {
		resultJSON = []byte("{}")
	}
	_ = c.PostJSON(ctx,
		fmt.Sprintf("/api/v1/agent/update-job/%d/result", jobID),
		map[string]any{"status": status, "result_json": string(resultJSON)}, nil)
}

var kbScanCounter int

func reportKBScan(ctx context.Context, log *slog.Logger, c *httpc.Client, f agentFlags) {
	kbs, err := updates.ScanInstalledKBs(ctx)
	if err != nil {
		log.Warn("kbscan.fail", slog.String("error", err.Error()))
		return
	}
	if len(kbs) == 0 {
		return
	}
	if err := c.PostJSON(ctx, "/api/v1/agent/update-status",
		map[string]any{"agent_id": f.agentID, "kb_numbers": kbs}, nil); err != nil {
		log.Warn("kbscan.report", slog.String("error", err.Error()))
		return
	}
	log.Info("kbscan.reported", slog.Int("count", len(kbs)))
}

// scheduleUpdateReboot restarts the machine so reboot-required updates
// finalize. Only called when the operator opted in via the update's
// "Reboot after install" flag; the 60-second delay gives the result POSTs
// and log shipping time to finish. Best-effort; a no-op under --dry-run.
func scheduleUpdateReboot(ctx context.Context, log *slog.Logger, dryRun bool) {
	if dryRun {
		log.Info("update.reboot.skip", slog.String("reason", "--dry-run"))
		return
	}
	runner := &steps.OSRunner{Log: log, DryRun: dryRun}
	if _, err := runner.Run(ctx, "shutdown",
		[]string{"/r", "/t", "60", "/c", "AutoDeploy: restarting to complete Windows Update installation"}, ""); err != nil {
		log.Warn("update.reboot", slog.String("error", err.Error()))
		return
	}
	log.Info("update.reboot.scheduled", slog.Int("delay_seconds", 60))
}
