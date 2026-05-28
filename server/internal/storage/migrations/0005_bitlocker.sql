-- 0005_bitlocker.sql
--
-- Phase 12. BitLocker secret storage. The PIN is the user-chosen pre-boot
-- credential and is preserved across re-imaging; recovery keys are escrowed
-- and retained as an append-only history (never overwritten) so a key for
-- any prior encryption state remains available.
--
-- Both columns hold values encrypted at rest by the application using
-- AES-256-GCM with a key from internal/secrets. The raw values are never
-- stored, never logged, and only decrypted at the audited retrieval
-- boundary.

CREATE TABLE bitlocker_pin (
    machine_id    INTEGER PRIMARY KEY REFERENCES machine_record(id) ON DELETE CASCADE,
    -- ciphertext is the AES-GCM output (nonce || ciphertext || tag),
    -- base64-encoded. Empty means "no PIN" -> machine left unencrypted.
    pin_cipher    TEXT NOT NULL,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE bitlocker_recovery_key (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id    INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    key_cipher    TEXT NOT NULL,
    note          TEXT NOT NULL DEFAULT '',
    escrowed_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_recovery_key_machine ON bitlocker_recovery_key(machine_id, escrowed_at);
