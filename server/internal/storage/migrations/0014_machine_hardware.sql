-- Full hardware specs the agent collects (CPU, RAM, disks, GPU, NICs) and
-- reports on each poll. Stored as a JSON blob so new fields don't need a
-- migration; the portal renders it on the machine page. Empty default for
-- machines that predate this or haven't reported yet.
ALTER TABLE machine_record ADD COLUMN hardware_json TEXT NOT NULL DEFAULT '';
