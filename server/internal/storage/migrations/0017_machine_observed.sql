-- Observed identity the running agent reports back each poll: the machine's
-- current Windows computer name and its Active Directory distinguished name.
-- These track REALITY (a manual rename, an AD move) as distinct from the
-- binding, which holds the operator's DESIRED name/OU. The inventory shows
-- these as the machine's Name and AD location, and the binding is synced from
-- them so re-image and AD placement follow where the machine actually is.
ALTER TABLE machine_record ADD COLUMN reported_name TEXT NOT NULL DEFAULT '';
ALTER TABLE machine_record ADD COLUMN ad_dn TEXT NOT NULL DEFAULT '';
