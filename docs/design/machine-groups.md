# Machine Groups — Implementation Plan

Status: PLAN — awaiting approval before implementation.
No code in this document by design; it is a precise specification of the
data model, layers, endpoints, UI, integrations, and the file-by-file change
set required to land the feature.

---

## 1. Decision

Add **Machine Groups**: a first-class object **local to AutoDeploy** (stored
in the server's SQLite database, independent of Active Directory) that names a
set of machines. There are **two kinds**, and the rest of the system **treats
them identically**:

- **Manual group** — the operator explicitly adds and removes machines.
- **Dynamic group** — the operator defines a filter (name match, OS, OU,
  AD-group membership) and the group is populated by evaluating that filter
  against current inventory whenever the group is read.

The unifying contract: **every group resolves to a set of machine IDs through
one method.** Consumers (the Machines page, bulk operations, the activity log,
and future call sites) only ever ask a group "who are your members?" and never
branch on the group's kind. The kind matters in exactly two places: the editing
UI, and the resolver's internal implementation.

This mirrors a pattern AutoDeploy already ships: a bulk operation today targets
either a **frozen selection** of machine IDs or a **re-evaluated filter**
(`bulk_operation.target_mode` = `selection` | `filter`). Manual groups are the
"selection" idea promoted to a named, reusable object; dynamic groups are the
"filter" idea promoted the same way. We reuse that proven machinery rather than
inventing a parallel one.

### Terminology guard

"Group" is overloaded, so this document is explicit:

- **Machine Group** (new) — an AutoDeploy-local object, the subject of this plan.
- **AD group membership** — the existing `machine_binding.group_memberships`
  list (the AD security groups a machine is placed in). This remains a *machine
  attribute*, and becomes one of the *filter criteria* a dynamic Machine Group
  can match on ("member of"). It is **not** the same thing as a Machine Group.

---

## 2. Goals and non-goals

### Goals

1. Create either kind of group with a **simple** flow, inline from the Machines
   page.
2. The Machines page shows groups in a **left-hand sidebar**; selecting a group
   filters the inventory table to that group's members only.
3. Manual groups: add/remove machines using the inventory selection already on
   the page.
4. Dynamic groups: define a filter on **name**, **OS**, **OU**, and **AD-group
   membership ("member of")**, evaluated live.
5. Groups are usable as a **target for bulk operations**, a **filter for the
   activity log**, and are designed so other call sites can adopt them with no
   new concepts.

### Non-goals (this iteration)

- Nested groups (a group whose members are other groups). The schema leaves room
  but the resolver does not recurse yet.
- Sharing/scoping groups per-operator or per-role (groups are global, like
  loadouts and mirrors today).
- Pushing AutoDeploy Machine Groups into Active Directory. These stay local.

---

## 3. Core design principle — "treated the same"

A single entity, `MachineGroup`, carries a `Kind` discriminator. The model layer
exposes one resolver:

- **Members(group) → []machine ID** — for a manual group it reads the membership
  table; for a dynamic group it evaluates the stored filter against current
  inventory. Callers get an ID set and never learn which path produced it.

Two convenience reads sit on top of the same resolver:

- **MemberCount(group) → int** — for sidebar badges and list pages.
- **MemberUUIDs(group) → []SMBIOS UUID** — for consumers keyed on UUID rather
  than internal ID (the activity log's `actor` is the SMBIOS UUID).

Everything else in the system depends only on these three reads. That is the
entire "treated the same" guarantee, enforced by having no public API that
exposes the kind to consumers.

---

## 4. Data model

### 4.1 New table: `machine_group`

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PRIMARY KEY AUTOINCREMENT | internal ID, the `ID` domain type |
| `name` | TEXT NOT NULL UNIQUE | validated like every other named entity |
| `description` | TEXT NOT NULL DEFAULT '' | |
| `kind` | TEXT NOT NULL | `manual` \| `dynamic` |
| `filter_json` | TEXT NOT NULL DEFAULT '{}' | the dynamic filter; `{}` for manual groups |
| `created_at` | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP | |
| `updated_at` | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP | |

Follows the established entity shape (`payload_mirror`, `software_loadout`):
unique name, description, timestamps, JSON for the variable part.

### 4.2 New table: `machine_group_member` (manual groups only)

| Column | Type | Notes |
|--------|------|-------|
| `group_id` | INTEGER NOT NULL → `machine_group(id)` ON DELETE CASCADE | |
| `machine_id` | INTEGER NOT NULL → `machine_record(id)` ON DELETE CASCADE | |
| | PRIMARY KEY (`group_id`, `machine_id`) | composite, idempotent membership |

Cascade on both foreign keys means: deleting a group drops its rows; deleting a
machine (the existing inventory delete) drops it from every manual group with no
extra code. Index `(machine_id)` to make "which groups is this machine in?" — a
machine-detail-page read — cheap. This exactly mirrors the
`software_loadout` / `software_loadout_package` parent/child pattern.

Dynamic groups never write to this table; their membership is computed.

### 4.3 The dynamic filter shape

Stored as JSON in `machine_group.filter_json`. Fields, each optional; an unset
field is "don't care"; **all set fields are AND-combined** (consistent with the
existing bulk target and the `match` package's per-key AND semantics):

| Field | Source of truth on a machine | Match semantics |
|-------|------------------------------|-----------------|
| name match | the machine's display name (reported Windows name, falling back to the binding's desired name) | regular expression, case-insensitive |
| OS | `machine_record.hardware_json` → `os_caption` | substring, case-insensitive (e.g. `windows 11` matches "Microsoft Windows 11 Pro"); same approach the Windows-Update applicability filter already uses |
| OU | the machine's desired OU (`machine_binding.target_ou`) with the observed AD DN (`machine_record.ad_dn`) as fallback | exact DN match, or **subtree** match when a "include child OUs" flag is set |
| member of | `machine_binding.group_memberships[]` (the AD groups) | membership test, case-insensitive |

Notes:

- The criteria set is deliberately the same vocabulary bulk targeting already
  speaks (name regex, OU, AD group), **plus OS**, which the user asked for and
  which bulk targeting lacks today. Adding OS is the only genuinely new predicate.
- A dynamic group **must define at least one criterion** (save-time validation).
  An all-empty filter would otherwise mean "every machine," which is surprising
  for a named group; operators who want "everything" simply use the existing
  "All machines" view. (Flagged as an open decision in §13.)

### 4.4 Referencing a group from a bulk operation — no migration needed

A bulk operation persists its target as schemaless JSON in
`bulk_operation.target_json`. Adding an optional **group reference** to the bulk
target is therefore a struct-field addition only — **no schema migration**. See
§9.1.

### 4.5 Migration

One new forward-only file, `server/internal/storage/migrations/0030_machine_groups.sql`,
creating the two tables and their indexes. It follows the existing runner
(lexicographic order, one transaction per file, recorded in `schema_migration`).
`0030` is the next free number (the tree is at `0029`).

---

## 5. Domain / model layer

### 5.1 Types (in `server/internal/model/types.go`, beside the other entities)

- **MachineGroup** — `ID`, `Name`, `Description`, `Kind`, `Filter`
  (the decoded dynamic filter), `CreatedAt`, `UpdatedAt`. The struct also
  carries a non-persisted `MemberCount` populated on list reads for the UI.
- **MachineFilter** — the four optional criteria from §4.3 plus the OU-subtree
  flag. This is a standalone, reusable type (see §5.3), not nested inside the
  group, so bulk targeting can reference the same type.

### 5.2 Repository (new `server/internal/model/machinegroup.go`)

A `MachineGroupRepo` wrapping `*storage.DB`, constructed by
`NewMachineGroupRepo(db)`, matching the mirror/loadout repos exactly. Methods:

- **Create / Get / List / Update / Delete** — standard CRUD. `List` populates
  `MemberCount` per group (manual: a COUNT on the member table; dynamic:
  resolver count). `Update` of a manual group rewrites membership transactionally
  the way `SoftwareLoadoutRepo.Update` rewrites packages. `Delete` is a hard
  delete; member rows cascade.
- **Members(ctx, id) → []ID** — the unified resolver. Manual: select from
  `machine_group_member`. Dynamic: load the filter and evaluate it (via §5.3).
- **MemberCount(ctx, id) → int** and **MemberUUIDs(ctx, id) → []string** — built
  on `Members`.
- **AddMembers(ctx, id, []ID)** / **RemoveMembers(ctx, id, []ID)** /
  **SetMembers(ctx, id, []ID)** — manual-only mutation; reject (validation error)
  on a dynamic group so the "treated the same" read API can't be misused to write
  a computed group.
- **GroupsForMachine(ctx, machineID) → []MachineGroup** — reverse lookup for the
  machine-detail page (manual via the member table; dynamic by testing each
  dynamic group's filter against that one machine).

Validation and error mapping reuse the shared helpers (`validateName`,
`isUniqueErr` → `ErrConflict`, `sql.ErrNoRows` → `ErrNotFound`,
`ErrValidation`, `ErrInUse`). A group that is referenced by a bulk operation's
stored target is refused on delete with `ErrInUse` (or the reference is cleared —
see §13).

### 5.3 Shared machine matcher (the important reuse)

Today the in-Go matching logic lives inside `BulkRepo.PreviewTargets`
(`server/internal/model/bulk.go`): it loads the whole inventory via
`InventoryRepo.List`, then filters in Go by name regex, OU, and AD group. The
codebase's deliberate scale assumption — "all machines first, then filter in Go;
at thousands of machines this is plenty" — applies equally to dynamic groups.

Plan: **extract that predicate into one reusable matcher** and have both bulk
targeting and dynamic-group resolution call it.

- New `server/internal/model/machinematch.go`: a function that, given a
  `MachineFilter` and the already-loaded inventory rows + bindings, returns the
  matching machine IDs (and a compiled-regex error path for an invalid name
  pattern). It adds the **OS** predicate that bulk targeting lacks.
- `BulkRepo.PreviewTargets` is refactored to build a `MachineFilter` from its
  existing `BulkTarget` fields (name regex → name, OU → OU, group → member of)
  and call the shared matcher. Behavior is unchanged for existing bulk users;
  the OS field is simply unused by the current bulk form until §9.1 surfaces it.

This keeps one matching implementation, one place to fix bugs, and guarantees a
dynamic group and a bulk filter with the same criteria select the same machines.

If the refactor is judged too invasive for a first cut, the fallback is a
self-contained matcher used only by groups, with bulk left untouched; the cost is
duplicated predicate logic. The shared matcher is recommended.

### 5.4 Wiring

`NewMachineGroupRepo(db)` is constructed in `repos()` in
`server/cmd/autodeploy-server/main.go` and added to the `appRepos` struct,
exactly as every other repo.

---

## 6. API layer

New handler file `server/internal/api/machinegroup_handlers.go`, registered in
`server/internal/api/api.go` (a `Groups *model.MachineGroupRepo` field on
`Repos`, routes under `requireAuth`). Conventions match the loadout handlers
(`decodeJSON`, `parseID`, `validateName`, `writeJSON`, `writeError`, CSRF via the
`X-Requested-With` requirement on mutating verbs).

| Method & path | Purpose |
|---------------|---------|
| `GET /api/v1/machine-groups` | list groups (with member counts) |
| `POST /api/v1/machine-groups` | create (manual or dynamic) |
| `GET /api/v1/machine-groups/{id}` | read one group |
| `PUT /api/v1/machine-groups/{id}` | update name/description/filter/membership |
| `DELETE /api/v1/machine-groups/{id}` | delete |
| `GET /api/v1/machine-groups/{id}/members` | resolved members (works for both kinds) |
| `POST /api/v1/machine-groups/{id}/members` | add machines (manual only) |
| `DELETE /api/v1/machine-groups/{id}/members` | remove machines (manual only) |

**Audit logging.** Every mutating handler appends a `LogEvent` with
`component: "groups"` and an action of `group.create` / `group.update` /
`group.delete` / `group.members_changed`, target = the group name/ID, fields =
counts and kind — using the existing `r.Logs.Append` pattern. This is what makes
group changes show up in the very activity log the feature also helps filter.

**Notifications.** Optional and reserved: a `group.created` /
`group.membership_changed` event can be emitted through the existing
`Emitter.Emit` if desired, but groups are configuration objects, so this is not
required for v1 and is listed as a reserved event in the docs.

`reference/api.md` gains a Machine Groups section documenting the bodies.

---

## 7. Portal / UI

The portal is server-rendered Go `html/template` with progressive-enhancement
vanilla JS (no SPA, no htmx-heavy flows). All additions follow that grain.

### 7.1 Machines page: left-hand group sidebar

The Machines list (`machineList` in `server/internal/portal/inventory.go`,
template `machine_list.html`) is currently a single centred column: toolbar +
table + pager. Change it to a **two-pane layout**:

- A new CSS grid wrapper (e.g. `grid-template-columns: 220px 1fr`) added to
  `style.css`, collapsing to stacked/hidden at the existing 900px breakpoint.
  No existing portal page has a sidebar; the closest precedent is the bulk form's
  two-table layout, so this is a small, additive CSS block.
- **Left pane** lists, in order: **All machines** (the unfiltered default), then
  each group with its name, a kind icon (manual vs dynamic), and a member-count
  badge. A small **+ New group** affordance sits at the top or bottom of the
  pane for the "simple creation" requirement.
- **Right pane** is the existing table, untouched in structure.

### 7.2 Filtering the table by group

Introduce a `?group=<id>` query parameter, handled in `machineList`:

- After loading inventory, when `group` is present, resolve that group's member
  IDs via `MachineGroupRepo.Members` and restrict the row set to them **before**
  the existing text filter (`?q=`), sort, and pagination run. Group filter and
  text filter compose (group narrows the universe, `q` searches within it).
- **Thread `?group=` through every URL builder** the page already has so it
  survives navigation: the sort-header links (`machineSortColumns`), the pager
  links (`paginate` / `_pagination.html`), the items-per-page selector, the text
  filter form, and the **CSV export** link (so an export of a selected group
  contains exactly that group). This is the same discipline the page already
  applies to `q`, `sort`, `dir`, and `size`.
- The selected group is marked active in the sidebar; "All machines" clears it.

No new client-side framework is needed: selecting a group is an ordinary link to
`/portal/machines?group=<id>`, and the existing debounced server-side filter
keeps working within it.

### 7.3 Editing membership from the inventory (manual groups)

The page already has per-row checkboxes that POST `machine_ids[]` (today to the
bulk-delete endpoint). When a **manual** group is selected, surface two actions
on the existing selection bar — **Add selected to group** and **Remove selected
from group** — POSTing the same `machine_ids[]` to new portal routes that call
`AddMembers` / `RemoveMembers`. This reuses the existing selection UX wholesale;
no new selection mechanism. For a **dynamic** group these actions are hidden
(membership is computed), reinforcing "treated the same" on read while keeping
writes meaningful only where they apply.

### 7.4 Creating and editing groups (simple)

- **Create** is a small form (template `machine_group_form.html`): name,
  description, and a **kind** choice. Choosing *manual* finishes immediately (an
  empty group you then fill from the inventory selection). Choosing *dynamic*
  reveals the filter builder: a name-regex field, an OS substring field, an OU
  field with an "include child OUs" checkbox, and a "member of" AD-group field —
  i.e. the four criteria of §4.3, presented like the bulk form's existing
  search fields. A **live preview count** ("matches N machines") reuses the
  members endpoint, mirroring the bulk form's `PreviewTargets`-backed preview.
- **Edit** reuses the same form. Manual groups can also be curated from the
  inventory (§7.3).
- Group management can live inline on the Machines page (create/edit via the
  sidebar) and/or as a light `/portal/groups` list; a dedicated list page is
  optional given the sidebar already lists them.

### 7.5 Navigation and routes

- Portal routes for the group pages and membership actions are registered in
  `Register()` in `server/internal/portal/portal.go`, alongside the existing
  machine routes; the handlers live in a new
  `server/internal/portal/machinegroup.go`.
- A nav entry is optional. Because the primary surface is the Machines sidebar,
  a separate top-nav "Groups" link in `_layout.html` is a nicety, not a
  requirement; if added it follows the existing `{{if eq .Path ...}}on{{end}}`
  active-state pattern and the `_icons.html` sprite.

### 7.6 Machine detail page

The machine detail page gains a small read-only **"Groups"** line listing the
groups the machine belongs to (via `GroupsForMachine`), each linking to the
filtered Machines view. This is the natural place an operator confirms a dynamic
filter is catching the machine they expect.

---

## 8. Assets

- `style.css`: one new block for the two-pane grid, the sidebar list, the kind
  icons, and the count badge (reusing existing `.badge` / `.dot` tokens).
- `app.js`: at most a tiny enhancement for the create form's kind toggle
  (show/hide the dynamic filter fields) and the live preview count fetch; both
  degrade gracefully without JS (the fields just all show). No framework.
- `_icons.html`: two small SVG glyphs (manual vs dynamic) if we want the visual
  distinction.

---

## 9. Integration with the rest of the system

### 9.1 Bulk operations

The user's primary "usable for bulk operations" requirement is met by letting a
bulk operation **target a group**:

- Add an optional **group reference** field to the bulk target struct
  (`BulkTarget` in `server/internal/model/bulk.go`). Because the target persists
  as `target_json`, this needs **no migration**.
- In `PreviewTargets`, when a group is referenced, resolve it via
  `MachineGroupRepo.Members` and intersect/compose with any other target fields
  (the existing name/OU/group/explicit-ID logic).
- This composes for free with the existing recurring scheduler: an operation in
  `target_mode = filter` that references a **dynamic** group will, on each run,
  call `PreviewTargets`, which re-resolves the group — so a recurring job
  automatically tracks the group's live membership. A **manual** group resolves
  to its current members each run. No scheduler changes.
- The bulk form (`bulk_form.html` / `server/internal/portal/bulk.go`) gains a
  "target a group" choice beside the existing search fields, with the same
  preview-count treatment.

### 9.2 Activity log filtering

The activity log's `actor` is a machine's SMBIOS UUID, and `LogSearch`
(`server/internal/model/logs.go`) currently filters on a **single** actor.

- Extend `LogSearch` with an **`Actors []string`** field and build a
  `WHERE actor IN (...)` clause when it is set (the single-actor path is the
  one-element case). Enrichment of machine names is unchanged.
- The logs page (`server/internal/portal/logs.go`, `logs.html`) gains a **group
  selector**; choosing a group resolves it to member UUIDs via
  `MachineGroupRepo.MemberUUIDs` and passes them as `Actors`. Result: "show me
  everything the machines in the Helsinki lab did."

### 9.3 Other sensible call sites (designed-for, mostly future)

Because every consumer only needs an ID set, these adopt groups trivially when
desired, and are explicitly *out of scope for the first cut* unless cheap:

- **Wake-on-LAN** and **Windows Update deployment targeting** — both already act
  on machine sets; a "pick a group" entry point reuses the resolver.
- **Dashboard** — a per-group count tile.
- **Notifications** — a future "notify when a dynamic group's membership
  changes" event (reserved).

The plan's job is to make the resolver the single integration seam so these need
no new concepts.

---

## 10. Performance and consistency

- **Evaluation cost.** Dynamic resolution loads the full inventory and filters in
  Go, consistent with the existing bulk path and the documented scale target
  (thousands of machines). The Machines sidebar shows a count per group; rendering
  it naively evaluates every dynamic group per page load (N groups × one
  inventory scan). At the stated scale this is acceptable, but the plan calls for
  a single inventory load per request shared across all group counts (load once,
  match many) to keep it linear, not quadratic.
- **Optional caching.** If counts become hot, cache `(group_id → count, members,
  computed_at)` with a short TTL, invalidated on inventory upsert/delete and on
  group-filter edit. Recommended only if profiling shows a need; not built in v1.
- **Consistency.** Dynamic membership is **live by definition** — it reflects the
  inventory at read time. Manual membership is durable until edited. This
  difference is intentional and is the only externally visible behavioral
  distinction between the kinds.

---

## 11. Security and permissions

- All group endpoints sit behind the same `requireAuth` + CSRF (`X-Requested-With`)
  the rest of the portal/API uses. Groups carry no secrets, so the secret-handling
  rules add nothing new.
- Every create/update/delete/membership change is audit-logged (§6), satisfying
  the "who did what" expectation for a new operator-facing object.

---

## 12. Testing plan

Matching the repo's table-driven + render-test conventions:

- **Matcher** (`machinematch_test.go`): table tests for each criterion (name
  regex incl. invalid-pattern error, OS substring, OU exact vs subtree, member-of
  case-insensitive) and AND-combinations; empty-filter behavior per the §13
  decision.
- **Repo** (`machinegroup_test.go`): CRUD; unique-name conflict; manual add/
  remove/set idempotency; cascade on machine delete and on group delete; dynamic
  `Members`/`MemberCount`; `GroupsForMachine`; rejection of member-writes to a
  dynamic group.
- **Bulk** (`bulk_test.go` additions): a bulk target referencing a group resolves
  correctly; a recurring dynamic-group target re-resolves across runs.
- **Logs** (`logs_test.go` additions): `Actors []string` produces the right
  `IN (...)` filtering, single-element included.
- **Portal render** (`machine_list_test.go`, new `machinegroup_render_test.go`):
  sidebar renders groups + counts; `?group=` restricts rows and is preserved
  across sort/pager/CSV links; create form toggles dynamic fields.
- **API** (`api_test.go` additions): the CRUD + members lifecycle, auth/CSRF
  enforcement, and an audit-log entry landing for a group mutation.

A red build blocks "done," per CONVENTIONS §8.

---

## 13. Open decisions (recommended defaults shown)

1. **Empty dynamic filter.** Recommend **reject on save** (require ≥1 criterion);
   "everything" is the existing All-machines view.
2. **Name-match source.** Recommend matching the **displayed** name (reported
   Windows name, binding name as fallback) so the filter matches what the operator
   sees in the table. Alternative: match desired binding name only.
3. **OU matching.** Recommend supporting **both** exact and subtree (a checkbox),
   defaulting to subtree off.
4. **Deleting a group referenced by a bulk operation.** Recommend **refuse with
   `ErrInUse`** (consistent with loadout/mirror delete guards); alternative is to
   null the reference and let the op fall back to its other target fields.
5. **Dynamic membership freshness.** Recommend **always live**; revisit caching
   only under load.
6. **Group nesting.** Out of scope now; schema/resolver leave room to add later.

These are flagged rather than blocking; the plan proceeds on the recommended
defaults unless changed at approval.

---

## 14. File-by-file change set

Create:

- `server/internal/storage/migrations/0030_machine_groups.sql` — two tables + indexes.
- `server/internal/model/machinegroup.go` — `MachineGroupRepo` + resolver.
- `server/internal/model/machinematch.go` — shared `MachineFilter` matcher.
- `server/internal/api/machinegroup_handlers.go` — CRUD + members endpoints.
- `server/internal/portal/machinegroup.go` — group create/edit + membership actions.
- `server/internal/portal/templates/machine_group_form.html` — create/edit form.
- `docs/portal/machine-groups.md` — user guide for the feature.
- Test files: `server/internal/model/machinegroup_test.go`,
  `server/internal/model/machinematch_test.go`,
  `server/internal/portal/machinegroup_render_test.go`.

Modify:

- `server/internal/model/types.go` — `MachineGroup`, `MachineFilter` types.
- `server/internal/model/bulk.go` — `BulkTarget` group reference; `PreviewTargets`
  uses the shared matcher and resolves group targets.
- `server/internal/model/logs.go` — `LogSearch.Actors []string` + `IN (...)`.
- `server/internal/api/api.go` — `Repos.Groups` field + route registration.
- `server/cmd/autodeploy-server/main.go` — construct/inject the repo.
- `server/internal/notify/events.go` — reserved group event constants (optional).
- `server/internal/portal/portal.go` — register group + membership routes.
- `server/internal/portal/inventory.go` — sidebar data, `?group=` filter, param
  threading, membership actions.
- `server/internal/portal/logs.go` — group selector → `Actors`.
- `server/internal/portal/bulk.go` — target-a-group option.
- `server/internal/portal/templates/machine_list.html` — two-pane layout + sidebar.
- `server/internal/portal/templates/logs.html` — group selector.
- `server/internal/portal/templates/bulk_form.html` — group target field.
- `server/internal/portal/templates/_layout.html` / `_icons.html` — optional nav +
  kind icons.
- `server/internal/portal/assets/style.css` — two-pane + sidebar styles.
- `server/internal/portal/assets/app.js` — create-form kind toggle + preview count.
- `docs/portal/machines.md`, `docs/portal/bulk-operations.md`,
  `docs/portal/logs.md`, `docs/reference/api.md`, `docs/design/WORKLOG.md` —
  documentation.

---

## 15. Phasing (incremental, each independently shippable & green)

1. **Model + migration + matcher.** Tables, `MachineGroupRepo`, shared matcher,
   bulk refactor to the shared matcher (no behavior change). Tests.
2. **API.** CRUD + members endpoints, audit logging, api docs. Tests.
3. **Machines page.** Sidebar, `?group=` filtering + param threading, manual
   membership from the selection, create/edit form. Render tests + user docs.
4. **Integrations.** Bulk target-a-group; logs `Actors[]` + group selector;
   machine-detail "Groups" line. Tests + docs.
5. **Polish (optional).** Top-nav entry, dashboard tile, count caching if needed,
   reserved notification event.

Each phase leaves the build green and the feature usable up to that point;
Phase 3 is the first point at which the user-visible requirement
("groups on the left, click to filter") is fully met.
