-- 0007_logs.sql
--
-- Phase 14. Centralised log collection. Per-component logging was wired
-- in from Phase 0 (each component emits structured JSON with actor,
-- action, component, target, when). Phase 14 adds:
--
--   log_event  - one row per event ingested from any component or
--                produced by the server itself.

CREATE TABLE log_event (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    component  TEXT NOT NULL,
    level      TEXT NOT NULL DEFAULT 'INFO',
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL,
    target     TEXT NOT NULL DEFAULT '',
    fields     TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_log_event_time ON log_event(occurred_at);
CREATE INDEX idx_log_event_component ON log_event(component, occurred_at);
CREATE INDEX idx_log_event_actor ON log_event(actor, occurred_at);
