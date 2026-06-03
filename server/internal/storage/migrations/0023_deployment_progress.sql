-- Deployment software-install progress: drive the live progress bar through
-- the agent-run software phase, not just the boot/Setup milestones. The agent
-- reports how many of the machine's assigned packages it has installed so far
-- (e.g. 2 of 5); the machine detail page renders "Installing software (2/5)"
-- and scales the bar within the install band. progress_total is the number of
-- packages the agent set out to install for this deploy; progress_done is how
-- many it has finished. Both default 0 (no software phase reported yet).

ALTER TABLE deployment_history ADD COLUMN progress_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE deployment_history ADD COLUMN progress_total INTEGER NOT NULL DEFAULT 0;
