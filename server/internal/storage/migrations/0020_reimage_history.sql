-- Re-image history: one concise row per re-image event so operators can see
-- when a machine was rebuilt and what triggered it, without mining the
-- verbose deployment_history. Distinct from deployment_history (which logs
-- every deploy, including first installs); a row is written here only when a
-- machine that already had an OS is re-imaged.

CREATE TABLE reimage_event (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    machine_id  INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    image_id    INTEGER REFERENCES image(id),
    -- How the re-image was triggered: "remote_flag" (portal/bulk remote
    -- re-image) or "boot_menu" (operator picked Re-image at the boot menu).
    source      TEXT     NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_reimage_event_machine ON reimage_event(machine_id, created_at);
