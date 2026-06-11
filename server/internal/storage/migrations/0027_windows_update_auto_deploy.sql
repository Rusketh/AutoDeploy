-- Per-update "auto-deploy" toggle. When auto_deploy=1 and the update has an
-- uploaded payload, the server queues an install job for every APPLICABLE
-- machine (OS filter match) that does not already have the KB installed.
-- Applicability is evaluated lazily at each machine's agent poll, so machines
-- added or reimaged later are covered automatically without the operator
-- re-running "Deploy updates". Auto-queued jobs hang off a per-update
-- deployment row (created_by='auto-deploy') so progress is visible like any
-- manual rollout, and attempts are capped so a failing update cannot loop.
ALTER TABLE windows_update ADD COLUMN auto_deploy INTEGER NOT NULL DEFAULT 0;
