-- Remote re-image: an operator can flag a machine (or a fleet, via bulk) to
-- re-image. The flag is set server-side; the running agent reboots the
-- machine, it network-boots into AutoDeploy, and the boot client auto-
-- deploys the flagged image without waiting at the menu. The flag is
-- cleared when the boot client reports the deploy has started ("staging"),
-- so it fires exactly once and the reboot loop terminates.
--
-- reimage_image_id is the image to deploy; NULL/0 means use the machine's
-- existing binding.
ALTER TABLE machine_record ADD COLUMN reimage_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE machine_record ADD COLUMN reimage_image_id INTEGER NOT NULL DEFAULT 0;
