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
		"bitlocker_pin", "bitlocker_recovery_key",
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
