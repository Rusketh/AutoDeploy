// Package retention runs the periodic housekeeping tasks: log pruning
// (Phase 14) and any future scheduled cleanup. A single goroutine
// wakes on Tick and runs each task once. Operators set retention from
// AUTODEPLOY_LOG_RETENTION_DAYS.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Scheduler runs periodic retention tasks. Construct, then Start.
type Scheduler struct {
	Logs           *model.LogRepo
	LogRetention   time.Duration // 0 disables log pruning
	Interval       time.Duration // 0 -> hourly
	Logger         *slog.Logger
}

// Start runs the scheduler until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	if s.Interval == 0 {
		s.Interval = time.Hour
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
	if s.Logs != nil && s.LogRetention > 0 {
		cutoff := time.Now().Add(-s.LogRetention)
		n, err := s.Logs.Prune(ctx, cutoff)
		if err != nil {
			s.log().Warn("retention.log_prune.fail",
				slog.String("error", err.Error()))
			return
		}
		s.log().Info("retention.log_prune.ok",
			slog.Int64("removed", n),
			slog.String("cutoff", cutoff.Format(time.RFC3339)))
	}
}

func (s *Scheduler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
