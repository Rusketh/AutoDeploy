-- 0008_mirrors.sql
--
-- Mass-scale deployment: payload mirrors per site.
--
-- A mirror is any HTTP server that serves the same /payload/... path
-- layout the primary serves. Operators register mirrors with a site
-- name, a base URL and a priority. The manifest endpoint rewrites WIM,
-- driver and software URLs to point at the highest-priority healthy
-- mirror matching the requesting machine's site. The primary remains
-- the single source of truth for metadata (manifest, identity, agent
-- reports, log ingest) and small payloads (unattend XML).
--
-- Mirror sync is an operator concern (rsync from the primary's data
-- dir, or autodeploy-server --mirror mode that pulls through on miss).
-- AutoDeploy does not manage replication itself; the URL rewrite is
-- the contract.

CREATE TABLE payload_mirror (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    base_url    TEXT    NOT NULL,
    site        TEXT    NOT NULL DEFAULT '',  -- empty = matches any site
    priority    INTEGER NOT NULL DEFAULT 100, -- lower = preferred
    healthy     INTEGER NOT NULL DEFAULT 1,
    last_checked DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_payload_mirror_site_priority ON payload_mirror(site, priority);

-- Remember each machine's last-seen site so we can pick the right
-- mirror even when the Boot Client doesn't pass the header on a
-- particular request.
ALTER TABLE machine_record ADD COLUMN last_site TEXT NOT NULL DEFAULT '';
