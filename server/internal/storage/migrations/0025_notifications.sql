-- Notification system: in-portal notifications, email preferences, webhooks.

-- Per-user email address for email notifications.
ALTER TABLE user_account ADD COLUMN email TEXT NOT NULL DEFAULT '';

-- In-portal notifications: one row per user per event.
CREATE TABLE notification (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    occurred_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    event       TEXT    NOT NULL,
    severity    TEXT    NOT NULL DEFAULT 'info',
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    link        TEXT    NOT NULL DEFAULT '',
    read_at     TEXT
);
CREATE INDEX idx_notification_user_unread ON notification(user_id, read_at);
CREATE INDEX idx_notification_occurred     ON notification(occurred_at);

-- Per-user notification preferences: which events, which channels.
CREATE TABLE notification_preference (
    id      INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
    event   TEXT    NOT NULL,
    portal  INTEGER NOT NULL DEFAULT 1,
    email   INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, event)
);

-- Webhook endpoints configured by operators.
CREATE TABLE webhook (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL,
    url          TEXT    NOT NULL,
    secret_enc   TEXT    NOT NULL DEFAULT '',
    headers_enc  TEXT    NOT NULL DEFAULT '{}',
    events       TEXT    NOT NULL DEFAULT '["*"]',
    min_severity TEXT    NOT NULL DEFAULT 'info',
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at   TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

-- Webhook delivery log for debugging.
CREATE TABLE webhook_delivery (
    id           INTEGER PRIMARY KEY,
    webhook_id   INTEGER NOT NULL REFERENCES webhook(id) ON DELETE CASCADE,
    event        TEXT    NOT NULL,
    payload      TEXT    NOT NULL,
    status_code  INTEGER,
    response     TEXT    NOT NULL DEFAULT '',
    error        TEXT    NOT NULL DEFAULT '',
    attempted_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    duration_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_webhook_delivery_webhook ON webhook_delivery(webhook_id, attempted_at);
