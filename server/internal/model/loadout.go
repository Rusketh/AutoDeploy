package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// SoftwareLoadoutRepo owns CRUD for software_loadout + software_loadout_package.
type SoftwareLoadoutRepo struct{ db *storage.DB }

func NewSoftwareLoadoutRepo(db *storage.DB) *SoftwareLoadoutRepo {
	return &SoftwareLoadoutRepo{db: db}
}

func (r *SoftwareLoadoutRepo) Create(ctx context.Context, in SoftwareLoadout) (SoftwareLoadout, error) {
	if err := validateName(in.Name); err != nil {
		return SoftwareLoadout{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return SoftwareLoadout{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO software_loadout (name, description, parent_id)
		VALUES (?, ?, ?)`,
		in.Name, in.Description, nullID(in.ParentID))
	if err != nil {
		if isUniqueErr(err) {
			return SoftwareLoadout{}, fmt.Errorf("loadout %q: %w", in.Name, ErrConflict)
		}
		return SoftwareLoadout{}, err
	}
	id, _ := res.LastInsertId()
	if err := writeLoadoutPackages(ctx, tx, ID(id), in.Packages); err != nil {
		return SoftwareLoadout{}, err
	}
	if err := tx.Commit(); err != nil {
		return SoftwareLoadout{}, err
	}
	return r.Get(ctx, ID(id))
}

func (r *SoftwareLoadoutRepo) Update(ctx context.Context, in SoftwareLoadout) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.ParentID != nil {
		if *in.ParentID == in.ID {
			return fmt.Errorf("loadout %d: %w (self parent)", in.ID, ErrCycle)
		}
		cycle, err := r.parentChainContains(ctx, *in.ParentID, in.ID)
		if err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("loadout %d: %w (parent chain returns to self)", in.ID, ErrCycle)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE software_loadout
		SET name=?, description=?, parent_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, nullID(in.ParentID), in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("loadout %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("loadout %d: %w", in.ID, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM software_loadout_package WHERE loadout_id=?`, in.ID); err != nil {
		return err
	}
	if err := writeLoadoutPackages(ctx, tx, in.ID, in.Packages); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SoftwareLoadoutRepo) Get(ctx context.Context, id ID) (SoftwareLoadout, error) {
	var v SoftwareLoadout
	var parent sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, parent_id, created_at, updated_at
		FROM software_loadout WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &parent, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwareLoadout{}, fmt.Errorf("loadout %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return SoftwareLoadout{}, err
	}
	v.ParentID = idPtr(parent)
	pkgs, err := r.packagesFor(ctx, id)
	if err != nil {
		return SoftwareLoadout{}, err
	}
	v.Packages = pkgs
	return v, nil
}

func (r *SoftwareLoadoutRepo) List(ctx context.Context) ([]SoftwareLoadout, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, parent_id, created_at, updated_at
		FROM software_loadout ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwareLoadout
	for rows.Next() {
		var v SoftwareLoadout
		var parent sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &parent,
			&v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.ParentID = idPtr(parent)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		pkgs, err := r.packagesFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Packages = pkgs
	}
	return out, nil
}

func (r *SoftwareLoadoutRepo) packagesFor(ctx context.Context, id ID) ([]SoftwareLoadoutPackage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT software_package_id, order_value, opt_out
		FROM software_loadout_package
		WHERE loadout_id=?
		ORDER BY order_value, software_package_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwareLoadoutPackage
	for rows.Next() {
		var p SoftwareLoadoutPackage
		var optOut int
		if err := rows.Scan(&p.PackageID, &p.OrderValue, &optOut); err != nil {
			return nil, err
		}
		p.OptOut = optOut != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// Delete removes a loadout. Refuses if any image still links it or any
// child loadout still names it as parent.
func (r *SoftwareLoadoutRepo) Delete(ctx context.Context, id ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var images, children int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE loadout_id=?`, id).Scan(&images); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM software_loadout WHERE parent_id=?`, id).Scan(&children); err != nil {
		return err
	}
	refs := images + children
	if refs > 0 {
		return fmt.Errorf("loadout %d: %w (referenced by %d objects)", id, ErrInUse, refs)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM software_loadout WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("loadout %d: %w", id, ErrNotFound)
	}
	return tx.Commit()
}

// Clone creates a copy of the loadout with the given name, preserving all
// packages and parent references.
func (r *SoftwareLoadoutRepo) Clone(ctx context.Context, id ID, newName string) (SoftwareLoadout, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return SoftwareLoadout{}, err
	}
	src.ID = 0
	src.Name = newName
	return r.Create(ctx, src)
}

// RefCount returns the number of images that link this loadout plus the
// number of child loadouts that point to it.
func (r *SoftwareLoadoutRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var images, children int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE loadout_id=?`, id).Scan(&images); err != nil {
		return 0, err
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM software_loadout WHERE parent_id=?`, id).Scan(&children); err != nil {
		return 0, err
	}
	return images + children, nil
}

func (r *SoftwareLoadoutRepo) parentChainContains(ctx context.Context, start, target ID) (bool, error) {
	const maxDepth = 256
	cur := start
	for depth := 0; depth < maxDepth; depth++ {
		if cur == target {
			return true, nil
		}
		var next sql.NullInt64
		err := r.db.QueryRowContext(ctx,
			`SELECT parent_id FROM software_loadout WHERE id=?`, cur).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !next.Valid {
			return false, nil
		}
		cur = ID(next.Int64)
	}
	return false, fmt.Errorf("loadout parent depth exceeded %d", maxDepth)
}

func writeLoadoutPackages(ctx context.Context, tx *sql.Tx, loadoutID ID, pkgs []SoftwareLoadoutPackage) error {
	for _, p := range pkgs {
		optOut := 0
		if p.OptOut {
			optOut = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO software_loadout_package
			    (loadout_id, software_package_id, order_value, opt_out)
			VALUES (?, ?, ?, ?)`,
			loadoutID, p.PackageID, p.OrderValue, optOut); err != nil {
			return err
		}
	}
	return nil
}
