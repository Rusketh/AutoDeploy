package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// PayloadMirror is an alternate HTTP host that serves the same
// /payload/... tree the primary serves. The manifest endpoint picks
// the most appropriate mirror per request and rewrites payload URLs.
type PayloadMirror struct {
	ID          ID        `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	BaseURL     string    `json:"base_url"` // e.g. https://mirror-eu.corp.example
	Site        string    `json:"site"`     // "" matches any site
	Priority    int       `json:"priority"` // lower = preferred
	Healthy     bool      `json:"healthy"`
	LastChecked time.Time `json:"last_checked"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PayloadMirrorRepo owns payload_mirror.
type PayloadMirrorRepo struct{ db *storage.DB }

func NewPayloadMirrorRepo(db *storage.DB) *PayloadMirrorRepo {
	return &PayloadMirrorRepo{db: db}
}

func (r *PayloadMirrorRepo) Create(ctx context.Context, m PayloadMirror) (PayloadMirror, error) {
	if err := validateName(m.Name); err != nil {
		return PayloadMirror{}, err
	}
	if strings.TrimSpace(m.BaseURL) == "" {
		return PayloadMirror{}, fmt.Errorf("%w: base_url required", ErrValidation)
	}
	if m.Priority == 0 {
		m.Priority = 100
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO payload_mirror (name, description, base_url, site, priority, healthy)
		VALUES (?, ?, ?, ?, ?, 1)`,
		m.Name, m.Description, normaliseBaseURL(m.BaseURL), m.Site, m.Priority)
	if err != nil {
		if isUniqueErr(err) {
			return PayloadMirror{}, fmt.Errorf("mirror %q: %w", m.Name, ErrConflict)
		}
		return PayloadMirror{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

func (r *PayloadMirrorRepo) Update(ctx context.Context, m PayloadMirror) error {
	if err := validateName(m.Name); err != nil {
		return err
	}
	if strings.TrimSpace(m.BaseURL) == "" {
		return fmt.Errorf("%w: base_url required", ErrValidation)
	}
	healthy := 0
	if m.Healthy {
		healthy = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE payload_mirror
		SET name=?, description=?, base_url=?, site=?, priority=?, healthy=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		m.Name, m.Description, normaliseBaseURL(m.BaseURL),
		m.Site, m.Priority, healthy, m.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("mirror %q: %w", m.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("mirror %d: %w", m.ID, ErrNotFound)
	}
	return nil
}

func (r *PayloadMirrorRepo) Get(ctx context.Context, id ID) (PayloadMirror, error) {
	var m PayloadMirror
	var healthy int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, base_url, site, priority, healthy,
		       last_checked, created_at, updated_at
		FROM payload_mirror WHERE id=?`, id).Scan(
		&m.ID, &m.Name, &m.Description, &m.BaseURL, &m.Site,
		&m.Priority, &healthy, &m.LastChecked, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PayloadMirror{}, fmt.Errorf("mirror %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return PayloadMirror{}, err
	}
	m.Healthy = healthy != 0
	return m, nil
}

func (r *PayloadMirrorRepo) List(ctx context.Context) ([]PayloadMirror, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, base_url, site, priority, healthy,
		       last_checked, created_at, updated_at
		FROM payload_mirror ORDER BY site, priority, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PayloadMirror
	for rows.Next() {
		var m PayloadMirror
		var healthy int
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.BaseURL,
			&m.Site, &m.Priority, &healthy, &m.LastChecked,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.Healthy = healthy != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *PayloadMirrorRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM payload_mirror WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("mirror %d: %w", id, ErrNotFound)
	}
	return nil
}

// SetHealth records a mirror's last health-check outcome.
func (r *PayloadMirrorRepo) SetHealth(ctx context.Context, id ID, healthy bool) error {
	h := 0
	if healthy {
		h = 1
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE payload_mirror SET healthy=?, last_checked=CURRENT_TIMESTAMP WHERE id=?`,
		h, id)
	return err
}

// PickFor returns the highest-priority healthy mirror that matches the
// requesting site. Site-specific mirrors win over global ("") mirrors.
// Returns (PayloadMirror{}, false) when no mirror is suitable.
func (r *PayloadMirrorRepo) PickFor(ctx context.Context, site string) (PayloadMirror, bool, error) {
	site = strings.TrimSpace(site)
	// First try the requested site exactly.
	if site != "" {
		var m PayloadMirror
		var healthy int
		err := r.db.QueryRowContext(ctx, `
			SELECT id, name, description, base_url, site, priority, healthy,
			       last_checked, created_at, updated_at
			FROM payload_mirror
			WHERE site=? AND healthy=1
			ORDER BY priority, id
			LIMIT 1`, site).Scan(
			&m.ID, &m.Name, &m.Description, &m.BaseURL, &m.Site,
			&m.Priority, &healthy, &m.LastChecked, &m.CreatedAt, &m.UpdatedAt)
		if err == nil {
			m.Healthy = healthy != 0
			return m, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return PayloadMirror{}, false, err
		}
	}
	// Fall back to a global (site="") mirror.
	var m PayloadMirror
	var healthy int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, base_url, site, priority, healthy,
		       last_checked, created_at, updated_at
		FROM payload_mirror
		WHERE site='' AND healthy=1
		ORDER BY priority, id
		LIMIT 1`).Scan(
		&m.ID, &m.Name, &m.Description, &m.BaseURL, &m.Site,
		&m.Priority, &healthy, &m.LastChecked, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PayloadMirror{}, false, nil
	}
	if err != nil {
		return PayloadMirror{}, false, err
	}
	m.Healthy = healthy != 0
	return m, true, nil
}

// RecordSiteForMachine remembers a machine's last seen site so future
// menu / manifest fetches without a site header still pick the right
// mirror.
func (r *PayloadMirrorRepo) RecordSiteForMachine(ctx context.Context, uuid, site string) error {
	if uuid == "" || site == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE machine_record SET last_site=? WHERE system_uuid=?`,
		site, uuid)
	return err
}

// LastSiteForMachine returns the stored site, or "" if unknown.
func (r *PayloadMirrorRepo) LastSiteForMachine(ctx context.Context, uuid string) (string, error) {
	if uuid == "" {
		return "", nil
	}
	var site string
	err := r.db.QueryRowContext(ctx,
		`SELECT last_site FROM machine_record WHERE system_uuid=?`, uuid).Scan(&site)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return site, err
}

// normaliseBaseURL strips any trailing slash so the rewriter can join
// without worrying about doubles.
func normaliseBaseURL(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	return s
}
