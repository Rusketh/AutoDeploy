package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// marshalEditions serialises an editions slice to the JSON stored in
// editions_json; nil/empty becomes "[]".
func marshalEditions(e []string) string {
	if len(e) == 0 {
		return "[]"
	}
	b, err := json.Marshal(e)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// unmarshalEditions parses editions_json; bad/empty JSON yields nil.
func unmarshalEditions(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// ISORepo is the repository for ISO rows.
type ISORepo struct{ db *storage.DB }

// NewISORepo returns a repository bound to db.
func NewISORepo(db *storage.DB) *ISORepo { return &ISORepo{db: db} }

// Create inserts a new ISO. Returns ErrConflict on a duplicate name and
// ErrValidation if required fields are missing.
func (r *ISORepo) Create(ctx context.Context, in ISO) (ISO, error) {
	if err := validateName(in.Name); err != nil {
		return ISO{}, err
	}
	if strings.TrimSpace(in.OSType) == "" {
		return ISO{}, fmt.Errorf("%w: os_type required", ErrValidation)
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO iso (name, description, os_type, storage_path, size_bytes)
		VALUES (?, ?, ?, ?, ?)`,
		in.Name, in.Description, in.OSType, in.StoragePath, in.SizeBytes)
	if err != nil {
		if isUniqueErr(err) {
			return ISO{}, fmt.Errorf("iso %q: %w", in.Name, ErrConflict)
		}
		return ISO{}, fmt.Errorf("insert iso: %w", err)
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

// Get returns the ISO with id, or ErrNotFound.
func (r *ISORepo) Get(ctx context.Context, id ID) (ISO, error) {
	var v ISO
	var prepared sql.NullTime
	var editions string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, os_type, storage_path, size_bytes,
		       created_at, updated_at,
		       install_image_format, install_image_bytes, swm_parts,
		       bootloader_present, prep_error, media_prepared_at,
		       media_file_count, media_total_bytes, boot_wim_present, editions_json
		FROM iso WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.OSType, &v.StoragePath,
		&v.SizeBytes, &v.CreatedAt, &v.UpdatedAt,
		&v.InstallImageFormat, &v.InstallImageBytes, &v.SWMParts,
		&v.BootloaderPresent, &v.PrepError, &prepared,
		&v.MediaFileCount, &v.MediaTotalBytes, &v.BootWimPresent, &editions)
	if errors.Is(err, sql.ErrNoRows) {
		return ISO{}, fmt.Errorf("iso %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return ISO{}, err
	}
	if prepared.Valid {
		v.MediaPreparedAt = &prepared.Time
	}
	v.Editions = unmarshalEditions(editions)
	return v, nil
}

// List returns all ISO rows ordered by name.
func (r *ISORepo) List(ctx context.Context) ([]ISO, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, os_type, storage_path, size_bytes,
		       created_at, updated_at,
		       install_image_format, install_image_bytes, swm_parts,
		       bootloader_present, prep_error, media_prepared_at,
		       media_file_count, media_total_bytes, boot_wim_present, editions_json
		FROM iso ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ISO
	for rows.Next() {
		var v ISO
		var prepared sql.NullTime
		var editions string
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.OSType,
			&v.StoragePath, &v.SizeBytes, &v.CreatedAt, &v.UpdatedAt,
			&v.InstallImageFormat, &v.InstallImageBytes, &v.SWMParts,
			&v.BootloaderPresent, &v.PrepError, &prepared,
			&v.MediaFileCount, &v.MediaTotalBytes, &v.BootWimPresent, &editions); err != nil {
			return nil, err
		}
		if prepared.Valid {
			v.MediaPreparedAt = &prepared.Time
		}
		v.Editions = unmarshalEditions(editions)
		out = append(out, v)
	}
	return out, rows.Err()
}

// Update modifies an existing ISO. Returns ErrNotFound if id is unknown.
func (r *ISORepo) Update(ctx context.Context, in ISO) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if strings.TrimSpace(in.OSType) == "" {
		return fmt.Errorf("%w: os_type required", ErrValidation)
	}
	var prepared sql.NullTime
	if in.MediaPreparedAt != nil {
		prepared = sql.NullTime{Time: *in.MediaPreparedAt, Valid: true}
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE iso
		SET name=?, description=?, os_type=?, storage_path=?, size_bytes=?,
		    install_image_format=?, install_image_bytes=?, swm_parts=?,
		    bootloader_present=?, prep_error=?, media_prepared_at=?,
		    media_file_count=?, media_total_bytes=?, boot_wim_present=?,
		    editions_json=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.OSType, in.StoragePath, in.SizeBytes,
		in.InstallImageFormat, in.InstallImageBytes, in.SWMParts,
		in.BootloaderPresent, in.PrepError, prepared,
		in.MediaFileCount, in.MediaTotalBytes, in.BootWimPresent,
		marshalEditions(in.Editions), in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("iso %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("iso %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

// Delete removes the ISO. Returns ErrInUse if any image links it.
func (r *ISORepo) Delete(ctx context.Context, id ID) error {
	refs, err := r.RefCount(ctx, id)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("iso %d: %w (referenced by %d images)", id, ErrInUse, refs)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM iso WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("iso %d: %w", id, ErrNotFound)
	}
	return nil
}

// RefCount returns the number of images that link this ISO directly.
func (r *ISORepo) RefCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE iso_id=?`, id).Scan(&n)
	return n, err
}
