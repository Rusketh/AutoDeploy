package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// UnattendRepo is the repository for Unattend rows.
type UnattendRepo struct{ db *storage.DB }

func NewUnattendRepo(db *storage.DB) *UnattendRepo { return &UnattendRepo{db: db} }

func (r *UnattendRepo) Create(ctx context.Context, in Unattend) (Unattend, error) {
	if err := validateName(in.Name); err != nil {
		return Unattend{}, err
	}
	if in.SettingsJSON == "" {
		in.SettingsJSON = "{}"
	}
	if !json.Valid([]byte(in.SettingsJSON)) {
		return Unattend{}, fmt.Errorf("%w: settings_json is not valid JSON", ErrValidation)
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO unattend (name, description, settings_json)
		VALUES (?, ?, ?)`,
		in.Name, in.Description, in.SettingsJSON)
	if err != nil {
		if isUniqueErr(err) {
			return Unattend{}, fmt.Errorf("unattend %q: %w", in.Name, ErrConflict)
		}
		return Unattend{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

func (r *UnattendRepo) Get(ctx context.Context, id ID) (Unattend, error) {
	var v Unattend
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, settings_json, created_at, updated_at
		FROM unattend WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.SettingsJSON, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Unattend{}, fmt.Errorf("unattend %d: %w", id, ErrNotFound)
	}
	return v, err
}

func (r *UnattendRepo) List(ctx context.Context) ([]Unattend, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, settings_json, created_at, updated_at
		FROM unattend ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unattend
	for rows.Next() {
		var v Unattend
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.SettingsJSON,
			&v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *UnattendRepo) Update(ctx context.Context, in Unattend) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.SettingsJSON == "" {
		in.SettingsJSON = "{}"
	}
	if !json.Valid([]byte(in.SettingsJSON)) {
		return fmt.Errorf("%w: settings_json is not valid JSON", ErrValidation)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE unattend
		SET name=?, description=?, settings_json=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.SettingsJSON, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("unattend %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("unattend %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

func (r *UnattendRepo) Delete(ctx context.Context, id ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var refs int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE unattend_id=?`, id).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("unattend %d: %w (referenced by %d images)", id, ErrInUse, refs)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM unattend WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("unattend %d: %w", id, ErrNotFound)
	}
	return tx.Commit()
}

// Clone creates a copy of the unattend with the given name, preserving all settings.
func (r *UnattendRepo) Clone(ctx context.Context, id ID, newName string) (Unattend, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return Unattend{}, err
	}
	src.ID = 0
	src.Name = newName
	return r.Create(ctx, src)
}

func (r *UnattendRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE unattend_id=?`, id).Scan(&n)
	return n, err
}
