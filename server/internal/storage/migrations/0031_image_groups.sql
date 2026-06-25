-- Image groups: named buckets for organising images on the PXE boot menu.
-- An image belongs to at most one group; deleting a group ungroups its
-- images automatically (ON DELETE SET NULL) so deletion never blocks.
CREATE TABLE image_group (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT     NOT NULL UNIQUE,
    description TEXT     NOT NULL DEFAULT '',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE image ADD COLUMN group_id INTEGER
    REFERENCES image_group(id) ON DELETE SET NULL;

CREATE INDEX idx_image_group_id ON image(group_id);
