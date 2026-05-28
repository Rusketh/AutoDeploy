-- 0006_bulk.sql
--
-- Phase 13. Bulk operations and the resident-agent job queue.
--
--   bulk_operation - the operator's intent: action + target selection.
--   bulk_job       - one per targeted machine, claimed and completed by
--                    the agent on check-in.

CREATE TABLE bulk_operation (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    action      TEXT NOT NULL,  -- "rename" | "software_push" | "script"
    payload     TEXT NOT NULL,  -- action-specific JSON
    target_json TEXT NOT NULL,  -- {name_regex, ou, group}
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE bulk_job (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id  INTEGER NOT NULL REFERENCES bulk_operation(id) ON DELETE CASCADE,
    machine_id    INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'queued',  -- queued | running | ok | failed
    result_json   TEXT NOT NULL DEFAULT '{}',
    queued_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    claimed_at    DATETIME,
    completed_at  DATETIME
);
CREATE INDEX idx_bulk_job_machine_status ON bulk_job(machine_id, status);
