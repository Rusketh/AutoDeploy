-- 0028_bulk_wol_cancel.sql
--
-- Bulk-operation delivery options + offline notification bookkeeping.
--
--   wake_on_lan          - when 1, every run of the operation sends a
--                          Wake-on-LAN magic packet to each targeted
--                          machine's known MACs so powered-off machines
--                          boot and pick the job up.
--   cancel_after_minutes - when >0, a job still queued this long after it
--                          was created is cancelled (and any re-image flag
--                          it set is cleared), so e.g. an overnight
--                          re-image can't fire mid-morning on a machine
--                          that was powered off all night. 0 = never.
--
--   machine_record.offline_notified_at marks that a machine.offline
--   notification has been sent for the current offline episode; cleared on
--   the next check-in so the next episode notifies again.

ALTER TABLE bulk_operation ADD COLUMN wake_on_lan          INTEGER NOT NULL DEFAULT 0;
ALTER TABLE bulk_operation ADD COLUMN cancel_after_minutes INTEGER NOT NULL DEFAULT 0;

ALTER TABLE machine_record ADD COLUMN offline_notified_at DATETIME;
