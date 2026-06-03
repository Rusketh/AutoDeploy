-- Deployment phase: persist the canonical milestone token for the live deploy
-- progress bar on the machine detail page. Previously the phase survived only
-- inside the freeform notes field (brittle to match on). The boot client's
-- deploy-status callbacks now write this column directly:
--   staging | staged | specialize | first-logon | complete
-- Existing rows default to '' (treated as "unknown/legacy" by the UI, which
-- falls back to an indeterminate bar for any still-in_progress legacy row).

ALTER TABLE deployment_history ADD COLUMN phase TEXT NOT NULL DEFAULT '';
