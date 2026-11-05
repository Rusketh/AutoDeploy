-- 0038_media_tokens_clones.sql
--
-- Exported USB/ISO media tokens no longer expire: they stay valid until the
-- operator revokes them, and re-exporting reuses the image's token (so
-- existing sticks keep working). The token is kept encrypted (secrets.Box)
-- so a re-export can bake the same token in again; its hash is what is
-- checked.
ALTER TABLE image_enroll_token ADD COLUMN token_cipher TEXT NOT NULL DEFAULT '';

-- The image whose media authorised a machine's secret, so that machine's
-- first boot may bind itself to that image (and only that image).
ALTER TABLE agent_credential ADD COLUMN issued_image_id INTEGER NOT NULL DEFAULT 0;
