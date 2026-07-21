-- Scripts & Tasks: reusable, operator-authored script/task definitions that a
-- sequence step can reference. Each task carries a PAYLOAD (a powershell or cmd
-- body — the thing it does on the target) and an optional APPLICABILITY FILTER
-- that gates whether the step runs on a given machine:
--   filter_type = ''    → always runs
--               = 'os'   → filter_value is an OS-caption substring (case-
--                          insensitive), matched like an install step's FilterOS
--               = 'wmic' → filter_value is a WMI query; the step runs only if it
--                          returns at least one row
--               = 'ps1'  → filter_value is a PowerShell snippet; the step runs
--                          only if it exits 0
-- A step whose filter does not match is SKIPPED (not failed), mirroring the
-- existing per-install-step FilterOS behaviour.
CREATE TABLE task (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    name                TEXT     NOT NULL UNIQUE,
    description         TEXT     NOT NULL DEFAULT '',
    payload_shell       TEXT     NOT NULL DEFAULT 'powershell', -- powershell|cmd
    payload_body        TEXT     NOT NULL DEFAULT '',
    filter_type         TEXT     NOT NULL DEFAULT '',           -- ''|os|wmic|ps1
    filter_value        TEXT     NOT NULL DEFAULT '',
    continue_on_failure INTEGER  NOT NULL DEFAULT 0,
    success_codes_json  TEXT     NOT NULL DEFAULT '[0]',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
