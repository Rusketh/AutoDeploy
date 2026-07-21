-- Sequences: reusable, orderable "task sequences" (SCCM-style). A sequence is
-- an ordered list of steps; each step is one of:
--   kind='builtin'     → a built-in deploy action (builtin_action):
--                        software | domainjoin | reboot | gpupdate
--   kind='task'        → run a Script/Task (task_id)
--   kind='subsequence' → expand another sequence inline (child_sequence_id)
--
-- Sequences can nest other sequences; recursion is rejected at save time by the
-- repository (ErrCycle), mirroring machine-group nesting.
--
-- owner_image_id NULL  → a shared, reusable sequence (appears in the Sequences
--                        library and can be linked/inherited by images).
-- owner_image_id set   → an image-owned inline sequence, edited on the image
--                        page and hidden from the shared library; it cascades
--                        away with the image.
CREATE TABLE sequence (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT     NOT NULL,
    description    TEXT     NOT NULL DEFAULT '',
    owner_image_id INTEGER  REFERENCES image(id) ON DELETE CASCADE,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Shared (library) sequence names are unique; image-owned ones are not
-- constrained (an image owns at most one anyway).
CREATE UNIQUE INDEX idx_sequence_shared_name
    ON sequence(name) WHERE owner_image_id IS NULL;

CREATE TABLE sequence_step (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    sequence_id         INTEGER  NOT NULL REFERENCES sequence(id) ON DELETE CASCADE,
    order_value         INTEGER  NOT NULL DEFAULT 100,
    kind                TEXT     NOT NULL,               -- builtin|task|subsequence
    builtin_action      TEXT     NOT NULL DEFAULT '',    -- software|domainjoin|reboot|gpupdate
    task_id             INTEGER  REFERENCES task(id) ON DELETE RESTRICT,
    child_sequence_id   INTEGER  REFERENCES sequence(id) ON DELETE RESTRICT,
    continue_on_failure INTEGER  NOT NULL DEFAULT 0
);

CREATE INDEX idx_sequence_step_seq ON sequence_step(sequence_id, order_value);
