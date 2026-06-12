package notify

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/rusketh/autodeploy/server/internal/auth"
	"github.com/rusketh/autodeploy/server/internal/model"
)

// Emitter fans out events to all configured notification channels.
type Emitter struct {
	Notifications *model.NotificationRepo
	Webhooks      *model.WebhookRepo
	Users         *auth.Repo
	Logger        *slog.Logger

	// Email is nil when SMTP is not configured.
	Email *EmailSender

	// WebhookSender delivers webhook payloads.
	WebhookSender *WebhookSender

	// BaseURLFn returns the operator-configured external URL (may be "").
	// Event links are portal-relative ("/portal/..."); email recipients are
	// outside the portal, so the email channel prefixes them with this.
	BaseURLFn func() string

	work chan func()
	done chan struct{}
	once sync.Once
}

const workerCount = 10

// Start launches the background worker pool. Call Stop to drain.
func (e *Emitter) Start() {
	e.work = make(chan func(), 256)
	e.done = make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(workerCount)
	go func() {
		for i := 0; i < workerCount; i++ {
			go func() {
				defer wg.Done()
				for fn := range e.work {
					fn()
				}
			}()
		}
		wg.Wait()
		close(e.done)
	}()
}

// Stop drains the work queue. Blocks until all in-flight work finishes.
func (e *Emitter) Stop() {
	e.once.Do(func() {
		if e.work != nil {
			close(e.work)
			<-e.done
		}
	})
}

// Emit fans out an event to all channels asynchronously. Safe to call on a
// nil emitter (no-op) so handlers don't need a guard at every call site.
func (e *Emitter) Emit(ctx context.Context, ev Event) {
	if e == nil {
		return
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	if ev.Severity == "" {
		ev.Severity = SevInfo
	}
	// Callers pass request-scoped contexts that are cancelled the moment the
	// handler returns; the fan-out below runs after that on worker
	// goroutines. Keep the context's values (logging) but not its
	// cancellation, or every DB insert/SMTP send would fail mid-flight.
	ctx = context.WithoutCancel(ctx)

	e.submit(func() { e.fanOutPortal(ctx, ev) })
	e.submit(func() { e.fanOutEmail(ctx, ev) })
	e.submit(func() { e.fanOutWebhooks(ctx, ev) })
}

func (e *Emitter) submit(fn func()) {
	if e.work == nil {
		go fn()
		return
	}
	select {
	case e.work <- fn:
	default:
		go fn()
	}
}

func (e *Emitter) fanOutPortal(ctx context.Context, ev Event) {
	if e.Notifications == nil || e.Users == nil {
		return
	}
	users, err := e.Users.ListUsers(ctx)
	if err != nil {
		e.log("portal.list_users_failed", err)
		return
	}
	for _, u := range users {
		if u.Disabled {
			continue
		}
		if !e.Notifications.WantsPortal(ctx, u.ID, ev.Name) {
			continue
		}
		n := model.Notification{
			UserID:     u.ID,
			OccurredAt: ev.OccurredAt,
			Event:      ev.Name,
			Severity:   ev.Severity,
			Title:      ev.Title,
			Body:       ev.Body,
			Link:       ev.Link,
		}
		if err := e.Notifications.Insert(ctx, n); err != nil {
			e.log("portal.insert_failed", err)
		}
	}
}

func (e *Emitter) fanOutEmail(ctx context.Context, ev Event) {
	if e.Email == nil || e.Notifications == nil || e.Users == nil {
		return
	}
	// Make the "View in Portal" link absolute for mail clients.
	if e.BaseURLFn != nil && strings.HasPrefix(ev.Link, "/") {
		if base := strings.TrimSuffix(e.BaseURLFn(), "/"); base != "" {
			ev.Link = base + ev.Link
		}
	}
	users, err := e.Users.ListUsers(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		if u.Disabled || u.Email == "" {
			continue
		}
		if !e.Notifications.WantsEmail(ctx, u.ID, ev.Name) {
			continue
		}
		if err := e.Email.Send(ctx, u.Email, ev); err != nil {
			e.log("email.send_failed", err)
		}
	}
}

func (e *Emitter) fanOutWebhooks(ctx context.Context, ev Event) {
	if e.WebhookSender == nil {
		return
	}
	e.WebhookSender.Deliver(ctx, ev)
}

func (e *Emitter) log(msg string, err error) {
	if e.Logger != nil {
		e.Logger.Error("notify."+msg, slog.String("error", err.Error()))
	}
}
