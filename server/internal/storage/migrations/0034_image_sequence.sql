-- Per-image event sequence: an image links an optional sequence that defines
-- the order of deploy actions (domain join, software, reboots, scripts, GPO
-- update, nested sequences). Resolved NEAREST-WINS up the image parent chain,
-- exactly like loadout_id — a child image's sequence overrides an ancestor's.
--
-- Optional: an image with no sequence (directly or inherited) keeps AutoDeploy's
-- default behaviour (domain join, then software), synthesised by the resolver.
--
-- ON DELETE SET NULL so deleting a shared sequence simply unlinks it rather than
-- blocking; the resolver then falls back to the inherited/default sequence.
ALTER TABLE image ADD COLUMN sequence_id INTEGER
    REFERENCES sequence(id) ON DELETE SET NULL;

CREATE INDEX idx_image_sequence_id ON image(sequence_id);
