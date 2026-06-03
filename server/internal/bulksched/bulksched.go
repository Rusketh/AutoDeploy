// Package bulksched runs scheduled and recurring bulk operations. A single
// goroutine wakes on Tick, finds operations whose next_run_at has arrived,
// materialises a fresh run of jobs for each, and advances (or clears) the
// schedule. It mirrors the retention scheduler's shape so the server's
// background-task wiring stays uniform.
package bulksched

import (
	"context"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Scheduler fires due bulk operations. Construct, then Start.
type Scheduler struct {
	Bulk     *model.BulkRepo
	Interval time.Duration // 0 -> 30s
	Logger   *slog.Logger
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
		s.log().Info("bulksched.run.ok",
			slog.Int64("operation", int64(op.ID)),
			slog.String("action", op.Action),
			slog.String("schedule", op.ScheduleKind),
			slog.Int("jobs", len(jobs)))
	}
}

func (s *Scheduler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
