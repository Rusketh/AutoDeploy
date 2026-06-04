package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// Webhook is a configured outbound webhook endpoint.
type Webhook struct {
	ID          ID        `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Secret      string    `json:"secret,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Events      []string  `json:"events"`
	MinSeverity string    `json:"min_severity"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WebhookDelivery is one delivery attempt to a webhook.
type WebhookDelivery struct {
	ID          ID        `json:"id"`
	WebhookID   ID        `json:"webhook_id"`
	Event       string    `json:"event"`
	Payload     string    `json:"payload"`
	StatusCode  *int      `json:"status_code,omitempty"`
	Response    string    `json:"response"`
	Error       string    `json:"error"`
	AttemptedAt time.Time `json:"attempted_at"`
	DurationMs  int       `json:"duration_ms"`
}

// WebhookRepo handles webhook and delivery CRUD.
type WebhookRepo struct {
	db *storage.DB
	bx *secrets.Box
}

func NewWebhookRepo(db *storage.DB, bx *secrets.Box) *WebhookRepo {
	return &WebhookRepo{db: db, bx: bx}
}

func (r *WebhookRepo) Create(ctx context.Context, wh Webhook) (ID, error) {
	if strings.TrimSpace(wh.Name) == "" {
		return 0, fmt.Errorf("%w: webhook name required", ErrValidation)
	}
	if strings.TrimSpace(wh.URL) == "" {
		return 0, fmt.Errorf("%w: webhook URL required", ErrValidation)
	}
	secretEnc, err := r.encryptOptional(wh.Secret)
	if err != nil {
		return 0, err
	}
	headersEnc, err := r.encryptHeaders(wh.Headers)
	if err != nil {
		return 0, err
	}
	eventsJSON, _ := json.Marshal(wh.Events)
	if len(wh.Events) == 0 {
		eventsJSON = []byte(`["*"]`)
	}
	if wh.MinSeverity == "" {
		wh.MinSeverity = "info"
	}
	now := time.Now()
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO webhook (name, url, secret_enc, headers_enc, events, min_severity, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		wh.Name, wh.URL, secretEnc, headersEnc, string(eventsJSON),
		wh.MinSeverity, boolInt(wh.Enabled), now, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return ID(id), nil
}

func (r *WebhookRepo) Update(ctx context.Context, wh Webhook) error {
	if strings.TrimSpace(wh.Name) == "" {
		return fmt.Errorf("%w: webhook name required", ErrValidation)
	}
	if strings.TrimSpace(wh.URL) == "" {
		return fmt.Errorf("%w: webhook URL required", ErrValidation)
	}
	secretEnc, err := r.encryptOptional(wh.Secret)
	if err != nil {
		return err
	}
	headersEnc, err := r.encryptHeaders(wh.Headers)
	if err != nil {
		return err
	}
	eventsJSON, _ := json.Marshal(wh.Events)
	if len(wh.Events) == 0 {
		eventsJSON = []byte(`["*"]`)
	}
	if wh.MinSeverity == "" {
		wh.MinSeverity = "info"
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE webhook SET name=?, url=?, secret_enc=?, headers_enc=?, events=?,
		       min_severity=?, enabled=?, updated_at=?
		WHERE id=?`,
		wh.Name, wh.URL, secretEnc, headersEnc, string(eventsJSON),
		wh.MinSeverity, boolInt(wh.Enabled), time.Now(), wh.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *WebhookRepo) Get(ctx context.Context, id ID) (Webhook, error) {
	var wh Webhook
	var secretEnc, headersEnc, eventsRaw string
	var enabled int
	var created, updated sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, url, secret_enc, headers_enc, events, min_severity, enabled, created_at, updated_at
		FROM webhook WHERE id=?`, id).Scan(
		&wh.ID, &wh.Name, &wh.URL, &secretEnc, &headersEnc,
		&eventsRaw, &wh.MinSeverity, &enabled, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return wh, ErrNotFound
	}
	if err != nil {
		return wh, err
	}
	wh.Enabled = enabled == 1
	wh.CreatedAt = parseDBTime(created.String)
	wh.UpdatedAt = parseDBTime(updated.String)
	wh.Secret = r.decryptOptional(secretEnc)
	wh.Headers = r.decryptHeaders(headersEnc)
	_ = json.Unmarshal([]byte(eventsRaw), &wh.Events)
	return wh, nil
}

func (r *WebhookRepo) List(ctx context.Context) ([]Webhook, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, url, secret_enc, headers_enc, events, min_severity, enabled, created_at, updated_at
		FROM webhook ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Webhook
	for rows.Next() {
		var wh Webhook
		var secretEnc, headersEnc, eventsRaw string
		var enabled int
		var created, updated sql.NullString
		if err := rows.Scan(&wh.ID, &wh.Name, &wh.URL, &secretEnc, &headersEnc,
			&eventsRaw, &wh.MinSeverity, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		wh.Enabled = enabled == 1
		wh.CreatedAt = parseDBTime(created.String)
		wh.UpdatedAt = parseDBTime(updated.String)
		wh.Secret = r.decryptOptional(secretEnc)
		wh.Headers = r.decryptHeaders(headersEnc)
		_ = json.Unmarshal([]byte(eventsRaw), &wh.Events)
		out = append(out, wh)
	}
	return out, rows.Err()
}

func (r *WebhookRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM webhook WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListEnabled returns only enabled webhooks.
func (r *WebhookRepo) ListEnabled(ctx context.Context) ([]Webhook, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Webhook
	for _, w := range all {
		if w.Enabled {
			out = append(out, w)
		}
	}
	return out, nil
}

// RecordDelivery saves a delivery attempt.
func (r *WebhookRepo) RecordDelivery(ctx context.Context, d WebhookDelivery) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO webhook_delivery (webhook_id, event, payload, status_code, response, error, attempted_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.WebhookID, d.Event, d.Payload, d.StatusCode, d.Response, d.Error, d.AttemptedAt, d.DurationMs)
	return err
}

// ListDeliveries returns recent deliveries for a webhook.
func (r *WebhookRepo) ListDeliveries(ctx context.Context, webhookID ID, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, webhook_id, event, payload, status_code, response, error, attempted_at, duration_ms
		FROM webhook_delivery WHERE webhook_id=?
		ORDER BY attempted_at DESC LIMIT ?`, webhookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		var code sql.NullInt64
		var ts sql.NullString
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload,
			&code, &d.Response, &d.Error, &ts, &d.DurationMs); err != nil {
			return nil, err
		}
		if code.Valid {
			c := int(code.Int64)
			d.StatusCode = &c
		}
		d.AttemptedAt = parseDBTime(ts.String)
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneDeliveries deletes deliveries older than cutoff.
func (r *WebhookRepo) PruneDeliveries(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM webhook_delivery WHERE attempted_at<?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *WebhookRepo) encryptOptional(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	return r.bx.Encrypt(s)
}

func (r *WebhookRepo) decryptOptional(s string) string {
	if s == "" {
		return ""
	}
	plain, err := r.bx.Decrypt(s)
	if err != nil {
		return ""
	}
	return plain
}

func (r *WebhookRepo) encryptHeaders(h map[string]string) (string, error) {
	if len(h) == 0 {
		return "{}", nil
	}
	raw, _ := json.Marshal(h)
	return r.bx.Encrypt(string(raw))
}

func (r *WebhookRepo) decryptHeaders(s string) map[string]string {
	if s == "" || s == "{}" {
		return nil
	}
	plain, err := r.bx.Decrypt(s)
	if err != nil {
		return nil
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(plain), &m)
	return m
}
