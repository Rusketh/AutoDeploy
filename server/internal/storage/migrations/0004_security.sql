-- 0004_security.sql
--
-- Phase 11. Adds:
--   system_setting     - typed key/value system settings (access PIN config,
--                        future-extensible).
--   user_account       - local portal accounts (bcrypt-hashed passwords).
--   user_session       - cookie-backed portal sessions.
--   pin_attempt        - rate-limit log for the deployment access PIN.

CREATE TABLE system_setting (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_account (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_session (
    token      TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);
CREATE INDEX idx_user_session_user ON user_session(user_id);

CREATE TABLE pin_attempt (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    system_uuid TEXT NOT NULL,
    attempted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    success     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_pin_attempt_uuid_time ON pin_attempt(system_uuid, attempted_at);
