package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// WebhookSender delivers events to configured webhook endpoints.
type WebhookSender struct {
	Webhooks *model.WebhookRepo
	Logger   *slog.Logger
	Client   *http.Client
}

// NewWebhookSender creates a sender with sensible defaults.
func NewWebhookSender(wh *model.WebhookRepo, logger *slog.Logger) *WebhookSender {
	return &WebhookSender{
		Webhooks: wh,
		Logger:   logger,
		Client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Deliver sends the event to all matching enabled webhooks.
func (s *WebhookSender) Deliver(ctx context.Context, ev Event) {
	hooks, err := s.Webhooks.ListEnabled(ctx)
	if err != nil {
		if s.Logger != nil {
			s.Logger.Error("notify.webhook.list_failed", slog.String("error", err.Error()))
		}
		return
	}
	for _, wh := range hooks {
		if !s.matches(wh, ev) {
			continue
		}
		s.deliverOne(ctx, wh, ev)
	}
}

func (s *WebhookSender) matches(wh model.Webhook, ev Event) bool {
	if !SeverityAtLeast(ev.Severity, wh.MinSeverity) {
		return false
	}
	for _, e := range wh.Events {
		if e == "*" || e == ev.Name {
			return true
		}
	}
	return false
}

func (s *WebhookSender) deliverOne(ctx context.Context, wh model.Webhook, ev Event) {
	payload, _ := json.Marshal(ev)

	var lastErr error
	var lastCode *int
	var lastResp string
	var totalDuration time.Duration

	delays := []time.Duration{0, 5 * time.Second, 30 * time.Second}
retry:
	for attempt, delay := range delays {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				// Stop retrying when the context is cancelled. A bare
				// break here would only exit the select, letting the
				// loop fall through into another doPost on a dead ctx.
				lastErr = ctx.Err()
				break retry
			case <-time.After(delay):
			}
		}

		start := time.Now()
		code, resp, err := s.doPost(ctx, wh, payload)
		elapsed := time.Since(start)
		totalDuration += elapsed

		lastCode = code
		lastResp = resp
		lastErr = err

		if err == nil && code != nil && *code >= 200 && *code < 300 {
			break
		}
	}

	delivery := model.WebhookDelivery{
		WebhookID:   wh.ID,
		Event:       ev.Name,
		Payload:     string(payload),
		StatusCode:  lastCode,
		Response:    lastResp,
		AttemptedAt: time.Now(),
		DurationMs:  int(totalDuration.Milliseconds()),
	}
	if lastErr != nil {
		delivery.Error = lastErr.Error()
	}
	if err := s.Webhooks.RecordDelivery(ctx, delivery); err != nil {
		if s.Logger != nil {
			s.Logger.Error("notify.webhook.record_failed", slog.String("error", err.Error()))
		}
	}
}

func (s *WebhookSender) doPost(ctx context.Context, wh model.Webhook, payload []byte) (*int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "AutoDeploy-Webhook/1.0")

	if wh.Secret != "" {
		mac := hmac.New(sha256.New, []byte(wh.Secret))
		mac.Write(payload)
		sig := hex.EncodeToString(mac.Sum(nil))
		req.Header.Set("X-AutoDeploy-Signature", fmt.Sprintf("sha256=%s", sig))
	}

	for k, v := range wh.Headers {
		req.Header.Set(k, v)
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	code := resp.StatusCode
	return &code, string(body), nil
}

// DeliverTest sends a test payload to a specific webhook.
func (s *WebhookSender) DeliverTest(ctx context.Context, wh model.Webhook) error {
	ev := Event{
		Name:       "system.test",
		Severity:   SevInfo,
		OccurredAt: time.Now(),
		Title:      "Test webhook from AutoDeploy",
		Body:       "If you received this, your webhook is configured correctly.",
		Actor:      "system",
	}
	s.deliverOne(ctx, wh, ev)
	return nil
}
