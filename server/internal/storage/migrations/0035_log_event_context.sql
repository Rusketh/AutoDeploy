-- 0035_log_event_context.sql
--
-- Readable, linkable log rows. Every column is nullable and additive: rows
-- written before this migration keep NULLs and the server fills the gaps at
-- read time (the message/hint come from the action catalogue, the machine
-- from the actor), so old logs still display well and nothing is rewritten.
--
--   message       plain-English sentence for the row, rendered at ingest from
--                 the action catalogue (or the client's own fields.message).
--   machine_id    machine_record.id the event belongs to: from the client's
--                 X-AutoDeploy-Machine-UUID header, else from an actor that is
--                 a known SMBIOS UUID / agent ID, else a "machine:N" target.
--   deployment_id deployment_history.id of the run the event was part of
--                 (fields.deployment_id from newer clients).
--   user          portal user behind an audit event (login, edits, ...).
--   outcome       'ok' / 'failed' when the event reports a result.
--   error         the low-level error text (fields.error).
--   hint          what to try next, for failures the catalogue knows.
ALTER TABLE log_event ADD COLUMN message TEXT;
ALTER TABLE log_event ADD COLUMN machine_id INTEGER;
ALTER TABLE log_event ADD COLUMN deployment_id INTEGER;
ALTER TABLE log_event ADD COLUMN user TEXT;
ALTER TABLE log_event ADD COLUMN outcome TEXT;
ALTER TABLE log_event ADD COLUMN error TEXT;
ALTER TABLE log_event ADD COLUMN hint TEXT;

-- Cursor paging walks id DESC; the per-machine / per-deployment views page
-- the same way within their slice.
CREATE INDEX idx_log_event_machine ON log_event(machine_id, id);
CREATE INDEX idx_log_event_deployment ON log_event(deployment_id, id);
CREATE INDEX idx_log_event_level ON log_event(level, id);
CREATE INDEX idx_log_event_user ON log_event(user, id);
CREATE INDEX idx_log_event_action ON log_event(action, id);
