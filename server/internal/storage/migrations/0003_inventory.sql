-- 0003_inventory.sql
--
-- Phase 8. Adds the inventory tables:
--   machine_record         - every machine AutoDeploy has seen, keyed on
--                            SMBIOS UUID (with serial as a secondary).
--   machine_binding        - the assigned image + name + AD placement.
--                            One-to-one with machine_record (a machine has
--                            exactly one current binding).
--   deployment_history     - append-only dated record per deployment.
--   machine_detected_state - last-known result of detection-rule
--                            evaluation per software package, for drift.
--
-- Re-imaging (Phase 9) uses the binding to resolve the current
-- configuration. The history is a log for audit; it is NOT a replay
-- manifest — the design is explicit that re-imaging targets the LATEST
-- definition, not a frozen snapshot.

CREATE TABLE machine_record (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    system_uuid         TEXT    NOT NULL UNIQUE,
    system_serial       TEXT    NOT NULL DEFAULT '',
    system_manufacturer TEXT    NOT NULL DEFAULT '',
    system_product      TEXT    NOT NULL DEFAULT '',
    bios_vendor         TEXT    NOT NULL DEFAULT '',
    bios_version        TEXT    NOT NULL DEFAULT '',
    board_manufacturer  TEXT    NOT NULL DEFAULT '',
    board_product       TEXT    NOT NULL DEFAULT '',
    board_serial        TEXT    NOT NULL DEFAULT '',
    first_seen          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_machine_record_serial ON machine_record(system_serial);

CREATE TABLE machine_binding (
    machine_id        INTEGER PRIMARY KEY REFERENCES machine_record(id) ON DELETE CASCADE,
    -- ON DELETE SET NULL so deleting an image breaks the binding rather
    -- than the machine record. The portal warns operators to re-bind
    -- before deleting an image that machines depend on.
    image_id          INTEGER REFERENCES image(id) ON DELETE SET NULL,
    machine_name      TEXT    NOT NULL DEFAULT '',
    target_ou         TEXT    NOT NULL DEFAULT '',
    group_memberships TEXT    NOT NULL DEFAULT '[]',
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE deployment_history (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id   INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    image_id     INTEGER REFERENCES image(id),
    started_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    outcome      TEXT    NOT NULL DEFAULT 'in_progress',
    notes        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_deployment_history_machine ON deployment_history(machine_id);

CREATE TABLE machine_detected_state (
    machine_id          INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    software_package_id INTEGER NOT NULL REFERENCES software_package(id) ON DELETE CASCADE,
    detected            INTEGER NOT NULL,
    last_evaluated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (machine_id, software_package_id)
);
