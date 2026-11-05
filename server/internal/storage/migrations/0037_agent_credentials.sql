-- 0037_agent_credentials.sql
--
-- Per-machine agent secrets. Until now an agent proved who it was with its
-- server-minted agent_id alone, which /agent/enroll (and the image manifest)
-- hand to anyone who presents the machine's SMBIOS UUID. Each machine now gets
-- a random secret; only its SHA-256 is stored here.
--
--   agent_credential        - the machine's current secret hash, plus the
--                             previous one while a rotation is unconfirmed
--                             (so agent and server can never disagree).
--                             secret_hash '' = no usable secret: the machine
--                             needs an operator-approved request.
--                             No row at all = a legacy machine that has
--                             never been issued a secret (upgrade path).
--   agent_credential_request - a request for a secret that needs approval
--                             (identified by the requester's nonce).
--   agent_deploy_grant      - a one-time right, created by an authorised
--                             deploy, for the freshly installed OS to obtain
--                             a new secret (older boot clients).
--   deploy_authorization    - SMBIOS UUIDs that passed the access PIN
--                             recently, so their manifest may issue a secret.
--   image_enroll_token      - a token baked into an exported USB/ISO that
--                             authorises its machines to obtain a secret.

CREATE TABLE agent_credential (
    machine_id        INTEGER PRIMARY KEY REFERENCES machine_record(id) ON DELETE CASCADE,
    secret_hash       TEXT     NOT NULL DEFAULT '',
    prev_secret_hash  TEXT     NOT NULL DEFAULT '',
    issued_via        TEXT     NOT NULL DEFAULT '',
    issued_at         DATETIME,
    rotated_at        DATETIME,
    last_used_at      DATETIME,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE agent_credential_request (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id   INTEGER  NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    nonce_hash   TEXT     NOT NULL,
    status       TEXT     NOT NULL DEFAULT 'pending', -- pending | approved | denied | redeemed
    reason       TEXT     NOT NULL DEFAULT '',
    remote_addr  TEXT     NOT NULL DEFAULT '',
    decided_by   TEXT     NOT NULL DEFAULT '',
    decided_at   DATETIME,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (machine_id, nonce_hash)
);
CREATE INDEX idx_agent_credential_request_status ON agent_credential_request(status);

CREATE TABLE agent_deploy_grant (
    machine_id  INTEGER PRIMARY KEY REFERENCES machine_record(id) ON DELETE CASCADE,
    expires_at  DATETIME NOT NULL
);

CREATE TABLE deploy_authorization (
    system_uuid    TEXT PRIMARY KEY,
    authorized_at  DATETIME NOT NULL
);

CREATE TABLE image_enroll_token (
    image_id    INTEGER PRIMARY KEY REFERENCES image(id) ON DELETE CASCADE,
    token_hash  TEXT     NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  DATETIME NOT NULL
);
