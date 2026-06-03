-- Bulk operation name + description: an operator-facing label and optional
-- notes so scheduled bulk tasks are easy to scan, search and sort on the bulk
-- list. Both blank by default; the server fills a sensible default from the
-- action and target (e.g. "Re-image — 12 machines") when the operator leaves
-- them empty, so existing rows and API callers that omit them still get a
-- meaningful name on next write. Pre-existing rows keep '' until edited; the
-- portal list falls back to the action for those.

ALTER TABLE bulk_operation ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE bulk_operation ADD COLUMN description TEXT NOT NULL DEFAULT '';
