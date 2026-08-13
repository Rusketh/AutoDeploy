package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ImageRepo is the repository for image and image_software_package rows.
type ImageRepo struct{ db *storage.DB }

func NewImageRepo(db *storage.DB) *ImageRepo { return &ImageRepo{db: db} }

// Create inserts an image and its direct software links. Rejects a parent
// pointer that would create an inheritance cycle.
func (r *ImageRepo) Create(ctx context.Context, in Image) (Image, error) {
	if err := validateName(in.Name); err != nil {
		return Image{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// We cannot have a cycle on insert because the new row has no children
	// yet; the parent_id just needs to refer to an existing image. The check
	// for a NULL row reference happens via the FK.

	res, err := tx.ExecContext(ctx, `
		INSERT INTO image (name, description, parent_id, iso_id, unattend_id, loadout_id, group_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.Description,
		nullID(in.ParentID), nullID(in.ISOID), nullID(in.UnattendID), nullID(in.LoadoutID), nullID(in.GroupID))
	if err != nil {
		if isUniqueErr(err) {
			return Image{}, fmt.Errorf("image %q: %w", in.Name, ErrConflict)
		}
		return Image{}, err
	}
	id, _ := res.LastInsertId()
	if err := upsertSoftwareLinks(ctx, tx, ID(id), in.SoftwareLinks); err != nil {
		return Image{}, err
	}
	if err := tx.Commit(); err != nil {
		return Image{}, err
	}
	return r.Get(ctx, ID(id))
}

// Update modifies an image's metadata, parent, ISO/unattend links and
// software-link set. Rejects a parent change that would create a cycle.
func (r *ImageRepo) Update(ctx context.Context, in Image) error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.ParentID != nil {
		if *in.ParentID == in.ID {
			return fmt.Errorf("image %d: %w (self parent)", in.ID, ErrCycle)
		}
		// Walking UP from the proposed parent must never reach `in.ID`.
		cycle, err := r.parentChainContains(ctx, *in.ParentID, in.ID)
		if err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("image %d: %w (parent chain returns to self)", in.ID, ErrCycle)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		UPDATE image
		SET name=?, description=?, parent_id=?, iso_id=?, unattend_id=?, loadout_id=?,
		    group_id=?, updated_at=CURRENT_TIMESTAMP
		WHERE id=?`,
		in.Name, in.Description,
		nullID(in.ParentID), nullID(in.ISOID), nullID(in.UnattendID), nullID(in.LoadoutID),
		nullID(in.GroupID), in.ID)
	if err != nil {
		if isUniqueErr(err) {
			return fmt.Errorf("image %q: %w", in.Name, ErrConflict)
		}
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image %d: %w", in.ID, ErrNotFound)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM image_software_package WHERE image_id=?`, in.ID); err != nil {
		return err
	}
	if err := upsertSoftwareLinks(ctx, tx, in.ID, in.SoftwareLinks); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the image with id, including its software links.
func (r *ImageRepo) Get(ctx context.Context, id ID) (Image, error) {
	var v Image
	var parent, iso, unattend, loadout sql.NullInt64
	var group sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, parent_id, iso_id, unattend_id, loadout_id,
		       group_id, created_at, updated_at
		FROM image WHERE id=?`, id).Scan(
		&v.ID, &v.Name, &v.Description, &parent, &iso, &unattend, &loadout,
		&group, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, fmt.Errorf("image %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Image{}, err
	}
	v.ParentID = idPtr(parent)
	v.ISOID = idPtr(iso)
	v.UnattendID = idPtr(unattend)
	v.LoadoutID = idPtr(loadout)
	v.GroupID = idPtr(group)
	links, err := r.linksFor(ctx, id)
	if err != nil {
		return Image{}, err
	}
	v.SoftwareLinks = links
	return v, nil
}

// List returns all images ordered by name. Software links are populated.
func (r *ImageRepo) List(ctx context.Context) ([]Image, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, parent_id, iso_id, unattend_id, loadout_id,
		       group_id, created_at, updated_at
		FROM image ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		var v Image
		var parent, iso, unattend, loadout, group sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &parent, &iso, &unattend, &loadout,
			&group, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.ParentID = idPtr(parent)
		v.ISOID = idPtr(iso)
		v.UnattendID = idPtr(unattend)
		v.LoadoutID = idPtr(loadout)
		v.GroupID = idPtr(group)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		links, err := r.linksFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].SoftwareLinks = links
	}
	return out, nil
}

func (r *ImageRepo) linksFor(ctx context.Context, id ID) ([]ImageSoftwareLink, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT software_package_id, order_value
		FROM image_software_package
		WHERE image_id=?
		ORDER BY order_value, software_package_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImageSoftwareLink
	for rows.Next() {
		var l ImageSoftwareLink
		if err := rows.Scan(&l.PackageID, &l.OrderValue); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListNamesByIDs returns a map of image ID to image name for the given IDs.
// Missing IDs are silently omitted. This is a lightweight batch lookup used
// by the portal machine list to avoid N+1 per-machine image loads.
func (r *ImageRepo) ListNamesByIDs(ctx context.Context, ids []ID) (map[ID]string, error) {
	if len(ids) == 0 {
		return map[ID]string{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name FROM image WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ID]string, len(ids))
	for rows.Next() {
		var id ID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// Delete removes an image. Refuses if any other image points at it via
// parent_id (ON DELETE RESTRICT enforces this at the DB level too).
func (r *ImageRepo) Delete(ctx context.Context, id ID) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var refs int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE parent_id=?`, id).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("image %d: %w (parent of %d images)", id, ErrInUse, refs)
	}
	// Audit-log tables reference image(id) but must not pin the image in place:
	// history targets the LATEST image definition, and machine_binding already
	// nulls its image_id on delete. deployment_history and reimage_event have no
	// ON DELETE clause (implicit RESTRICT), which would otherwise make any image
	// that has ever been deployed or re-imaged undeletable. Null those references
	// first so the row can go while the audit rows survive.
	if _, err := tx.ExecContext(ctx,
		`UPDATE deployment_history SET image_id=NULL WHERE image_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE reimage_event SET image_id=NULL WHERE image_id=?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM image WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("image %d: %w", id, ErrNotFound)
	}
	return tx.Commit()
}

// ChildCount returns the number of images that name this one as parent.
func (r *ImageRepo) ChildCount(ctx context.Context, id ID) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM image WHERE parent_id=?`, id).Scan(&n)
	return n, err
}

// parentChainContains walks parent pointers from start, returning true if it
// reaches target. Guards against pathological cycles in already-stored data
// by capping the walk depth.
func (r *ImageRepo) parentChainContains(ctx context.Context, start, target ID) (bool, error) {
	const maxDepth = 256
	cur := start
	for depth := 0; depth < maxDepth; depth++ {
		if cur == target {
			return true, nil
		}
		var next sql.NullInt64
		err := r.db.QueryRowContext(ctx,
			`SELECT parent_id FROM image WHERE id=?`, cur).Scan(&next)
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
	return false, fmt.Errorf("parent chain depth exceeded %d (data may already have a cycle)", maxDepth)
}

// Clone creates a copy of the image with the given name, preserving all
// composition fields (parent, ISO, unattend, loadout) and direct software links.
func (r *ImageRepo) Clone(ctx context.Context, id ID, newName string) (Image, error) {
	src, err := r.Get(ctx, id)
	if err != nil {
		return Image{}, err
	}
	src.ID = 0
	src.Name = newName
	return r.Create(ctx, src)
}

func upsertSoftwareLinks(ctx context.Context, tx *sql.Tx, imageID ID, links []ImageSoftwareLink) error {
	for _, l := range links {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO image_software_package (image_id, software_package_id, order_value)
			VALUES (?, ?, ?)`,
			imageID, l.PackageID, l.OrderValue); err != nil {
			return err
		}
	}
	return nil
}

func nullID(p *ID) any {
	if p == nil {
		return nil
	}
	return int64(*p)
}

func idPtr(n sql.NullInt64) *ID {
	if !n.Valid {
		return nil
	}
	v := ID(n.Int64)
	return &v
}
