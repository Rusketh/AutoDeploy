package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rusketh/autodeploy/server/internal/secrets"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ImageDomainJoin is an image's Active Directory domain-join configuration,
// performed by the AGENT after first boot (reliable: full networking + DNS),
// replacing the fragile unattend online-join during specialize. The join
// password is a secret: encrypted at rest via secrets.Box and only ever
// returned to the deploying agent (never logged, never in the unattend XML).
type ImageDomainJoin struct {
	ImageID  ID     `json:"image_id"`
	Enabled  bool   `json:"enabled"`
	Domain   string `json:"domain"`
	OU       string `json:"ou"`
	JoinUser string `json:"join_user"`
	// HasPassword reports that a join password is stored, without exposing it.
	HasPassword bool      `json:"has_password"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// DomainJoinRepo stores per-image domain-join config. The join password is
// encrypted with the same secrets.Box used for BitLocker PINs.
type DomainJoinRepo struct {
	db *storage.DB
	bx *secrets.Box
}

// NewDomainJoinRepo binds the repo to a storage handle and a secrets box.
func NewDomainJoinRepo(db *storage.DB, bx *secrets.Box) *DomainJoinRepo {
	return &DomainJoinRepo{db: db, bx: bx}
}

// Get returns the image's domain-join config. ErrNotFound when none is set
// (the common case: most images don't join a domain).
func (r *DomainJoinRepo) Get(ctx context.Context, imageID ID) (ImageDomainJoin, error) {
	var v ImageDomainJoin
	var enabled int
	var cipher string
	err := r.db.QueryRowContext(ctx, `
		SELECT image_id, enabled, domain, ou, join_user, join_password_cipher, updated_at
		FROM image_domain_join WHERE image_id=?`, imageID).Scan(
		&v.ImageID, &enabled, &v.Domain, &v.OU, &v.JoinUser, &cipher, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageDomainJoin{ImageID: imageID}, fmt.Errorf("domain join for image %d: %w", imageID, ErrNotFound)
	}
	if err != nil {
		return ImageDomainJoin{}, err
	}
	v.Enabled = enabled != 0
	v.HasPassword = cipher != ""
	return v, nil
}

// Set upserts the config. An empty password PRESERVES the stored one (so the
// operator can edit domain/OU/user without re-typing the secret); a non-empty
// password replaces it. Disabling (Enabled=false) keeps the stored values so
// they survive a toggle.
func (r *DomainJoinRepo) Set(ctx context.Context, cfg ImageDomainJoin, password string) error {
	cipher := ""
	if password != "" {
		ct, err := r.bx.Encrypt(password)
		if err != nil {
			return err
		}
		cipher = ct
	} else {
		// Preserve the existing cipher; a missing row scans nothing and
		// leaves cipher empty.
		_ = r.db.QueryRowContext(ctx,
			`SELECT join_password_cipher FROM image_domain_join WHERE image_id=?`,
			cfg.ImageID).Scan(&cipher)
	}
	enabled := 0
	if cfg.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO image_domain_join
		    (image_id, enabled, domain, ou, join_user, join_password_cipher, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(image_id) DO UPDATE SET
		    enabled=excluded.enabled, domain=excluded.domain, ou=excluded.ou,
		    join_user=excluded.join_user,
		    join_password_cipher=excluded.join_password_cipher,
		    updated_at=CURRENT_TIMESTAMP`,
		cfg.ImageID, enabled, cfg.Domain, cfg.OU, cfg.JoinUser, cipher)
	return err
}

// RetrievePassword decrypts the join password. Calls to this function are the
// audited secret-access boundary; never log the return value.
func (r *DomainJoinRepo) RetrievePassword(ctx context.Context, imageID ID) (string, error) {
	var cipher string
	err := r.db.QueryRowContext(ctx,
		`SELECT join_password_cipher FROM image_domain_join WHERE image_id=?`,
		imageID).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("no domain join for image %d: %w", imageID, ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if cipher == "" {
		return "", nil
	}
	return r.bx.Decrypt(cipher)
}
