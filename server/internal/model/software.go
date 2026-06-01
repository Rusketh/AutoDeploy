package model

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rusketh/autodeploy/server/internal/storage"
	"github.com/rusketh/autodeploy/server/internal/swspec"
)

// marshalDeps serialises a dependency list to the JSON stored in
// depends_on_json. nil/empty becomes "[]".
func marshalDeps(deps []ID) string {
	if len(deps) == 0 {
		return "[]"
	}
	b, err := json.Marshal(deps)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// unmarshalDeps parses depends_on_json into a dependency list. Bad/empty
// JSON yields nil rather than an error -- a corrupt sidecar shouldn't make
// a package unreadable.
func unmarshalDeps(s string) []ID {
	if s == "" || s == "[]" {
		return nil
	}
	var out []ID
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

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
		    (name, description, storage_path, payload_filename,
		     size_bytes, detection_json, steps_json, depends_on_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Description, in.StoragePath, in.PayloadFilename,
		in.SizeBytes, in.DetectionJSON, in.StepsJSON, marshalDeps(in.DependsOn))
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
	var deps string
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, storage_path, payload_filename,
		       size_bytes, detection_json, steps_json, depends_on_json, created_at, updated_at
		FROM software_package WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.StoragePath, &v.PayloadFilename,
		&v.SizeBytes, &v.DetectionJSON, &v.StepsJSON, &deps, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwarePackage{}, fmt.Errorf("software package %d: %w", id, ErrNotFound)
	}
	v.DependsOn = unmarshalDeps(deps)
	return v, err
}

func (r *SoftwarePackageRepo) List(ctx context.Context) ([]SoftwarePackage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, storage_path, payload_filename,
		       size_bytes, detection_json, steps_json, depends_on_json, created_at, updated_at
		FROM software_package ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwarePackage
	for rows.Next() {
		var v SoftwarePackage
		var deps string
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.StoragePath,
			&v.PayloadFilename, &v.SizeBytes, &v.DetectionJSON, &v.StepsJSON,
			&deps, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.DependsOn = unmarshalDeps(deps)
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
		SET name=?, description=?, storage_path=?, payload_filename=?,
		    size_bytes=?, detection_json=?, steps_json=?, depends_on_json=?,
		    updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.StoragePath, in.PayloadFilename,
		in.SizeBytes, in.DetectionJSON, in.StepsJSON, marshalDeps(in.DependsOn), in.ID)
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

// Delete removes a software package and, in the same transaction, strips
// every reference to it from images and loadouts. Cascade-cleaning here
// (rather than refusing while referenced) means an operator can delete a
// package without first hunting down every image/loadout that links it,
// and -- crucially -- it can never leave a dangling reference that would
// later resolve into a manifest pointing at a package that no longer
// exists. RefCount remains available for the portal to warn before the
// operator confirms.
func (r *SoftwarePackageRepo) Delete(ctx context.Context, id ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM image_software_package WHERE software_package_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM software_loadout_package WHERE software_package_id=?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM software_package WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("software package %d: %w", id, ErrNotFound)
	}
	return tx.Commit()
}

// RefCount counts direct image links and loadout memberships referencing
// this package.
func (r *SoftwarePackageRepo) RefCount(ctx context.Context, id ID) (int, error) {
	var images, loadouts int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image_software_package WHERE software_package_id=?`,
		id).Scan(&images); err != nil {
		return 0, err
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM software_loadout_package WHERE software_package_id=?`,
		id).Scan(&loadouts); err != nil {
		return 0, err
	}
	return images + loadouts, nil
}

// ResolveOrder expands seed package IDs into the full ordered install
// list: every seed plus its transitive dependencies, with each package's
// dependencies placed BEFORE it (stable topological order, seed order
// otherwise preserved). Cycles and missing packages are skipped with a
// warning rather than failing the whole resolve. The result is deduped.
func (r *SoftwarePackageRepo) ResolveOrder(ctx context.Context, seeds []ID) ([]ID, []string) {
	var (
		order    []ID
		warnings []string
		state    = map[ID]int8{} // 0=unseen, 1=visiting, 2=done
		visit    func(id ID)
	)
	visit = func(id ID) {
		switch state[id] {
		case 2:
			return
		case 1:
			warnings = append(warnings, fmt.Sprintf("software package %d: dependency cycle broken", id))
			return
		}
		state[id] = 1
		pkg, err := r.Get(ctx, id)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("software dependency %d: %v", id, err))
			state[id] = 2
			return
		}
		for _, dep := range pkg.DependsOn {
			visit(dep)
		}
		state[id] = 2
		order = append(order, id) // post-order: deps land before the dependent
	}
	for _, s := range seeds {
		visit(s)
	}
	return order, warnings
}

// SoftwareCompliance is per-package fleet compliance stats.
type SoftwareCompliance struct {
	TargetCount    int
	InstalledCount int
	MissingCount   int
	UnknownCount   int
}

// AllComplianceSummaries returns compliance stats for every software package
// in a single batch. "Target" machines are those bound to images that include
// the package (directly or via loadout). Uses machine_detected_state for
// installed/missing status.
func (r *SoftwarePackageRepo) AllComplianceSummaries(ctx context.Context) (map[ID]SoftwareCompliance, error) {
	directRows, err := r.db.QueryContext(ctx, `
		SELECT isp.software_package_id, mb.machine_id
		FROM image_software_package isp
		JOIN machine_binding mb ON mb.image_id = isp.image_id`)
	if err != nil {
		return nil, err
	}
	defer directRows.Close()
	targets := map[ID]map[ID]bool{}
	for directRows.Next() {
		var pkgID, machID ID
		if err := directRows.Scan(&pkgID, &machID); err != nil {
			return nil, err
		}
		if targets[pkgID] == nil {
			targets[pkgID] = map[ID]bool{}
		}
		targets[pkgID][machID] = true
	}
	if err := directRows.Err(); err != nil {
		return nil, err
	}

	loadoutRows, err := r.db.QueryContext(ctx, `
		SELECT slp.software_package_id, mb.machine_id
		FROM software_loadout_package slp
		JOIN image i ON i.loadout_id = slp.loadout_id
		JOIN machine_binding mb ON mb.image_id = i.id
		WHERE slp.opt_out = 0`)
	if err != nil {
		return nil, err
	}
	defer loadoutRows.Close()
	for loadoutRows.Next() {
		var pkgID, machID ID
		if err := loadoutRows.Scan(&pkgID, &machID); err != nil {
			return nil, err
		}
		if targets[pkgID] == nil {
			targets[pkgID] = map[ID]bool{}
		}
		targets[pkgID][machID] = true
	}
	if err := loadoutRows.Err(); err != nil {
		return nil, err
	}

	detRows, err := r.db.QueryContext(ctx, `
		SELECT machine_id, software_package_id, detected
		FROM machine_detected_state`)
	if err != nil {
		return nil, err
	}
	defer detRows.Close()
	detected := map[ID]map[ID]bool{}
	for detRows.Next() {
		var machID, pkgID ID
		var d int
		if err := detRows.Scan(&machID, &pkgID, &d); err != nil {
			return nil, err
		}
		if detected[pkgID] == nil {
			detected[pkgID] = map[ID]bool{}
		}
		detected[pkgID][machID] = d != 0
	}
	if err := detRows.Err(); err != nil {
		return nil, err
	}

	out := make(map[ID]SoftwareCompliance, len(targets))
	for pkgID, machSet := range targets {
		c := SoftwareCompliance{TargetCount: len(machSet)}
		for machID := range machSet {
			if det, ok := detected[pkgID][machID]; ok {
				if det {
					c.InstalledCount++
				} else {
					c.MissingCount++
				}
			} else {
				c.UnknownCount++
			}
		}
		out[pkgID] = c
	}
	return out, nil
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
	if _, err := swspec.ParseDetection(in.DetectionJSON); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if _, err := swspec.ParseSteps(in.StepsJSON); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}
