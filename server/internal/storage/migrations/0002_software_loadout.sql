-- 0002_software_loadout.sql
--
-- Phase 7. Adds SoftwareLoadout: a named, ordered, inheritable collection
-- of software packages. Inheritance is additive (child = parent's packages
-- plus its own); a child can OPT OUT of an inherited package, and ordering
-- is by explicit per-package order_value rather than by chain depth.
--
-- Image gains an optional loadout_id link. The resolver's effective
-- software set is the loadout (resolved up its own chain, opt-outs honoured)
-- UNION any directly linked image_software_package rows, de-duplicated by
-- package id with direct-link ordering taking precedence.

CREATE TABLE software_loadout (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    parent_id   INTEGER REFERENCES software_loadout(id) ON DELETE RESTRICT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_software_loadout_parent ON software_loadout(parent_id);

CREATE TABLE software_loadout_package (
    loadout_id          INTEGER NOT NULL REFERENCES software_loadout(id) ON DELETE CASCADE,
    software_package_id INTEGER NOT NULL REFERENCES software_package(id) ON DELETE RESTRICT,
    order_value         INTEGER NOT NULL DEFAULT 100,
    opt_out             INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (loadout_id, software_package_id)
);

ALTER TABLE image ADD COLUMN loadout_id INTEGER REFERENCES software_loadout(id) ON DELETE RESTRICT;
