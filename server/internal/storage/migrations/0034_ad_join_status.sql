-- Active Directory join status, reported by the resident agent each poll.
--
-- ad_join_status is one of:
--   ''            unknown / not applicable (workgroup, or the image doesn't
--                 join a domain) — the default for existing rows.
--   'joined'      domain-joined AND the secure channel (trust) is healthy.
--   'join_failed' the agent attempted an AD join and it failed.
--   'trust_broken' the machine is domain-joined but the secure channel /
--                 trust relationship test fails ("the trust relationship
--                 between this workstation and the primary domain failed").
--
-- ad_join_detail carries the last human-readable error/detail (never any
-- credential). ad_join_reported_at is when the agent last reported a status.
--
-- The *_notified_at columns dedup failure alerts to once per failure episode
-- (mirrors offline_notified_at): they are set when the alert fires and cleared
-- when the machine reports a healthy 'joined' again, re-arming the alert.
ALTER TABLE machine_record ADD COLUMN ad_join_status TEXT NOT NULL DEFAULT '';
ALTER TABLE machine_record ADD COLUMN ad_join_detail TEXT NOT NULL DEFAULT '';
ALTER TABLE machine_record ADD COLUMN ad_join_reported_at DATETIME;
ALTER TABLE machine_record ADD COLUMN ad_join_failed_notified_at DATETIME;
ALTER TABLE machine_record ADD COLUMN ad_trust_failed_notified_at DATETIME;
