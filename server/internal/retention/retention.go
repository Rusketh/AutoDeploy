// Package retention runs the periodic housekeeping tasks: log pruning
// (Phase 14) and any future scheduled cleanup. A single goroutine
// wakes on Tick and runs each task once. Retention days is read on
// each tick so an operator who changes it in the portal sees the new
// value take effect within an interval.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// Scheduler runs periodic retention tasks. Construct, then Start.
type Scheduler struct {
	Logs *model.LogRepo
	// RetentionDays is a callback so the scheduler picks up portal
	// changes on the next tick without a restart. Return 0 to disable.
	RetentionDays func() int
	Interval      time.Duration // 0 -> hourly
	Logger        *slog.Logger
	// SessionPrune, when set, deletes expired sessions on each tick.
	SessionPrune func(ctx context.Context) error
	// Notifications, when set, prunes old notification rows.
	Notifications *model.NotificationRepo
	// NotifyRetentionDays returns the notification retention period.
	NotifyRetentionDays func() int
	// WebhookRepo, when set, prunes old webhook delivery rows.
	WebhookRepo *model.WebhookRepo
	// Assets, when set, sweeps imported-asset rows that have matched a machine
	// or whose serial+model is already in inventory (self-cleaning).
	Assets *model.ImportedAssetRepo
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
	days := 0
	if s.RetentionDays != nil {
		days = s.RetentionDays()
	}
	if s.Logs != nil && days > 0 {
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		n, err := s.Logs.Prune(ctx, cutoff)
		if err != nil {
			s.log().Warn("retention.log_prune.fail",
				slog.String("error", err.Error()))
		} else {
			s.log().Info("retention.log_prune.ok",
				slog.Int64("removed", n),
				slog.Int("days", days),
				slog.String("cutoff", cutoff.Format(time.RFC3339)))
		}
	}

	if s.SessionPrune != nil {
		if err := s.SessionPrune(ctx); err != nil {
			s.log().Warn("retention.session_prune.fail",
				slog.String("error", err.Error()))
		}
	}

	if s.Notifications != nil && s.NotifyRetentionDays != nil {
		nd := s.NotifyRetentionDays()
		if nd > 0 {
			cutoff := time.Now().Add(-time.Duration(nd) * 24 * time.Hour)
			n, err := s.Notifications.Prune(ctx, cutoff)
			if err != nil {
				s.log().Warn("retention.notification_prune.fail",
					slog.String("error", err.Error()))
			} else if n > 0 {
				s.log().Info("retention.notification_prune.ok",
					slog.Int64("removed", n), slog.Int("days", nd))
			}
		}
	}

	if s.WebhookRepo != nil {
		cutoff := time.Now().Add(-30 * 24 * time.Hour)
		n, err := s.WebhookRepo.PruneDeliveries(ctx, cutoff)
		if err != nil {
			s.log().Warn("retention.webhook_delivery_prune.fail",
				slog.String("error", err.Error()))
		} else if n > 0 {
			s.log().Info("retention.webhook_delivery_prune.ok",
				slog.Int64("removed", n))
		}
	}

	if s.Assets != nil {
		n, err := s.Assets.SweepMatched(ctx)
		if err != nil {
			s.log().Warn("retention.imported_asset_sweep.fail",
				slog.String("error", err.Error()))
		} else if n > 0 {
			s.log().Info("retention.imported_asset_sweep.ok",
				slog.Int64("removed", n))
		}
	}
}

func (s *Scheduler) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
