package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// SoftwarePackageRepo is the repository for software_package rows.
type SoftwarePackageRepo struct{ db *storage.DB }

func NewSoftwarePackageRepo(db *storage.DB) *SoftwarePackageRepo {
	return &SoftwarePackageRepo{db: db}
}

func (r *SoftwarePackageRepo) Create(ctx context.Context, in SoftwarePackage) (SoftwarePackage, error) {
	if err := validateSoftware(&in); err != nil {
		return SoftwarePackage{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO software_package
		    (name, description, storage_path, size_bytes, detection_json, steps_json)
		VALUES (?, ?, ?, ?, ?, ?)`,
		in.Name, in.Description, in.StoragePath, in.SizeBytes,
		in.DetectionJSON, in.StepsJSON)
	if err != nil {
		if isUniqueErr(err) {
			return SoftwarePackage{}, fmt.Errorf("software package %q: %w", in.Name, ErrConflict)
		}
		return SoftwarePackage{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, ID(id))
}

func (r *SoftwarePackageRepo) Get(ctx context.Context, id ID) (SoftwarePackage, error) {
	var v SoftwarePackage
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, storage_path, size_bytes,
		       detection_json, steps_json, created_at, updated_at
		FROM software_package WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.StoragePath, &v.SizeBytes,
		&v.DetectionJSON, &v.StepsJSON, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwarePackage{}, fmt.Errorf("software package %d: %w", id, ErrNotFound)
	}
	return v, err
}

func (r *SoftwarePackageRepo) List(ctx context.Context) ([]SoftwarePackage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, storage_path, size_bytes,
		       detection_json, steps_json, created_at, updated_at
		FROM software_package ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwarePackage
	for rows.Next() {
		var v SoftwarePackage
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.StoragePath,
			&v.SizeBytes, &v.DetectionJSON, &v.StepsJSON,
			&v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *SoftwarePackageRepo) Update(ctx context.Context, in SoftwarePackage) error {
	if err := validateSoftware(&in); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE software_package
		SET name=?, description=?, storage_path=?, size_bytes=?,
		    detection_json=?, steps_json=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.StoragePath, in.SizeBytes,
		in.DetectionJSON, in.StepsJSON, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("software package %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("software package %d: %w", in.ID, ErrNotFound)
	}
	return nil
}

func (r *SoftwarePackageRepo) Delete(ctx context.Context, id ID) error {
	refs, err := r.RefCount(ctx, id)
	if err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("software package %d: %w (linked by %d images)", id, ErrInUse, refs)
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM software_package WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("software package %d: %w", id, ErrNotFound)
	}
	return nil
}

// RefCount counts direct image -> software package links. Software loadout
// links land in Phase 7 and add to this number.
func (r *SoftwarePackageRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image_software_package WHERE software_package_id=?`,
		id).Scan(&n)
	return n, err
}

func validateSoftware(in *SoftwarePackage) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.DetectionJSON == "" {
		in.DetectionJSON = "[]"
	}
	if in.StepsJSON == "" {
		in.StepsJSON = "[]"
	}
	if !json.Valid([]byte(in.DetectionJSON)) {
		return fmt.Errorf("%w: detection_json is not valid JSON", ErrValidation)
	}
	if !json.Valid([]byte(in.StepsJSON)) {
		return fmt.Errorf("%w: steps_json is not valid JSON", ErrValidation)
	}
	return nil
}
