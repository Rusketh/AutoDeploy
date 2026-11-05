-- 0036_toolkits.sql
--
-- Tool Kits: operator-defined, reusable payloads (files + ordered steps, the
-- same step format as a software package) that run ONCE each time they are
-- deployed — no detection rules — on a single machine or as a bulk operation.
--
--   toolkit          - the reusable definition, including its default Run As
--                      account. The Run As password is a secret: encrypted at
--                      rest with secrets.Box and released only to the agent
--                      running a claimed toolkit job (never logged).
--   bulk_credential  - a per-operation Run As password override entered at
--                      deploy time. Deleted once the operation finishes or is
--                      cancelled (and cascades with the operation).

CREATE TABLE toolkit (
    id                      INTEGER PRIMARY KEY AUTOINCREMENT,
    name                    TEXT    NOT NULL UNIQUE,
    description             TEXT    NOT NULL DEFAULT '',
    steps_json              TEXT    NOT NULL DEFAULT '[]',
    -- Named parameters ([{name,label,default,required}]) expanded as %NAME%
    -- in every step field; defaults can be overridden at deploy time.
    params_json             TEXT    NOT NULL DEFAULT '[]',
    -- system | user | logged_on_user
    run_as_mode             TEXT    NOT NULL DEFAULT 'system',
    run_as_user             TEXT    NOT NULL DEFAULT '',
    run_as_password_cipher  TEXT    NOT NULL DEFAULT '',
    -- Whole-run timeout enforced by the agent; 0 = no timeout.
    timeout_minutes         INTEGER NOT NULL DEFAULT 60,
    -- never | if_requested (a step exited 3010/1641) | always
    reboot_policy           TEXT    NOT NULL DEFAULT 'never',
    -- Bumped on every save; each job records the revision it ran.
    revision                INTEGER NOT NULL DEFAULT 1,
    created_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE bulk_credential (
    operation_id     INTEGER PRIMARY KEY REFERENCES bulk_operation(id) ON DELETE CASCADE,
    run_as_user      TEXT NOT NULL,
    password_cipher  TEXT NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_bulk_operation_action ON bulk_operation(action);
