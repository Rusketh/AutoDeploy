-- 0010_iso_boot_media.sql
--
-- Boot-the-media deploy: record how each ISO's install image was
-- prepared for FAT32 boot media. Deploys stage a bootable copy of the
-- media on a single FAT32 partition; a sources/install.wim larger than
-- FAT32's 4 GiB limit is split into <4 GiB install.swm parts at ingest
-- (the original is then removed). These columns are descriptive only --
-- the operator configures nothing; the portal reports readiness from
-- them.
--
--   install_image_format  wim | esd | swm | ''   ('' = not yet extracted)
--   install_image_bytes   size of the install image before any split
--   swm_parts             0 = not split (fit FAT32); N = split into N parts
--   bootloader_present    1 = efi/boot/bootx64.efi found (UEFI-bootable)
--   prep_error            non-empty => media not deploy-ready
--   media_prepared_at     when prepare last ran (NULL = never)

ALTER TABLE iso ADD COLUMN install_image_format TEXT    NOT NULL DEFAULT '';
ALTER TABLE iso ADD COLUMN install_image_bytes  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE iso ADD COLUMN swm_parts            INTEGER NOT NULL DEFAULT 0;
ALTER TABLE iso ADD COLUMN bootloader_present   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE iso ADD COLUMN prep_error           TEXT    NOT NULL DEFAULT '';
ALTER TABLE iso ADD COLUMN media_prepared_at    DATETIME;
