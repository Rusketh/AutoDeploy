-- AutoDeploy mints its OWN per-machine UUID (agent_id) rather than trusting
-- the SMBIOS/BIOS UUID. Manufacturers frequently ship duplicate or all-zero
-- BIOS UUIDs across a bulk fleet, so it can't be relied on as a unique key.
-- This server-assigned id is the stable "object id" the agent stores in the
-- registry and identifies itself with when it polls /api/v1/agent/self.
--
-- New rows get an id at INSERT time (in InventoryRepo.UpsertFromIdentity);
-- pre-existing rows backfill lazily on their next upsert. The unique index
-- is partial so the transient empty default never collides.
ALTER TABLE machine_record ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_machine_record_agent_id
    ON machine_record(agent_id) WHERE agent_id != '';
