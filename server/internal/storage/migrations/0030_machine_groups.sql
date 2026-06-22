-- 0030_machine_groups.sql
--
-- Machine Groups: an AutoDeploy-local named set of machines, independent of
-- Active Directory. Two kinds, resolved to a flat machine set the SAME way by
-- every consumer (the Machines page, bulk operations, the activity log):
--
--   manual  - explicit machine membership, optionally plus child subgroups.
--   dynamic - a stored filter (name / OS / OU / AD-group) evaluated against
--             current inventory at read time.
--
-- Groups may NEST: a group's child subgroups contribute their resolved members
-- to the parent. Cycle prevention and recursion guards live in the model layer
-- (machinegroup.go), not the schema.

CREATE TABLE machine_group (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT     NOT NULL UNIQUE,
    description TEXT     NOT NULL DEFAULT '',
    kind        TEXT     NOT NULL DEFAULT 'manual',   -- manual | dynamic
    filter_json TEXT     NOT NULL DEFAULT '{}',       -- dynamic filter; {} for manual
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Direct machine membership (manual groups). Deleting either the group or the
-- machine drops the row, so a machine removed from inventory leaves every group
-- with no extra code.
CREATE TABLE machine_group_member (
    group_id   INTEGER NOT NULL REFERENCES machine_group(id)  ON DELETE CASCADE,
    machine_id INTEGER NOT NULL REFERENCES machine_record(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, machine_id)
);
CREATE INDEX idx_machine_group_member_machine ON machine_group_member(machine_id);

-- Child subgroups (nesting). Both ends reference machine_group, so deleting any
-- group cleans up the edges where it is a parent OR a child.
CREATE TABLE machine_group_subgroup (
    group_id       INTEGER NOT NULL REFERENCES machine_group(id) ON DELETE CASCADE,
    child_group_id INTEGER NOT NULL REFERENCES machine_group(id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, child_group_id)
);
CREATE INDEX idx_machine_group_subgroup_child ON machine_group_subgroup(child_group_id);
