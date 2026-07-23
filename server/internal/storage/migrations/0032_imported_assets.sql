-- 0032_imported_assets.sql
--
-- Imported assets: a staging table of machines an operator pre-registered from
-- their asset-management system (a CSV of serial / model / desired name / OU)
-- BEFORE those machines are imaged or their agents enroll.
--
-- When AutoDeploy first sees a machine (the Boot Client imaging it, or an
-- unknown agent enrolling) it matches the machine's reported SMBIOS
-- serial + product ("model") against this table. On a hit it applies the
-- imported name/OU to the machine's binding and records the match here.
--
-- The table is self-cleaning: rows that have matched, or whose serial+model is
-- already present in inventory, are swept out by the retention scheduler. An
-- operator can also wipe the whole table from the Import page.

CREATE TABLE imported_asset (
    id                 INTEGER  PRIMARY KEY AUTOINCREMENT,
    serial             TEXT     NOT NULL,
    model              TEXT     NOT NULL DEFAULT '',
    name               TEXT     NOT NULL DEFAULT '',
    ou                 TEXT     NOT NULL DEFAULT '',
    -- overwrite is the apply mode chosen for this row at import time:
    --   0 = fill only if the machine has no name/OU yet (never clobber),
    --   1 = always overwrite the machine's name/OU on first contact.
    overwrite          INTEGER  NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- matched_at / matched_machine_id are set when the row was applied to a
    -- machine. Deleting the machine nulls the reference (the row is swept by
    -- matched_at regardless).
    matched_at         DATETIME,
    matched_machine_id INTEGER REFERENCES machine_record(id) ON DELETE SET NULL
);

-- The match key is (serial, model): a re-import of the same pair updates the
-- stored name/OU/apply-mode in place rather than inserting a duplicate.
CREATE UNIQUE INDEX idx_imported_asset_serial_model ON imported_asset(serial, model);
