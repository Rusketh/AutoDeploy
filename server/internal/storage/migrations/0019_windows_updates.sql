-- Windows Updates: KB patches / .msu files as a first-class entity,
-- separate from software packages, with fleet-wide targeting and
-- compliance tracking.

CREATE TABLE windows_update (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kb_number        TEXT    NOT NULL,
    title            TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    os_filter        TEXT    NOT NULL DEFAULT '',
    severity         TEXT    NOT NULL DEFAULT '',
    storage_path     TEXT    NOT NULL DEFAULT '',
    payload_filename TEXT    NOT NULL DEFAULT '',
    size_bytes       INTEGER NOT NULL DEFAULT 0,
    supersedes_json  TEXT    NOT NULL DEFAULT '[]',
    reboot_after     INTEGER NOT NULL DEFAULT 0,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX idx_windows_update_kb ON windows_update(kb_number);

CREATE TABLE machine_update_status (
    machine_id   INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    kb_number    TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'unknown',
    installed_at DATETIME,
    reported_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (machine_id, kb_number)
);
CREATE INDEX idx_mus_kb ON machine_update_status(kb_number);

CREATE TABLE update_deployment (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    update_ids_json TEXT    NOT NULL,
    target_json     TEXT    NOT NULL,
    os_filter       TEXT    NOT NULL DEFAULT '',
    created_by      TEXT    NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE update_deployment_job (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id   INTEGER NOT NULL REFERENCES update_deployment(id) ON DELETE CASCADE,
    machine_id      INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    update_id       INTEGER NOT NULL REFERENCES windows_update(id) ON DELETE CASCADE,
    status          TEXT    NOT NULL DEFAULT 'queued',
    result_json     TEXT    NOT NULL DEFAULT '{}',
    queued_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    claimed_at      DATETIME,
    completed_at    DATETIME
);
CREATE INDEX idx_udj_machine_status ON update_deployment_job(machine_id, status);
