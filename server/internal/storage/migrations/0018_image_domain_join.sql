-- Per-image Active Directory domain-join configuration, performed by the
-- AGENT after first boot (full networking/DNS available) rather than by the
-- unattend's online-join during specialize, which is fragile (DC must be
-- reachable mid-Setup) and was observed to hang ~15 minutes and fail.
--
-- The join password is a secret: stored AES-256-GCM-encrypted (secrets.Box,
-- same as BitLocker PINs) and only ever handed to the deploying agent over
-- the agent API. Empty cipher = no password stored.
--
-- When enabled=1, the unattend generator suppresses its legacy
-- <Microsoft-Windows-UnattendedJoin> block for images using this image's
-- unattend, so Setup never attempts (and hangs on) the online join.
CREATE TABLE image_domain_join (
    image_id             INTEGER PRIMARY KEY REFERENCES image(id) ON DELETE CASCADE,
    enabled              INTEGER  NOT NULL DEFAULT 0,
    domain               TEXT     NOT NULL DEFAULT '',
    ou                   TEXT     NOT NULL DEFAULT '',
    join_user            TEXT     NOT NULL DEFAULT '',
    join_password_cipher TEXT     NOT NULL DEFAULT '',
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
