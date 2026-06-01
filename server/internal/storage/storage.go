// Package storage owns the server's relational persistence. Phase 1 uses
// SQLite via the pure-Go modernc.org/sqlite driver so dev and CI need no
// external services. The on-disk schema is the API: it is migrated forward
// in Open() and not edited by hand.
//
// The storage layer is deliberately thin. Higher layers (internal/model) own
// the domain types and the resolution rules; storage just speaks SQL and
// returns rows.
package storage

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// DB wraps *sql.DB so callers do not import database/sql directly and so we
// can add helpers (transactions, audit hooks) in one place.
type DB struct {
	*sql.DB
}

// Open opens (or creates) the SQLite database at path and runs any
// outstanding migrations. The special path ":memory:" works for tests.
func Open(ctx context.Context, path string) (*DB, error) {
	// SQLite driver: enable foreign keys, WAL, busy timeout. Each connection
	// must run these PRAGMAs; the connection string applies them.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)"
	if path == ":memory:" {
		// In-memory DB cannot do WAL.
		dsn = path + "?_pragma=foreign_keys(1)"
	}
	if path != ":memory:" {
		if err := ensureParentDir(path); err != nil {
			return nil, err
		}
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	db := &DB{DB: sqlDB}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	return db, nil
}

func ensureParentDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return fsMkdirAll(dir)
}

// migrate applies any SQL files under migrations/ in lexicographic order
// that have not been recorded as applied. Each migration runs in its own
// transaction.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `
	CREATE TABLE IF NOT EXISTS schema_migration (
		name TEXT PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return fmt.Errorf("create schema_migration: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		var exists int
		row := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migration WHERE name=?`, name)
		if err := row.Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists > 0 {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migration(name) VALUES (?)`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

// ErrNotFound is returned when a requested row does not exist. Callers can
// use errors.Is(err, ErrNotFound) to translate to an HTTP 404.
var ErrNotFound = errors.New("not found")

// ErrConflict indicates a uniqueness or referential-integrity violation that
// the caller may want to surface as 409.
var ErrConflict = errors.New("conflict")

// ErrInUse indicates an object cannot be deleted because other objects
// reference it. Surface as 409 with the reference count.
var ErrInUse = errors.New("in use")
