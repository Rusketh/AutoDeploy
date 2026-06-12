package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// OfflineWatcher emits machine.offline when a machine hasn't checked in for
// the operator-configured threshold (Settings > Notifications). One event
// per offline episode: the inventory records that the episode was notified
// and clears the marker on the machine's next contact. Mirrors the
// retention/bulksched scheduler shape.
type OfflineWatcher struct {
	Inventory *model.InventoryRepo
	Emitter   *Emitter
	// ThresholdMinutes is read each tick so a portal change takes effect
	// without restart. Return <=0 to disable.
	ThresholdMinutes func() int
	Interval         time.Duration // 0 -> 1m
	Logger           *slog.Logger
}

// Start runs the watcher until ctx is cancelled.
func (w *OfflineWatcher) Start(ctx context.Context) {
	if w.Interval == 0 {
		w.Interval = time.Minute
	}
	t := time.NewTicker(w.Interval)
	defer t.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *OfflineWatcher) runOnce(ctx context.Context) {
	if w.Inventory == nil || w.Emitter == nil || w.ThresholdMinutes == nil {
		return
	}
	threshold := w.ThresholdMinutes()
	if threshold <= 0 {
		return
	}
	machines, err := w.Inventory.ListOfflineUnnotified(ctx, threshold)
	if err != nil {
		w.log().Warn("notify.offline.list.fail", slog.String("error", err.Error()))
		return
	}
	if len(machines) == 0 {
		return
	}
	ids := make([]model.ID, 0, len(machines))
	for _, m := range machines {
		name := m.ReportedName
		if name == "" {
			name = fmt.Sprintf("machine #%d", int64(m.ID))
		}
		w.Emitter.Emit(ctx, Event{
			Name:     EventMachineOffline,
			Severity: SevWarning,
			Title:    "Machine offline: " + name,
			Body: fmt.Sprintf("%s has not checked in since %s (threshold %d minutes).",
				name, m.LastSeen.Local().Format("Mon 2 Jan 15:04"), threshold),
			Link:  fmt.Sprintf("/portal/machines/%d", int64(m.ID)),
			Actor: "system",
		})
		ids = append(ids, m.ID)
	}
	// Mark BEFORE the async fan-out completes is fine: the episode is keyed
	// on the row, not the delivery, and a failed channel shouldn't re-spam
	// the working ones next tick.
	if err := w.Inventory.MarkOfflineNotified(ctx, ids); err != nil {
		w.log().Warn("notify.offline.mark.fail", slog.String("error", err.Error()))
	}
	w.log().Info("notify.offline.emitted",
		slog.String("actor", "system"),
		slog.String("action", "machine.offline"),
		slog.String("target", "machines"),
		slog.Int("count", len(ids)),
		slog.Int("threshold_minutes", threshold))
}

func (w *OfflineWatcher) log() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
