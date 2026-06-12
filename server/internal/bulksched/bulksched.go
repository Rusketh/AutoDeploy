// Package bulksched runs scheduled and recurring bulk operations. A single
// goroutine wakes on Tick, finds operations whose next_run_at has arrived,
// materialises a fresh run of jobs for each, and advances (or clears) the
// schedule. The same tick reaps jobs past their operation's cancel-after
// window and sends Wake-on-LAN packets for runs that ask for them. It
// mirrors the retention scheduler's shape so the server's background-task
// wiring stays uniform.
package bulksched

import (
	"context"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/notify"
)

// Waker sends Wake-on-LAN packets for a run's targeted machines.
// Satisfied by *wol.Waker; an interface so tests can fake it.
type Waker interface {
	WakeForJobs(ctx context.Context, jobs []model.BulkJob) int
}

// Scheduler fires due bulk operations. Construct, then Start.
type Scheduler struct {
	Bulk     *model.BulkRepo
	Interval time.Duration // 0 -> 30s
	Logger   *slog.Logger
	// Waker, when set, wakes a run's machines as its jobs are queued
	// (operations with the wake_on_lan option).
	Waker Waker
	// Emitter, when set, receives bulk.* events for operations the expiry
	// sweep finishes.
	Emitter *notify.Emitter
}

// Start runs the scheduler until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	if s.Interval == 0 {
		s.Interval = 30 * time.Second
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	s.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnce(ctx)
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context) {
	if s.Bulk == nil {
		return
	}
	now := time.Now()
	s.expireOverdue(ctx, now)
	due, err := s.Bulk.ListDueOperations(ctx, now)
	if err != nil {
		s.log().Warn("bulksched.list_due.fail", slog.String("error", err.Error()))
		return
	}
	for _, op := range due {
		jobs, err := s.Bulk.RunOperationNow(ctx, op.ID)
		if err != nil {
			s.log().Warn("bulksched.run.fail",
				slog.Int64("operation", int64(op.ID)),
				slog.String("error", err.Error()))
			continue
		}
		// Advance (recurring) or clear (one-time) the schedule so the
		// operation isn't re-fired on the next tick.
		if err := s.Bulk.AdvanceSchedule(ctx, op.ID, now); err != nil {
			s.log().Warn("bulksched.advance.fail",
				slog.Int64("operation", int64(op.ID)),
				slog.String("error", err.Error()))
		}
		// Wake the targets AFTER the jobs (and any re-image flags) are
		// committed, so a machine that boots straight into PXE finds its
		// flag already set.
		if op.WakeOnLAN && s.Waker != nil {
			s.Waker.WakeForJobs(ctx, jobs)
		}
		s.log().Info("bulksched.run.ok",
			slog.Int64("operation", int64(op.ID)),
			slog.String("action", op.Action),
			slog.String("schedule", op.ScheduleKind),
			slog.Bool("wake_on_lan", op.WakeOnLAN),
			slog.Int("jobs", len(jobs)))
	}
}

// expireOverdue cancels queued jobs past their operation's cancel-after
// window and emits a finished event for any operation that completed as a
// result (its other jobs already ran).
func (s *Scheduler) expireOverdue(ctx context.Context, now time.Time) {
	cancelled, completedOps, err := s.Bulk.ExpireOverdueJobs(ctx, now)
	if err != nil {
		s.log().Warn("bulksched.expire.fail", slog.String("error", err.Error()))
		return
	}
	if cancelled > 0 {
		s.log().Info("bulksched.expire.ok",
			slog.String("actor", "system"),
			slog.String("action", "bulk.jobs_expired"),
			slog.String("target", "bulk_job"),
			slog.Int64("cancelled", cancelled))
	}
	for _, opID := range completedOps {
		op, _, err := s.Bulk.GetOperation(ctx, opID)
		if err != nil {
			continue
		}
		prog, err := s.Bulk.ProgressFor(ctx, opID)
		if err != nil {
			continue
		}
		s.Emitter.Emit(ctx, notify.BulkOutcomeEvent(op, prog))
	}
}

func (s *Scheduler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
