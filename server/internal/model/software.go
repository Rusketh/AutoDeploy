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
	if err := r.validateSucceeds(ctx, in.ID, in.SucceedsID); err != nil {
		return SoftwarePackage{}, err
	}
	res, err := r.db.ExecContext(ctx, `
		INSERT INTO software_package
		    (name, description, storage_path, payload_filename,
		     size_bytes, detection_json, steps_json, depends_on_json, succeeds_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Description, in.StoragePath, in.PayloadFilename,
		in.SizeBytes, in.DetectionJSON, in.StepsJSON, marshalDeps(in.DependsOn), nullID(in.SucceedsID))
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
	var succeeds sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, storage_path, payload_filename,
		       size_bytes, detection_json, steps_json, depends_on_json, succeeds_id,
		       created_at, updated_at
		FROM software_package WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &v.StoragePath, &v.PayloadFilename,
		&v.SizeBytes, &v.DetectionJSON, &v.StepsJSON, &deps, &succeeds,
		&v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SoftwarePackage{}, fmt.Errorf("software package %d: %w", id, ErrNotFound)
	}
	v.DependsOn = unmarshalDeps(deps)
	v.SucceedsID = idPtr(succeeds)
	return v, err
}

func (r *SoftwarePackageRepo) List(ctx context.Context) ([]SoftwarePackage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, storage_path, payload_filename,
		       size_bytes, detection_json, steps_json, depends_on_json, succeeds_id,
		       created_at, updated_at
		FROM software_package ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoftwarePackage
	for rows.Next() {
		var v SoftwarePackage
		var deps string
		var succeeds sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.StoragePath,
			&v.PayloadFilename, &v.SizeBytes, &v.DetectionJSON, &v.StepsJSON,
			&deps, &succeeds, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.DependsOn = unmarshalDeps(deps)
		v.SucceedsID = idPtr(succeeds)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *SoftwarePackageRepo) Update(ctx context.Context, in SoftwarePackage) error {
	if err := validateSoftware(&in); err != nil {
		return err
	}
	if err := r.validateSucceeds(ctx, in.ID, in.SucceedsID); err != nil {
		return err
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE software_package
		SET name=?, description=?, storage_path=?, payload_filename=?,
		    size_bytes=?, detection_json=?, steps_json=?, depends_on_json=?,
		    succeeds_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description, in.StoragePath, in.PayloadFilename,
		in.SizeBytes, in.DetectionJSON, in.StepsJSON, marshalDeps(in.DependsOn),
		nullID(in.SucceedsID), in.ID)
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
//
// A final supersedence pass then drops any package that is succeeded
// (superseded) by another package present in the resolved set, so only the
// newest survivor of each supersedence chain installs. See applySupersedence.
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
	return r.applySupersedence(ctx, order, warnings)
}

// applySupersedence drops every package in order that is superseded by another
// package also in order. "A succeeds B" (A.SucceedsID == B) means B is
// superseded by A: when both land in the same install set only A installs.
// Supersedence is transitive — if C succeeds B and B succeeds A, then C in the
// set supersedes A even when B itself is absent — so each package's succeeds
// chain is walked to the end, marking every predecessor present in the set.
// A corrupt self/cyclic chain is broken with a warning rather than looping.
func (r *SoftwarePackageRepo) applySupersedence(ctx context.Context, order []ID, warnings []string) ([]ID, []string) {
	if len(order) == 0 {
		return order, warnings
	}
	inSet := make(map[ID]bool, len(order))
	for _, id := range order {
		inSet[id] = true
	}
	// predCache memoises each package's immediate predecessor (SucceedsID) so
	// walking overlapping chains stays cheap; intermediate packages on a chain
	// need not be in the install set.
	predCache := map[ID]*ID{}
	predOf := func(id ID) *ID {
		if p, ok := predCache[id]; ok {
			return p
		}
		pkg, err := r.Get(ctx, id)
		if err != nil {
			predCache[id] = nil
			return nil
		}
		predCache[id] = pkg.SucceedsID
		return pkg.SucceedsID
	}

	superseded := map[ID]bool{}
	for _, start := range order {
		seen := map[ID]bool{start: true}
		cur := start
		for {
			pred := predOf(cur)
			if pred == nil {
				break
			}
			if seen[*pred] {
				warnings = append(warnings,
					fmt.Sprintf("software package %d: supersedence cycle broken", *pred))
				break
			}
			seen[*pred] = true
			if inSet[*pred] {
				superseded[*pred] = true
			}
			cur = *pred
		}
	}
	if len(superseded) == 0 {
		return order, warnings
	}
	filtered := make([]ID, 0, len(order))
	for _, id := range order {
		if superseded[id] {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, warnings
}

// validateSucceeds rejects a supersedence pointer that names the package
// itself or that would close a supersedence cycle (walking UP from the
// proposed predecessor must never return to id). A nil pointer is always
// valid. Existence of the referenced package is enforced by the FK.
func (r *SoftwarePackageRepo) validateSucceeds(ctx context.Context, id ID, succeedsID *ID) error {
	if succeedsID == nil {
		return nil
	}
	if *succeedsID == id {
		return fmt.Errorf("software package %d: %w (cannot succeed itself)", id, ErrValidation)
	}
	const maxDepth = 256
	cur := *succeedsID
	for depth := 0; depth < maxDepth; depth++ {
		if cur == id {
			return fmt.Errorf("software package %d: %w (supersedence cycle)", id, ErrValidation)
		}
		var next sql.NullInt64
		err := r.db.QueryRowContext(ctx,
			`SELECT succeeds_id FROM software_package WHERE id=?`, cur).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !next.Valid {
			return nil
		}
		cur = ID(next.Int64)
	}
	return fmt.Errorf("software package %d: %w (supersedence chain too deep)", id, ErrValidation)
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

	// Loadout-derived targets. Resolve each loadout up its PARENT chain
	// (opt-outs honoured) so a package inherited from an ancestor loadout
	// still counts the machines whose image uses a descendant loadout. The
	// previous query joined software_loadout_package straight to the image's
	// own loadout, so a package contributed only by a PARENT loadout reported
	// zero targets. Mirrors resolve.resolveLoadout.
	effective, err := r.effectiveLoadoutPackages(ctx)
	if err != nil {
		return nil, err
	}
	loadoutRows, err := r.db.QueryContext(ctx, `
		SELECT i.loadout_id, mb.machine_id
		FROM image i
		JOIN machine_binding mb ON mb.image_id = i.id
		WHERE i.loadout_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer loadoutRows.Close()
	for loadoutRows.Next() {
		var loadoutID, machID ID
		if err := loadoutRows.Scan(&loadoutID, &machID); err != nil {
			return nil, err
		}
		for pkgID := range effective[loadoutID] {
			if targets[pkgID] == nil {
				targets[pkgID] = map[ID]bool{}
			}
			targets[pkgID][machID] = true
		}
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

// effectiveLoadoutPackages returns, for every loadout, the set of package IDs
// that resolve onto an image using it: inherited down the parent chain
// (nearest-wins) with a descendant's opt_out removing an ancestor's package.
// Mirrors resolve.resolveLoadout so the compliance "targets" count matches
// what the resolver actually installs on a machine.
func (r *SoftwarePackageRepo) effectiveLoadoutPackages(ctx context.Context) (map[ID]map[ID]bool, error) {
	parentRows, err := r.db.QueryContext(ctx, `SELECT id, parent_id FROM software_loadout`)
	if err != nil {
		return nil, err
	}
	defer parentRows.Close()
	parent := map[ID]*ID{}
	for parentRows.Next() {
		var id ID
		var p sql.NullInt64
		if err := parentRows.Scan(&id, &p); err != nil {
			return nil, err
		}
		parent[id] = idPtr(p)
	}
	if err := parentRows.Err(); err != nil {
		return nil, err
	}

	type entry struct {
		pkg    ID
		optOut bool
	}
	entries := map[ID][]entry{}
	entRows, err := r.db.QueryContext(ctx,
		`SELECT loadout_id, software_package_id, opt_out FROM software_loadout_package`)
	if err != nil {
		return nil, err
	}
	defer entRows.Close()
	for entRows.Next() {
		var lid, pid ID
		var opt int
		if err := entRows.Scan(&lid, &pid, &opt); err != nil {
			return nil, err
		}
		entries[lid] = append(entries[lid], entry{pkg: pid, optOut: opt != 0})
	}
	if err := entRows.Err(); err != nil {
		return nil, err
	}

	out := make(map[ID]map[ID]bool, len(parent))
	for id := range parent {
		// Apply eldest ancestor first so a descendant's entry overrides it,
		// and a descendant's opt_out removes an inherited package.
		chain := loadoutAncestry(id, parent)
		merged := map[ID]bool{}
		for i := len(chain) - 1; i >= 0; i-- {
			for _, e := range entries[chain[i]] {
				if e.optOut {
					delete(merged, e.pkg)
				} else {
					merged[e.pkg] = true
				}
			}
		}
		out[id] = merged
	}
	return out, nil
}

// loadoutAncestry returns the chain [id, parent, grandparent, …] up to the
// root, stopping on a missing link or a cycle (defensive — Update forbids
// cycles, but a corrupt row must not loop the batch summary forever).
func loadoutAncestry(id ID, parent map[ID]*ID) []ID {
	const maxDepth = 256
	var chain []ID
	seen := map[ID]bool{}
	cur := id
	for depth := 0; depth < maxDepth; depth++ {
		if seen[cur] {
			break
		}
		seen[cur] = true
		chain = append(chain, cur)
		p, ok := parent[cur]
		if !ok || p == nil {
			break
		}
		cur = *p
	}
	return chain
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
