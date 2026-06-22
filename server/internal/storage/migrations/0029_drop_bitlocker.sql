-- Remove BitLocker drive-encryption and the per-machine deploy token.
-- The agent no longer manages BitLocker (it clobbered GPO/AD-managed
-- encryption and did not reliably encrypt), so the PIN, recovery-key, and
-- deploy-token tables are dropped. Forward-only: the migration runner has
-- no down step. IF EXISTS keeps this a no-op on fresh databases that never
-- created the tables.
DROP TABLE IF EXISTS bitlocker_pin;
DROP TABLE IF EXISTS bitlocker_recovery_key;
DROP TABLE IF EXISTS machine_deploy_token;
