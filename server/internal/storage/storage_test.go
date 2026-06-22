package storage

import (
	"context"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	db, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// All artifact tables should exist after migrations.
	for _, table := range []string{
		"iso", "unattend", "driver_package", "driver_filter",
		"software_package", "image", "image_software_package",
		"software_loadout", "software_loadout_package",
		"machine_record", "machine_binding", "deployment_history",
		"machine_detected_state",
		"system_setting", "user_account", "user_session", "pin_attempt",
		"bulk_operation", "bulk_job",
		"log_event",
		"payload_mirror",
		"schema_migration",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}

	// The BitLocker and deploy-token tables were removed; the cleanup
	// migration drops them, so a fresh database must not have them.
	for _, table := range []string{
		"bitlocker_pin", "bitlocker_recovery_key", "machine_deploy_token",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err == nil {
			t.Errorf("table %s should have been dropped but still exists", table)
		}
	}
}

// TestDropBitlockerMigrationRemovesLegacyTables covers the upgrade path: a
// database created before BitLocker was removed still has the PIN, recovery-key
// and deploy-token tables, and the cleanup migration must drop them.
func TestDropBitlockerMigrationRemovesLegacyTables(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Simulate a legacy DB: recreate the dropped tables and forget that the
	// cleanup migration ran, so migrate() applies it again.
	for _, ddl := range []string{
		`CREATE TABLE bitlocker_pin (machine_id INTEGER PRIMARY KEY)`,
		`CREATE TABLE bitlocker_recovery_key (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE machine_deploy_token (machine_id INTEGER PRIMARY KEY)`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("seed legacy table: %v", err)
		}
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM schema_migration WHERE name='0029_drop_bitlocker.sql'`); err != nil {
		t.Fatalf("reset migration record: %v", err)
	}

	if err := db.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	for _, table := range []string{"bitlocker_pin", "bitlocker_recovery_key", "machine_deploy_token"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err == nil {
			t.Errorf("legacy table %s should have been dropped by the cleanup migration", table)
		}
	}
}

func TestMigrationIsIdempotent(t *testing.T) {
	db, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Running migrations a second time should be a no-op.
	if err := db.migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migration`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count == 0 {
		t.Error("expected at least one migration recorded")
	}
}
