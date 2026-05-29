-- 0010_software_payload_filename.sql
--
-- Operators kept losing track of which file they uploaded for a
-- software package: the form said "Current: software/1/payload.bin
-- (15 bytes)" with no hint of the original filename. The agent
-- renames uploads to a canonical name on disk so it's not even
-- recoverable from the storage path. Add an explicit
-- payload_filename column we can fill on upload and surface in the
-- portal so operators can see "office-365-installer.exe (1.2 GB)"
-- instead of an opaque blob path.
--
-- Existing rows get an empty default; the form treats empty as
-- "unknown filename" and renders the blob path as the fallback
-- (no UX regression for pre-existing uploads).

ALTER TABLE software_package
    ADD COLUMN payload_filename TEXT NOT NULL DEFAULT '';
