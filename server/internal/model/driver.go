package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

// validateDriverFilter checks that filter_json parses as a recognised SMBIOS
// filter (keys are in match.AllowedKeys). An empty filter is allowed at
// save time — the matcher treats it as "matches nothing", which is the
// safe default for an in-progress filter.
func validateDriverFilter(raw string) error {
	if raw == "" || raw == "{}" {
		return nil
	}
	_, err := match.ParseFilter(raw)
	return err
}

// DriverPackageRepo is the repository for driver_package and its child
// driver_filter rows.
type DriverPackageRepo struct{ db *storage.DB }

func NewDriverPackageRepo(db *storage.DB) *DriverPackageRepo {
	return &DriverPackageRepo{db: db}
}

func (r *DriverPackageRepo) Create(ctx context.Context, in DriverPackage) (DriverPackage, error) {
	if err := validateName(in.Name); err != nil {
		return DriverPackage{}, err
	}
	for _, f := range in.Filters {
		if err := validateDriverFilter(f.FilterJSON); err != nil {
			return DriverPackage{}, fmt.Errorf("%w: %v", ErrValidation, err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return DriverPackage{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO driver_package (name, description, storage_path, size_bytes)
		VALUES (?, ?, ?, ?)`,
		in.Name, in.Description, in.StoragePath, in.SizeBytes)
	if err != nil {
		if isUniqueErr(err) {
			return DriverPackage{}, fmt.Errorf("driver package %q: %w", in.Name, ErrConflict)
		}
		return DriverPackage{}, err
	}
	id, _ := res.LastInsertId()
	for _, f := range in.Filters {
		fj := f.FilterJSON
		if fj == "" {
			fj = "{}"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO driver_filter (driver_package_id, filter_json) VALUES (?, ?)`,
			id, fj); err != nil {
			return DriverPackage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return DriverPackage{}, err
	}
	return r.Get(ctx, ID(id))
}

func (r *DriverPackageRepo) Get(ctx context.Context, id ID) (DriverPackage, error) {
	var v DriverPackage
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, storage_path, size_bytes, created_at, updated_at
		FROM driver_package WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.StoragePath, &v.SizeBytes,
		&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DriverPackage{}, fmt.Errorf("driver package %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return DriverPackage{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, filter_json FROM driver_filter
		WHERE driver_package_id=? ORDER BY id`, id)
	if err != nil {
		return DriverPackage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var f DriverFilter
		if err := rows.Scan(&f.ID, &f.FilterJSON); err != nil {
			return DriverPackage{}, err
		}
		v.Filters = append(v.Filters, f)
	}
	return v, rows.Err()
}

func (r *DriverPackageRepo) List(ctx context.Context) ([]DriverPackage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, storage_path, size_bytes, created_at, updated_at
		FROM driver_package ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DriverPackage
	for rows.Next() {
		var v DriverPackage
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.StoragePath,
			&v.SizeBytes, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load filters per package. Small N expected (administrators curate the
	// driver library), so a per-row query is fine; revisit if it bites.
	for i := range out {
		fs, err := r.filtersFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Filters = fs
	}
	return out, nil
}

func (r *DriverPackageRepo) filtersFor(ctx context.Context, pkgID ID) ([]DriverFilter, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, filter_json FROM driver_filter
		WHERE driver_package_id=? ORDER BY id`, pkgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DriverFilter
	for rows.Next() {
		var f DriverFilter
		if err := rows.Scan(&f.ID, &f.FilterJSON); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Update replaces the driver package metadata AND its filter set wholesale.
// This is simpler and safer than diffing: filters are small and the operator
// expects "what I see is what I save".
func (r *DriverPackageRepo) Update(ctx context.Context, in DriverPackage) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	for _, f := range in.Filters {
		if err := validateDriverFilter(f.FilterJSON); err != nil {
			return fmt.Errorf("%w: %v", ErrValidation, err)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE driver_package
		SET name=?, description=?, storage_path=?, size_bytes=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.StoragePath, in.SizeBytes, in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("driver package %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("driver package %d: %w", in.ID, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM driver_filter WHERE driver_package_id=?`, in.ID); err != nil {
		return err
	}
	for _, f := range in.Filters {
		fj := f.FilterJSON
		if fj == "" {
			fj = "{}"
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO driver_filter (driver_package_id, filter_json) VALUES (?, ?)`,
			in.ID, fj); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes a driver package. Driver packages are not directly linked
// from images (matching is global in Phase 4), so there are no reference
// counts to guard. The ON DELETE CASCADE on driver_filter takes the filters
// with it.
func (r *DriverPackageRepo) Delete(ctx context.Context, id ID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM driver_package WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("driver package %d: %w", id, ErrNotFound)
	}
	return nil
}
