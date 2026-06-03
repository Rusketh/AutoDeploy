-- 0022_bulk_schedule.sql
--
-- Bulk-operation lifecycle + scheduling.
--
--   Bulk operations gain a status, an optional schedule (one-time or
--   recurring), and a target mode that decides whether a recurring run
--   re-resolves its filter or replays a frozen machine selection.
--
--   bulk_job gains run_no so each materialised run of a recurring
--   operation is distinguishable, and 'cancelled' joins the status set.

ALTER TABLE bulk_operation ADD COLUMN status           TEXT NOT NULL DEFAULT 'active';
   -- scheduled | active | paused | completed | cancelled
ALTER TABLE bulk_operation ADD COLUMN schedule_kind    TEXT NOT NULL DEFAULT 'now';
   -- now | once | recurring
ALTER TABLE bulk_operation ADD COLUMN target_mode      TEXT NOT NULL DEFAULT 'selection';
   -- selection (frozen machine_ids) | filter (re-resolve each run)
ALTER TABLE bulk_operation ADD COLUMN run_at           DATETIME;        -- one-time fire time
ALTER TABLE bulk_operation ADD COLUMN recur_spec       TEXT NOT NULL DEFAULT '';
   -- JSON, e.g. {"every":2,"unit":"week","at":"02:00","weekday":1} or {"cron":"0 2 * * 1"}
ALTER TABLE bulk_operation ADD COLUMN next_run_at      DATETIME;        -- scheduler's next fire
ALTER TABLE bulk_operation ADD COLUMN last_run_at      DATETIME;
ALTER TABLE bulk_operation ADD COLUMN run_count        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bulk_operation ADD COLUMN reimage_image_id INTEGER NOT NULL DEFAULT 0;
   -- persisted so scheduled/recurring re-images can re-apply the flag each run

CREATE INDEX idx_bulk_op_next_run ON bulk_operation(status, next_run_at);

-- bulk_job: group by run + add 'cancelled' terminal status.
ALTER TABLE bulk_job ADD COLUMN run_no INTEGER NOT NULL DEFAULT 1;  -- status: queued|running|ok|failed|cancelled
CREATE INDEX idx_bulk_job_op_run ON bulk_job(operation_id, run_no);

-- Back-compat: existing operations already materialised their (single) run.
UPDATE bulk_operation SET run_count = 1, last_run_at = created_at;
