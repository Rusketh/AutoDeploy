package model

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rusketh/autodeploy/server/internal/storage"
)

// ImportedAsset is one machine an operator pre-registered from their asset
// system: a desired name/OU keyed on the machine's SMBIOS serial + model.
// It lives in the imported_asset staging table until a real machine matching
// serial+model first contacts AutoDeploy, at which point the name/OU is applied
// to that machine's binding and the row is marked matched (then swept).
type ImportedAsset struct {
	ID     ID     `json:"id"`
	Serial string `json:"serial"`
	Model  string `json:"model"`
	Name   string `json:"name"`
	OU     string `json:"ou"`
	// Overwrite selects the apply mode chosen at import time: true replaces the
	// machine's name/OU on match, false only fills fields the machine lacks.
	Overwrite bool      `json:"overwrite"`
	CreatedAt time.Time `json:"created_at"`
	// MatchedAt / MatchedMachineID are set once the row has been applied to a
	// machine (nil until then).
	MatchedAt        *time.Time `json:"matched_at,omitempty"`
	MatchedMachineID *ID        `json:"matched_machine_id,omitempty"`
}

// Matched reports whether this asset has already been applied to a machine.
func (a ImportedAsset) Matched() bool { return a.MatchedAt != nil }

// ImportedAssetRepo owns the imported_asset staging table.
type ImportedAssetRepo struct {
	db *storage.DB
}

// NewImportedAssetRepo constructs the repo.
func NewImportedAssetRepo(db *storage.DB) *ImportedAssetRepo {
	return &ImportedAssetRepo{db: db}
}

// csvHeaderAliases maps recognised header cell text to a canonical column name.
// A header row is optional; when the first row's cells are all recognised names
// it is treated as a header and consumed, otherwise the file is read in the
// fixed order serial, model, name, ou.
var csvHeaderAliases = map[string]string{
	"serial":              "serial",
	"serial_number":       "serial",
	"serialnumber":        "serial",
	"service_tag":         "serial",
	"servicetag":          "serial",
	"model":               "model",
	"product":             "model",
	"name":                "name",
	"hostname":            "name",
	"computer_name":       "name",
	"computername":        "name",
	"ou":                  "ou",
	"org_unit":            "ou",
	"organizational_unit": "ou",
}

// ParseCSV reads assets from a CSV. It accepts an optional header row naming the
// columns (serial, model, name, ou — in any order, plus a few common aliases);
// without a header the fixed order serial, model, name, ou is assumed. Rows are
// trimmed; a row missing serial OR model is skipped and reported. The overwrite
// apply mode is not carried in the CSV — the caller sets it on the returned rows
// from the import form. Returns the valid assets and a list of human-readable
// skip reasons.
func ParseCSV(rd io.Reader) ([]ImportedAsset, []string, error) {
	cr := csv.NewReader(rd)
	cr.FieldsPerRecord = -1 // tolerate ragged rows; we index defensively
	cr.TrimLeadingSpace = true

	records, err := cr.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: could not parse CSV: %v", ErrValidation, err)
	}
	if len(records) == 0 {
		return nil, nil, nil
	}

	// Default column order; overridden if the first row is a header.
	cols := map[string]int{"serial": 0, "model": 1, "name": 2, "ou": 3}
	start := 0
	if hdr, ok := parseHeader(records[0]); ok {
		cols = hdr
		start = 1
	}

	cell := func(row []string, key string) string {
		i, ok := cols[key]
		if !ok || i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	var out []ImportedAsset
	var skipped []string
	for n := start; n < len(records); n++ {
		row := records[n]
		if isBlankRow(row) {
			continue
		}
		serial := cell(row, "serial")
		model := cell(row, "model")
		if serial == "" || model == "" {
			skipped = append(skipped, fmt.Sprintf("row %d: serial and model are both required", n+1))
			continue
		}
		out = append(out, ImportedAsset{
			Serial: serial,
			Model:  model,
			Name:   cell(row, "name"),
			OU:     cell(row, "ou"),
		})
	}
	return out, skipped, nil
}

// parseHeader returns a column-index map when every non-empty cell of row is a
// recognised header name and it includes at least the serial column. Otherwise
// (false) the row is data, not a header.
func parseHeader(row []string) (map[string]int, bool) {
	cols := map[string]int{}
	for i, raw := range row {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		canon, ok := csvHeaderAliases[key]
		if !ok {
			return nil, false // an unrecognised cell -> this is data
		}
		if _, dup := cols[canon]; dup {
			return nil, false // duplicate column -> treat as data, use fixed order
		}
		cols[canon] = i
	}
	if _, ok := cols["serial"]; !ok {
		return nil, false
	}
	return cols, true
}

func isBlankRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// Upsert writes assets, keyed on (serial, model). An existing pair has its
// name/OU/apply-mode replaced; a new pair is inserted. Returns how many rows
// were inserted vs updated. A re-matched pair is reset to unmatched so a fresh
// import re-applies it.
func (r *ImportedAssetRepo) Upsert(ctx context.Context, assets []ImportedAsset) (inserted, updated int, err error) {
	if len(assets) == 0 {
		return 0, 0, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, a := range assets {
		serial := strings.TrimSpace(a.Serial)
		model := strings.TrimSpace(a.Model)
		if serial == "" || model == "" {
			return inserted, updated, fmt.Errorf("%w: serial and model required", ErrValidation)
		}
		var existing int
		err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM imported_asset WHERE serial=? AND model=?`,
			serial, model).Scan(&existing)
		if err != nil {
			return inserted, updated, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO imported_asset (serial, model, name, ou, overwrite)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(serial, model) DO UPDATE SET
			    name=excluded.name,
			    ou=excluded.ou,
			    overwrite=excluded.overwrite,
			    matched_at=NULL,
			    matched_machine_id=NULL`,
			serial, model, strings.TrimSpace(a.Name), strings.TrimSpace(a.OU), boolToInt(a.Overwrite))
		if err != nil {
			return inserted, updated, err
		}
		if existing > 0 {
			updated++
		} else {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return inserted, updated, nil
}

// List returns all imported assets, newest first.
func (r *ImportedAssetRepo) List(ctx context.Context) ([]ImportedAsset, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, serial, model, name, ou, overwrite, created_at, matched_at, matched_machine_id
		FROM imported_asset
		ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ImportedAsset
	for rows.Next() {
		a, err := scanImportedAsset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Count returns the number of imported assets.
func (r *ImportedAssetRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM imported_asset`).Scan(&n)
	return n, err
}

// WipeAll deletes every imported asset and returns the number removed. This is
// the operator-triggered "wipe the imported device table" action.
func (r *ImportedAssetRepo) WipeAll(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM imported_asset`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MatchByIdentity finds the unmatched imported asset whose serial+model equal
// the given values (case-insensitive, trimmed). Returns ErrNotFound when serial
// or model is empty or no unmatched row matches.
func (r *ImportedAssetRepo) MatchByIdentity(ctx context.Context, serial, model string) (ImportedAsset, error) {
	serial = strings.TrimSpace(serial)
	model = strings.TrimSpace(model)
	if serial == "" || model == "" {
		return ImportedAsset{}, ErrNotFound
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, serial, model, name, ou, overwrite, created_at, matched_at, matched_machine_id
		FROM imported_asset
		WHERE matched_at IS NULL
		  AND lower(serial)=lower(?) AND lower(model)=lower(?)
		ORDER BY id ASC
		LIMIT 1`, serial, model)
	a, err := scanImportedAsset(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportedAsset{}, ErrNotFound
	}
	return a, err
}

// MarkMatched records that an asset was applied to a machine.
func (r *ImportedAssetRepo) MarkMatched(ctx context.Context, id, machineID ID) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE imported_asset
		SET matched_at=CURRENT_TIMESTAMP, matched_machine_id=?
		WHERE id=?`, int64(machineID), int64(id))
	return err
}

// SweepMatched removes rows that are self-evidently done with: any that have
// already matched a machine, plus any whose serial+model is already present in
// inventory (a "known" device that enrolled without an import-driven apply).
// Returns the number of rows removed. Called on the retention tick.
func (r *ImportedAssetRepo) SweepMatched(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `
		DELETE FROM imported_asset
		WHERE matched_at IS NOT NULL
		   OR EXISTS (
		        SELECT 1 FROM machine_record m
		        WHERE lower(m.system_serial)=lower(imported_asset.serial)
		          AND lower(m.system_product)=lower(imported_asset.model)
		   )`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// scanImportedAsset reads one row from a *sql.Row or *sql.Rows.
func scanImportedAsset(s interface{ Scan(...any) error }) (ImportedAsset, error) {
	var a ImportedAsset
	var overwrite int
	var matchedAt sql.NullTime
	var matchedMachine sql.NullInt64
	if err := s.Scan(&a.ID, &a.Serial, &a.Model, &a.Name, &a.OU,
		&overwrite, &a.CreatedAt, &matchedAt, &matchedMachine); err != nil {
		return ImportedAsset{}, err
	}
	a.Overwrite = overwrite != 0
	if matchedAt.Valid {
		t := matchedAt.Time
		a.MatchedAt = &t
	}
	a.MatchedMachineID = idPtr(matchedMachine)
	return a, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
