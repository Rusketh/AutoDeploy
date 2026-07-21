-- Sequence execution cursor: the index of the next resolved step the agent
-- should run for an open deployment. The server owns this cursor so that a
-- mid-sequence reboot (an explicit reboot step, or a domain-join reboot) is
-- transparent: after the machine comes back and polls /api/v1/agent/self, the
-- server returns the remaining steps from seq_cursor onward and the agent
-- continues where it left off. The agent advances the cursor by posting to
-- /api/v1/agent/sequence-progress after each step; when it passes the last
-- step the deployment closes as usual.
ALTER TABLE deployment_history ADD COLUMN seq_cursor INTEGER NOT NULL DEFAULT 0;
