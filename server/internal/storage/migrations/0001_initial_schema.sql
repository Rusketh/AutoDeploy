-- 0001_initial_schema.sql
--
-- Phase 1 schema. Establishes the standalone artifact objects (ISO, Unattend,
-- DriverPackage with filters, SoftwarePackage with detection rules and
-- ordered install steps) and the Image composition object with arbitrary-
-- depth inheritance.
--
-- Notes on shape:
--   - Settings and rules that are still being designed (unattend settings in
--     Phase 5, driver filter expression in Phase 4, software detection rules
--     and install steps in Phase 6) are stored as JSON columns so the schema
--     does not move every time those shapes change. Migrations may promote
--     individual fields to columns later if querying needs it.
--   - parent_id on image is ON DELETE RESTRICT: deleting an inherited image
--     while children still reference it must be explicitly resolved.
--   - storage_path columns hold paths relative to AUTODEPLOY_DATA_DIR; the
--     server combines them with the configured data root when serving files.

-- -----------------------------------------------------------------------------
-- ISO
-- -----------------------------------------------------------------------------
CREATE TABLE iso (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL UNIQUE,
    description   TEXT    NOT NULL DEFAULT '',
    os_type       TEXT    NOT NULL,
    storage_path  TEXT    NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- -----------------------------------------------------------------------------
-- Unattend
-- -----------------------------------------------------------------------------
CREATE TABLE unattend (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL UNIQUE,
    description   TEXT    NOT NULL DEFAULT '',
    -- JSON document of the operator's configured settings; the generator in
    -- Phase 5 turns this into a complete unattend.xml.
    settings_json TEXT    NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- -----------------------------------------------------------------------------
-- DriverPackage with one-to-many SMBIOS filters
-- -----------------------------------------------------------------------------
CREATE TABLE driver_package (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL UNIQUE,
    description  TEXT    NOT NULL DEFAULT '',
    storage_path TEXT    NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- A driver package may carry several filters; the matcher applies the
-- package if ANY filter matches reported hardware. The filter shape is
-- captured as JSON for now; structured matching arrives in Phase 4.
CREATE TABLE driver_filter (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    driver_package_id INTEGER NOT NULL REFERENCES driver_package(id) ON DELETE CASCADE,
    -- Example JSON shape, subject to refinement in Phase 4:
    --   {"system_manufacturer": "Dell Inc.", "system_product": "Latitude 5520"}
    -- Empty object matches nothing; matching everything is intentionally not
    -- expressible without an explicit "*" decision in Phase 4.
    filter_json       TEXT    NOT NULL DEFAULT '{}',
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_driver_filter_pkg ON driver_filter(driver_package_id);

-- -----------------------------------------------------------------------------
-- SoftwarePackage with detection and steps stored as JSON
-- -----------------------------------------------------------------------------
CREATE TABLE software_package (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT    NOT NULL UNIQUE,
    description    TEXT    NOT NULL DEFAULT '',
    storage_path   TEXT    NOT NULL DEFAULT '',
    size_bytes     INTEGER NOT NULL DEFAULT 0,
    -- detection_json: ordered list of detection rules; the package is treated
    -- as already installed when ALL rules report present (so detection is
    -- conservative). Rule shapes land in Phase 6.
    detection_json TEXT    NOT NULL DEFAULT '[]',
    -- steps_json: ordered list of typed install steps. Each step has its own
    -- success-code set and a continue-or-abort-on-failure flag. Shapes land
    -- in Phase 6.
    steps_json     TEXT    NOT NULL DEFAULT '[]',
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- -----------------------------------------------------------------------------
-- Image: a composition object with optional parent (arbitrary-depth chain)
-- -----------------------------------------------------------------------------
CREATE TABLE image (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL UNIQUE,
    description  TEXT    NOT NULL DEFAULT '',
    -- Parent for inheritance. Cycle prevention is enforced at save time in
    -- application code; SQLite cannot express the constraint declaratively.
    parent_id    INTEGER REFERENCES image(id) ON DELETE RESTRICT,
    -- Direct links. Resolution rules in Phase 2 walk parent chain:
    --   ISO and unattend are nearest-wins, used in full.
    iso_id       INTEGER REFERENCES iso(id) ON DELETE RESTRICT,
    unattend_id  INTEGER REFERENCES unattend(id) ON DELETE RESTRICT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_image_parent ON image(parent_id);

-- Direct image -> software package links. Software loadouts land in Phase 7;
-- in Phase 1 an image may reference packages directly. Resolution unions
-- this with loadout-resolved packages (Phase 7) and de-duplicates by
-- package id.
CREATE TABLE image_software_package (
    image_id            INTEGER NOT NULL REFERENCES image(id) ON DELETE CASCADE,
    software_package_id INTEGER NOT NULL REFERENCES software_package(id) ON DELETE RESTRICT,
    order_value         INTEGER NOT NULL DEFAULT 100,
    PRIMARY KEY (image_id, software_package_id)
);
