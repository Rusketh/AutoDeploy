# Machine groups

**Machine groups** are AutoDeploy-local sets of machines — defined and stored in
AutoDeploy itself, independent of Active Directory. Use them to filter the
inventory, target [bulk operations](bulk-operations.md), and scope
[log](logs.md) searches. There are two kinds, and the rest of AutoDeploy treats
them identically: every group resolves to a set of machines, and nothing that
consumes a group cares how that set was produced.

> "Machine group" is not the same as a machine's **AD group membership** (the
> security groups on a machine's binding). AD group membership is a machine
> attribute — and one of the criteria a *dynamic* machine group can filter on.

## The two kinds

- **Manual** — you add and remove machines by hand. A manual group can also
  **nest** other groups (see [Nesting](#nesting-groups)).
- **Dynamic** — you define a **filter** and the group is populated automatically
  by evaluating it against current inventory whenever the group is read. Add a
  matching machine to the fleet and it joins the group on its own; a machine that
  stops matching drops out.

## Where groups appear

### On the Machines page

The [Machines](machines.md) page lists your groups down the **left sidebar**,
each with its kind icon and a live member count, above an **All machines** entry.
Click a group to filter the inventory to just its members — the filter box, sort,
pager and CSV export all stay scoped to that group. The **+** at the top of the
sidebar creates a new group; **Manage groups** opens the full list.

When a **manual** group is selected, ticking machines in the table reveals
**Add to _group_** and **Remove from _group_** actions, so you curate membership
straight from the inventory you're looking at.

### The Groups page

**Groups** in the top navigation lists every group with its kind, member count,
and nested-subgroup count, and is where you create, edit, and delete them.

## Creating a group

Click **New group** (from the sidebar **+** or the Groups page). Give it a name
and description, then choose a kind:

- **Manual** — save it, then fill it from the Machines page (or nest subgroups
  on the edit form).
- **Dynamic** — define the filter. Every set criterion must match (AND); leave a
  field blank to ignore it. A dynamic group needs at least one criterion.

| Criterion | Matches |
|-----------|---------|
| **Name matches (regex)** | The machine's display name (its reported Windows name, or the binding's desired name), case-insensitively. |
| **OS contains** | A substring of the reported OS caption, e.g. `Windows 11`. |
| **OU** | The machine's target OU (its observed AD location as a fallback). Tick **Include child OUs** to match the OU and everything beneath it; otherwise the match is exact. |
| **Member of** | One of the machine's AD group memberships. |

The group's edit page shows the resolved member list so you can confirm a
dynamic filter is catching what you expect.

## Nesting groups

A **manual** group can contain other groups as **subgroups**. The parent's
membership then includes the resolved members of each child (which may themselves
be manual or dynamic). This lets you compose hierarchies — e.g. an **All labs**
group nesting **Lab A**, **Lab B**, and a dynamic **Windows 11 lab machines**
group. Loops are rejected: a group cannot contain itself, directly or through a
chain. Deleting a group quietly removes it from any parent that nested it.

## Using groups

- **Filter the inventory** — select a group in the Machines sidebar.
- **Bulk operations** — on the [bulk](bulk-operations.md) form's *Find machines*
  step, pick a **Machine group** to scope the search or, for a re-evaluated
  recurring task, to target the group directly. A recurring operation that
  targets a **dynamic** group re-resolves it on every run, so it automatically
  tracks the group's live membership.
- **Logs** — the [Logs](logs.md) search has a **Machine group** selector that
  restricts events to that group's member machines.

Group creation, edits, deletion, and membership changes are recorded in the
[activity log](logs.md) under the `groups` component.

## Removing a group

**Delete** removes the group (and its membership and nesting records). The
machines themselves are untouched. A bulk operation that still referenced the
deleted group simply resolves to no machines on its next run.

## Related

- [Machines](machines.md) — the inventory the sidebar filters.
- [Bulk operations](bulk-operations.md) — target a group.
- [Logs](logs.md) — scope a search to a group.
- [JSON API](../reference/api.md#machine-groups) — the same operations over HTTP.
