-- Software packages can depend on other packages. Resolving a package
-- (for an image's set or an ad-hoc bulk push) auto-includes its
-- dependencies transitively and installs them first. Stored as a JSON
-- array of package IDs; empty default for existing rows.
ALTER TABLE software_package ADD COLUMN depends_on_json TEXT NOT NULL DEFAULT '[]';
