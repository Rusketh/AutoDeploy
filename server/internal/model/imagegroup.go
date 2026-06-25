package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ImageGroupRepo owns CRUD for image_group rows.
type ImageGroupRepo struct{ db *storage.DB }

func NewImageGroupRepo(db *storage.DB) *ImageGroupRepo { return &ImageGroupRepo{db: db} }

func (r *ImageGroupRepo) Create(ctx context.Context, in ImageGroup) (ImageGroup, error) {
	if err := validateName(in.Name); err != nil {
		return ImageGroup{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO image_group (name, description)
		VALUES (?, ?)`,
		in.Name, in.Description)
	if err != nil {
		if isUniqueErr(err) {
			return ImageGroup{}, fmt.Errorf("image group %q: %w", in.Name, ErrConflict)
		}
		return ImageGroup{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

func (r *ImageGroupRepo) Get(ctx context.Context, id ID) (ImageGroup, error) {
	var v ImageGroup
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM image_group WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImageGroup{}, fmt.Errorf("image group %d: %w", id, ErrNotFound)
	}
	return v, err
}

func (r *ImageGroupRepo) List(ctx context.Context) ([]ImageGroup, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, created_at, updated_at
		FROM image_group ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageGroup
	for rows.Next() {
		var v ImageGroup
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *ImageGroupRepo) Update(ctx context.Context, in ImageGroup) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE image_group
		SET name=?, description=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("image group %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image group %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

// Delete removes the group. Images in the group are automatically unlinked
// by the ON DELETE SET NULL foreign key — deletion never blocks.
func (r *ImageGroupRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM image_group WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image group %d: %w", id, ErrNotFound)
	}
	return nil
}

// RefCount returns the number of images currently assigned to this group.
func (r *ImageGroupRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE group_id=?`, id).Scan(&n)
	return n, err
}
