-- Per-image "setup lockout" toggle. When enabled=1, machines deployed from
-- this image show a branded, full-screen setup screen IN PLACE OF the Windows
-- logon screen while the AGENT performs the initial software rollout, blocking
-- normal sign-in until the machine is ready for first use. A technician can
-- dismiss it with the global Access PIN (auth/pin.go).
--
-- This affects ONLY the initial deployment: the lock is gated on an open
-- deployment (the agent clears it once it closes the deployment), so later
-- software pushes to an already-provisioned machine never lock it.
--
-- The mechanism is a Windows Credential Provider injected into the deployed OS
-- at imaging time (via the boot client's $OEM$ tree) when this is enabled for
-- the image; disabled images deploy exactly as before.
CREATE TABLE image_setup_lock (
    image_id   INTEGER PRIMARY KEY REFERENCES image(id) ON DELETE CASCADE,
    enabled    INTEGER  NOT NULL DEFAULT 0,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
