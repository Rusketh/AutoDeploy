-- 0009_deploy_tokens.sql
--
-- Per-machine deploy tokens. Issued by the server on the agent's
-- open report (outcome=in_progress) at the start of a deploy, and
-- required as a bearer for secret-returning agent endpoints
-- (specifically POST /api/v1/agent/bitlocker/config, which returns
-- the cleartext PIN).
--
-- The audit (§10.2) flagged that the previous design -- "be on the
-- network and know the SMBIOS UUID" -- was too weak a credential
-- for cleartext PIN disclosure. This table fixes that: the token is
-- only ever returned to a freshly deploying client, so an attacker
-- who knows the UUID still cannot retrieve the PIN.
--
-- Storage: SHA-256 hash of the token, never the cleartext. The
-- token is issued once and re-issued (rotated) on every new deploy.

CREATE TABLE machine_deploy_token (
    machine_id   INTEGER PRIMARY KEY REFERENCES machine_record(id) ON DELETE CASCADE,
    -- 64 hex chars of SHA-256 over the cleartext token.
    token_hash   TEXT NOT NULL,
    issued_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   DATETIME NOT NULL
);
