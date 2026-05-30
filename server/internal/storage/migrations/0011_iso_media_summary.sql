-- 0011_iso_media_summary.sql
--
-- Make the ISO's Boot media panel show that the WHOLE install media was
-- ingested, not just the single install image. Without this the portal
-- only showed the install image (e.g. "install.swm"), which reads as if
-- that one file is all that was extracted -- confusing, since the boot
-- media is the entire ISO tree (setup.exe, boot/, efi/, sources/boot.wim,
-- the install.swm parts, ...).
--
--   media_file_count   number of files in the extracted media tree
--   media_total_bytes  total size of the extracted media tree
--   boot_wim_present   1 = sources/boot.wim (WinPE) is present

ALTER TABLE iso ADD COLUMN media_file_count  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE iso ADD COLUMN media_total_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE iso ADD COLUMN boot_wim_present  INTEGER NOT NULL DEFAULT 0;
