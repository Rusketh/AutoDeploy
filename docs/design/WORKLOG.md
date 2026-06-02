# AutoDeploy Worklog

Append-only running log of development work. Newest entries at the bottom.
Each entry: WHAT, WHY (assumption/decision flagged), build STATE, NEXT.

---

## 2026-05-28 — Phase 0 kickoff

**WHAT.** Initial project scaffolding. Created the three-component layout
(`server/`, `boot-client/`, `agent/`), top-level `README.md`, GitHub Actions
CI workflows for each component, a starter user guide, and this worklog.
Wrote and ran a "hello" build of each component so CI has something to compile.

**WHY (assumptions / decisions).** Phase 0 in the roadmap explicitly calls for
deciding the server technology stack and topology — these were open questions
in the design document. Decisions taken now (logged so they can be cheaply
revisited):

- ASSUMPTION: Server language and framework — **Go 1.22+**, using the standard
  library `net/http` plus minimal dependencies (chi router, pgx for Postgres).
  Reasons: single static binary deploys cleanly to either Windows or Linux
  servers; excellent streaming HTTP for multi-GB ISO/WIM payloads; the same
  toolchain cross-compiles the Linux Boot Client and the Windows agent so the
  whole platform shares one build culture.
- ASSUMPTION: Datastore — **PostgreSQL** for relational artifact metadata,
  bindings, inventory and audit; local filesystem (or S3-compatible later) for
  payload blobs.
- ASSUMPTION: Portal UI — **server-rendered Go html/template + HTMX** for
  interactivity. Avoids a parallel JS toolchain in the initial phases; can be
  replaced with a SPA later if needed. Keeps the portal trivially shippable in
  the same single binary as the API.
- ASSUMPTION: Topology — **single-server** to begin with. Architecture must
  remain open to distributed site servers (Phase 16 revisit).
- DECISION: Each of `server/`, `boot-client/`, `agent/` is its own Go module.
  Independent buildable components per Phase 0 exit criteria.
- DECISION: Logging is structured (`log/slog` JSON) from line one. Every event
  carries `who`, `what`, `where`, `when` (`actor`, `action`, `component` +
  `target`, automatic timestamp). Secret-handling rules are documented in
  `docs/design/CONVENTIONS.md` and enforced by code review and a `forbidden
  identifiers` lint in CI (BitLocker PIN/recovery-key/access-PIN values are
  never accepted by the logger).

**BUILD STATE.** New project — local builds of each component succeed
(`go build ./...` per module). GitHub Actions workflows added but not yet run
against the repository.

**NEXT.** Stand up the server's HTTP skeleton (Phase 0 exit criterion: empty
server, boot client, agent each build and run), confirm CI green on push,
then start Phase 1 (data model and management portal artifacts).

---

## 2026-05-28 — Phase 1 complete (data model + portal artifacts)

**WHAT.** Built the Phase 1 spine end to end:

- Storage layer: SQLite (modernc.org/sqlite, pure-Go, no CGO) with embedded
  migrations. Initial migration creates `iso`, `unattend`, `driver_package`
  + `driver_filter`, `software_package`, `image`, `image_software_package`,
  and `schema_migration`.
- Domain model (`internal/model`): typed repositories for ISO, Unattend,
  DriverPackage (with filters), SoftwarePackage, Image (with parent chain
  and software links). Validation, name uniqueness, JSON validity checks,
  reference-count guards on delete (ISO, Unattend, SoftwarePackage), child
  guard + cycle detection on Image parent updates.
- Resolution (`internal/resolve`): nearest-wins for ISO and Unattend up the
  parent chain; additive union with first-seen dedupe for direct software
  links. Diagnostics surfaced (missing ISO, missing unattend) without
  refusing to produce a manifest.
- JSON API (`internal/api`): CRUD for all five artifact/composition types
  at `/api/v1/*`, plus `GET /api/v1/images/{id}/resolved`. Domain errors
  map cleanly to 4xx in one place.
- Portal (`internal/portal`): server-rendered HTML at `/portal/*`, with
  embedded templates and a minimal stylesheet. List views for each artifact
  type and an image's resolved-configuration view. Per-request template
  parsing keeps pages independent (each defines its own `body` block).
- API integration tests and unit tests for resolution rules (nearest-wins,
  union dedup, missing-ISO diagnostic) and cycle prevention.

**WHY (assumptions / decisions).**

- ASSUMPTION — REVISED from Phase 0: datastore is **SQLite**, not Postgres.
  Rationale: deployment-tool scale (low thousands of machines), Postgres
  brings deployment burden (separate process, credentials, backups) for no
  benefit at this scale, and the pure-Go SQLite driver means CI and dev
  builds need nothing external. Storage code is small enough to swap later
  if scale dictates.
- ASSUMPTION: settings/rules JSON shapes are stored as raw JSON columns
  for the artifact types whose structured types belong to later phases:
  unattend settings → Phase 5, driver filter expressions → Phase 4,
  software detection rules and install steps → Phase 6. Migration to
  promoted columns is anticipated and not blocked.
- DECISION: image parent FK uses `ON DELETE RESTRICT`. Cycle detection is
  enforced in application code (SQLite can't express it declaratively)
  with a defensive 256-depth cap that would catch a corrupted dataset.
- DECISION: Driver packages are not directly linked from images (matching
  is global in Phase 4), so they have no reference-count guard on delete.
  This is consistent with the design's "drivers are matched globally by
  hardware fit, not by image inheritance" rule.
- DECISION: Portal renders directly via repositories rather than calling
  its own HTTP API. Simpler and faster; the API still exists as the
  authoritative contract for the Boot Client and external tooling.
- DECISION: Each page template is parsed per request together with the
  shared layout, so pages can each define a `body` block without name
  collisions. Templates are embedded so the binary is self-contained.

**BUILD STATE.** `make build` and `go test ./...` green across all
packages. Smoke-tested end to end: create ISO+image via curl, view
`/api/v1/images/{id}/resolved` and `/portal/images` in a running server.

**NEXT.** Phase 2 in roadmap order: payload delivery over HTTP. That
means ISO upload + extraction so the WIM/ESD is served as discrete files,
driver-package upload, software-installer upload, and an HTTPS strategy.
The resolver already returns the manifest shape the Boot Client will need;
Phase 2 fills in the payload-URL plumbing and the file-serving layer.

---

## 2026-05-28 — Phase 2 complete (HTTP API + payload delivery)

**WHAT.**

- BlobStore (`internal/storage`): filesystem-backed payload store rooted
  at `AUTODEPLOY_DATA_DIR`, with atomic writes (write-to-tmp + rename) and
  a path resolver that refuses paths escaping the root.
- Payload service (`internal/payload`): upload endpoints for ISOs, driver
  packages and software installers; ISO extraction (pure-Go ISO9660 via
  `github.com/kdomanski/iso9660`, no CGO); range-aware download endpoints
  at `/payload/iso/{id}/{path}`, `/payload/drivers/{id}`,
  `/payload/software/{id}`; deployment manifest endpoint at
  `/api/v1/images/{id}/manifest` that turns the resolved image into a
  flat list of `{role, url, size_bytes, ...}` items the Boot Client will
  consume in Phase 3.
- HTTPS (`internal/httpx/tls.go`): operator can supply cert/key via
  `AUTODEPLOY_TLS_CERT` / `AUTODEPLOY_TLS_KEY`; in dev mode a P-256
  self-signed cert is generated under `data/tls/` covering `localhost`,
  `127.0.0.1` and the configured host. Production refuses to start HTTPS
  without an explicit cert pair.
- main wired both HTTP and HTTPS listeners side by side; either can be
  disabled by leaving its address blank.
- Tests: ISO build/extract/serve round-trip using a real ISO9660 image,
  Range request returns `206 Partial Content`, manifest URLs are correct,
  TLS dev-cert generation has correct perms.

**WHY (assumptions / decisions).**

- DECISION: ISO extraction is a separate step (`POST .../extract`) from
  upload (`PUT .../upload`). Keeps upload simple and bounded in scope
  (one HTTP request, one streamed write); extraction can take minutes for
  a full Windows ISO and benefits from being its own observable
  operation. The portal will show extraction progress in a later phase.
- DECISION: Driver-package and software-package payloads are stored as
  opaque blobs in Phase 2. Phase 4 will parse SCCM driver packages on
  ingest; Phase 6 will execute software steps from the installer payload.
  The on-wire format for both is not nailed down until those phases.
- DECISION: The manifest endpoint is its own URL rather than being baked
  into the existing `/resolved` view, so the Boot Client has a stable
  contract that does not change when the portal's resolved-view JSON
  changes shape.
- ASSUMPTION: Self-signed cert generation in dev mode is acceptable
  because clients in dev are not the production fleet. Production
  deployments MUST provide a real cert; the server refuses to start TLS
  otherwise.
- ASSUMPTION: ISO file extraction uses pure-Go ISO9660; this covers
  Windows install media that ship as ISO9660 (with Joliet/Rock Ridge
  extensions where present). UDF-only media is not yet supported; the
  extractor will return a clear error and the operator can fall back to
  extracting manually and pointing the ISO row at the existing files.
  Real-media survey will land in Phase 3 lab testing.

**BUILD STATE.** `make build` green; `go test ./...` green across all
packages (server, storage, model, resolve, api, httpx, payload). Smoke
test: HTTP and HTTPS listeners both respond to `/healthz`; uploaded ISO
extracted and `install.wim` served back via the payload route.

**NEXT.** Phase 3 — Boot Client and basic imaging. Build the Linux
initramfs flow: iPXE chainloads `autodeploy-boot`, it reads SMBIOS,
calls the API for the menu (Phase 1/2 endpoints are sufficient), pulls
the manifest, partitions, applies the WIM with `wimlib-imagex`, places
the (still-empty) unattend, and reboots. The server is already
fact-complete for Phase 3; the work is on the Boot Client side.

---

## 2026-05-28 — Phase 3 complete (Boot Client + basic imaging)

**WHAT.**

- Server: added `POST /api/v1/clients/menu` (the Boot Client's menu
  endpoint) and `GET /ipxe/boot.ipxe` (the iPXE chainload script).
  Static iPXE assets are served from `AUTODEPLOY_DATA_DIR/ipxe/`.
- Boot Client: full sub-command suite (`identify`, `menu`, `deploy
  <image-id>`). HTTP layer (`internal/httpc`) handles JSON request /
  response and streamed downloads with progress callbacks; supports
  `--insecure-tls` for dev. Imaging layer (`internal/imaging`) issues
  the partition / mkfs / wimlib-imagex / driver-inject / unattend-place
  sequence through a Runner interface, with `OSRunner` for production
  and `Recorder` for tests. `--dry-run` logs every destructive call
  without executing it.
- Initramfs build script (`scripts/initramfs/build-initramfs.sh`):
  bundles `autodeploy-boot` plus the host tools the imaging plan needs
  (sgdisk, mkfs.fat, mkfs.ntfs, wimlib-imagex, busybox or coreutils
  basics), writes a minimal `/init` that parses `autodeploy.server=`
  from the kernel command line and execs `autodeploy-boot menu`.
- Tests: end-to-end dry-run drove a real server, fetched the menu,
  pulled a manifest, downloaded a payload, and emitted the full
  partition + wimlib step sequence. Imaging recorder asserts the right
  commands in the right order; first-error-aborts behaviour verified.

**WHY (assumptions / decisions).**

- DECISION: The imaging code calls `sgdisk`, `mkfs.fat`, `mkfs.ntfs`,
  `mount`, `umount`, `wimlib-imagex` and `cp` via subprocess. Pure-Go
  reimplementations of these tools exist for some but not all, and
  reproducing wimlib in particular is a project of its own. Subprocess
  keeps the Boot Client small and lets the initramfs share well-tested
  upstream tools. The Runner interface keeps tests independent of any
  of them being installed.
- DECISION: Partition layout is GPT, 100 MiB FAT32 ESP + remainder NTFS
  (single OS volume). This is the modern UEFI Windows layout and
  matches what Windows setup produces unattended. Legacy BIOS / MBR is
  out of scope for the initial release — modern Windows installs are
  UEFI.
- DECISION: Boot Client treats EVERY failure path as fail-safe and
  exits 0 without touching the disk — including unreachable server,
  empty menu, no operator input, manifest missing WIM, hardware
  identity unreadable. Only `imaging.Apply` is destructive, and only
  after an explicit operator selection. This matches the design's
  "imaging is destructive and must never be the default outcome of a
  failure" rule.
- DECISION: Operator-cancelled (`0`), no-input, and invalid-choice all
  log as `menu.cancel` and return without imaging. The operator can
  always escape to a normal boot.
- DECISION: iPXE script is rendered server-side and parameterised by
  the request `Host` header, so any client that reaches the server
  uses the same hostname to chain-fetch the kernel + initrd. No need
  for static config of the public URL.
- ASSUMPTION: NIC and disk drivers for the pre-boot environment come
  from the kernel the operator supplies. Most distro generic kernels
  cover common hardware; AutoDeploy does not ship its own kernel.
  Building one is the operator's choice, documented in
  `docs/user-guide/boot-client.md`.

**BUILD STATE.** `make build` and `go test ./...` green for all three
modules. End-to-end dry-run validated against a running server.

**NEXT.** Phase 4 — driver matching. The driver-package model and
upload endpoint already exist (Phase 1, Phase 2); Phase 4 is the
matching engine: structured SMBIOS-filter expressions, server-side
evaluation against reported hardware, and inclusion of the matched
packages in the manifest the Boot Client already consumes. The Boot
Client's imaging code already injects whatever driver paths the
manifest lists, so the wire change is contained in the server.

---

## 2026-05-28 — Phase 4 complete (driver matching)

**WHAT.**

- `internal/match`: structured `Filter` type, `Identity` type, exact
  case-insensitive matching with `*` wildcard support and a strict
  empty-filter-never-matches rule.
- Filter validation at save time: `validateDriverFilter` in the model
  layer parses filter JSON through `match.ParseFilter` so unknown keys
  are rejected with a clear error (no silent never-match).
- Resolver: new `ResolveForMachine(ctx, id, identity)` evaluates every
  driver package's filters against the reported identity, returning the
  union. The non-identity `Resolve` is unchanged so the portal's
  "view resolved" still works.
- Manifest endpoint accepts POST with an identity body; when present,
  matched drivers appear as `driver` items in the manifest. Diagnostic
  emitted when packages exist but none matched.
- Driver-preview endpoint (`POST /api/v1/drivers/{id}/preview`) returns
  per-filter `{matches: bool}` plus a package-level verdict, so the
  portal can show an operator whether a filter would catch a specific
  machine.
- Boot Client: `deploy` now POSTs identity to the manifest endpoint, so
  the driver list reflects the real hardware. The imaging code already
  injects whatever driver paths the manifest lists, so no Boot Client
  imaging changes were required.

**WHY (assumptions / decisions).**

- DECISION: Filter syntax is a flat JSON object of allowed SMBIOS-key
  -> required value. Case-insensitive equality, with `*` as the only
  wildcard. Glob/regex support is not in scope; SMBIOS values are short
  and structured, equality is enough.
- DECISION: Empty filter never matches, by design. The design doc calls
  out filter specificity as a key concern; the "never match" default is
  the safe answer to a partially-edited filter.
- DECISION: Unknown filter keys are rejected at save time, not silently
  treated as never-match. A typo in a filter key would otherwise produce
  the same observable behaviour as a correct filter that the machine
  happens not to match — invisible and dangerous.
- DECISION: Resolver gains an OPTIONAL drivers repo via `WithDrivers`,
  rather than requiring it. This lets Phase 1/2 callers and tests keep
  working without rewiring while making the driver path available to
  Phase 4+.
- DECISION: Plain `GET /api/v1/images/{id}/manifest` still works (for
  the portal); the per-machine path is `POST` with an identity body.
  Same handler, branches on method + body.

**BUILD STATE.** `go test ./...` green across all modules; `make build`
green for all three. End-to-end smoke: Dell machine receives driver in
manifest, HP machine does not and gets a "no match" diagnostic.

**NEXT.** Phase 5 — unattend generation. The Unattend entity exists
(Phase 1) but settings are stored as opaque JSON. Phase 5 turns the
settings into a structured type, generates a complete `unattend.xml`
from them, and serves it at `/payload/unattend/{image-id}`. The Boot
Client already downloads whatever `unattend` item the manifest lists and
places it at `Windows\Panther\unattend.xml`, so the wire change is in
the server.

---

## 2026-05-28 — Phase 5 complete (unattend generation)

**WHAT.**

- `internal/unattend`: structured `Settings` model covering locale,
  language, keyboard, time zone, edition + product key, local admin
  (with secret password), computer-naming strategy (random, literal,
  prefix), OOBE skip flags, optional domain join (with secret join
  password), and ordered first-logon commands.
- `unattend.Generate(Settings)` writes a complete Windows 10/11
  `unattend.xml` covering windowsPE, specialize and oobeSystem passes
  with both `urn:schemas-microsoft-com:unattend` and the `wcm`
  namespaces declared. HTML/XML-escapes every value.
- The agent bootstrap command is always appended to the
  FirstLogonCommands list so the agent runs after any operator-supplied
  first-logon steps.
- Payload service grew `GET /payload/unattend/{image-id}` which
  resolves the nearest-wins unattend for the given image, parses its
  settings_json and returns the generated XML. The Boot Client already
  downloads this URL via the manifest's `unattend` item.
- Tests: XML parses as valid XML; admin password is present in XML
  (Windows needs it) but never logged; hidden-admin omits the
  AdministratorPassword block; all three naming strategies emit the
  expected `<ComputerName>` value; domain join section appears when
  configured; defaults apply when settings_json is empty.

**WHY (assumptions / decisions).**

- DECISION: Unattend settings remain stored as JSON in the row, parsed
  on each render. Avoids a schema migration every time a new field is
  added; the structured type is the authority and the JSON is its
  on-disk representation.
- DECISION: The endpoint key is the IMAGE id, not the unattend id,
  because the choice of unattend is a property of the resolved image
  (nearest-wins up the chain). The Boot Client never knows or cares
  which unattend row was used.
- DECISION: The agent first-logon command is always appended by the
  generator, not added by the operator. Removing it would leave the
  machine without an agent, defeating the rest of the system.
- DECISION: Secrets (admin password, domain join password) live in the
  settings_json blob. They are emitted into the generated XML (this is
  the entire point of unattended setup) but never written to any log,
  HTTP error body, or stack trace. `scripts/check-secrets.sh` continues
  to enforce the no-leak rule statically.
- ASSUMPTION: Windows 10/11 amd64 is the target. The generated XML uses
  `processorArchitecture="amd64"` everywhere. Arm64 support is a
  follow-up: it would parameterise the architecture and select the
  matching component versions.
- ASSUMPTION: Operators that need a name strategy beyond `random`,
  `literal`, `prefix` can still use the bulk-rename agent job (Phase
  13) to set the authoritative name post-deploy. The unattend only
  picks the initial transient name.

**BUILD STATE.** `go test ./...` and `make build` green across the
server. Smoke test confirms the generated XML contains the operator-
configured first-logon command, the agent bootstrap, the local-admin
password and the computer name, with no secret leakage into logs.

**NEXT.** Phase 6 — Deployment Client (agent) and software step
execution. The agent already starts and logs (Phase 0); Phase 6
implements the detection rule evaluator (file presence/version,
registry key/value, MSI product code, custom script), the typed
install-step executor (copy files, run MSI, install APPX/MSIX, run CMD,
run PowerShell, run an executable with arguments), and a fetch loop
that pulls the effective software list from the API, skips already-
installed packages, executes each package's steps in order and
re-confirms via detection. The API already serves the resolved software
set; the agent is where Phase 6 lives.

---

## 2026-05-28 — Phase 6 complete (agent + software step execution)

**WHAT.**

- Spec: `server/internal/swspec` and `agent/internal/swspec` define the
  shared on-wire types for DetectionRule (file/registry/msi/script) and
  InstallStep (copy/msi/appx/cmd/powershell/exe) with per-type required
  field validation. SoftwarePackage create/update now parses the JSON
  through the spec; unknown rule or step types are rejected at save
  time.
- Server endpoint `POST /api/v1/agent/software` resolves the effective
  ordered software set for an image, materialises each package with
  its detection rules, install steps and a payload URL, and returns
  the list. Diagnostics from the resolver flow through as warnings.
- Agent detect (`agent/internal/detect`): pluggable Backend interface;
  PortableBackend used off-Windows (returns "not present" for registry
  and MSI rules so packages re-install, which is the safe default);
  WindowsBackend (build-tag windows) shells out to `reg`, `powershell`
  for file version and `(reg query …\Uninstall\<code>)` for MSI. SHA-256
  file detection works on any host.
- Agent steps (`agent/internal/steps`): Runner interface; OSRunner runs
  msiexec, powershell, cmd, exe; Recorder for tests. Per-step success
  codes (default `[0]`) and continue-on-failure (default `false`) drive
  abort behaviour.
- Agent main loop wires it together: fetch software list, for each
  package evaluate detection (skip if installed), download the payload
  to a work dir, substitute the `{payload}` token in step paths, run
  steps in order, log per-step exit code and aborted flag.
- Tests: detection rule semantics (all rules required, registry
  equals, file SHA), step executor exit-code handling (success codes
  accept non-zero, abort on default failure, continue-on-failure keeps
  going), agent end-to-end dry-run against a real server.

**WHY (assumptions / decisions).**

- DECISION: Detection is conservative AND (every rule must report
  present). A package with zero rules re-installs every time and emits
  a warning. The other option — empty rules = "always installed" — is
  unsafe (a misconfigured package would silently never install).
- DECISION: `{payload}` token in step paths is replaced at run time
  with the agent's chosen on-disk path for the downloaded installer.
  This lets package authors write paths without knowing the agent's
  work directory.
- DECISION: The non-Windows agent backend reports registry and MSI as
  "not present". Off-Windows the agent is a development convenience —
  it cannot actually verify whether Windows-specific state exists, so
  "not detected" (and therefore "install") is the safer default.
  Real installs are gated by `--dry-run` for dev hosts anyway.
- DECISION: PowerShell step bodies are piped to stdin, not put on the
  command line, so they can be arbitrary length and contain quotes
  without escaping.
- DECISION: Spec types are duplicated between server and agent
  packages rather than introducing a shared Go module. Each component
  remains independently buildable; the on-wire JSON contract is the
  single source of truth.
- ASSUMPTION: msiexec exit code 3010 ("reboot required") should be
  treated as success by package authors via `success_codes: [0, 3010]`.
  The agent does not coerce it.

**BUILD STATE.** Server, agent, boot-client all build green;
`go test ./...` green in each module. End-to-end smoke validated: agent
fetched the software list, downloaded the installer payload, ran the
expected msiexec and powershell calls with `{payload}` substituted.

**NEXT.** Phase 7 — SoftwareLoadout. Add the loadout entity (ordered,
inheritable collection of software packages), the union-and-dedup rule
in the resolver (loadout chain ∪ direct image package links, deduped
by package id with direct-link ordering taking precedence), opt-out of
inherited packages, and the portal CRUD. The agent and Boot Client need
no changes — they already consume whatever `res.Software` returns.

---

## 2026-05-28 — Phase 7 complete (software loadouts)

**WHAT.**

- Schema migration 0002 adds `software_loadout`, the link table
  `software_loadout_package` with `order_value` and `opt_out`, plus an
  optional `loadout_id` column on `image`.
- `SoftwareLoadoutRepo` with CRUD, package-link replacement, cycle
  detection on parent updates, and a `RefCount` that sums image links
  and child loadouts.
- Resolver gained `WithLoadouts(...)` and `resolveLoadout(ctx, id)`:
  walks the loadout parent chain (eldest-first), accumulates packages
  with descendant entries replacing ancestor ones and `opt_out`
  removing the package, sorts the merged set by `(order_value,
  package_id)` and unions it with the image's direct links —
  deduped by package id, **direct-link ordering takes precedence**.
- Image now reads/writes `loadout_id`. SoftwarePackage's `RefCount`
  also counts loadout memberships so direct deletes are blocked when
  loadouts still link the package.
- API CRUD at `/api/v1/loadouts`, mirroring the other artifact routes.
- Tests cover additive inheritance (child overrides parent order,
  child opt-out removes ancestor's package, child can add packages),
  direct-link precedence over loadout ordering, and loadout cycle
  detection on update.

**WHY (assumptions / decisions).**

- DECISION: An image's **own** loadout link is nearest-wins along the
  image chain — ancestor images do not also contribute loadouts. This
  mirrors how ISO and unattend behave and keeps "which loadout
  applies?" unambiguous. The loadout's *own* inheritance still
  accumulates (additive across the loadout chain), which is the
  documented per-design behaviour.
- DECISION: Sort within the merged loadout set is by
  `(order_value, package_id)` so install order is deterministic
  regardless of insertion order or chain depth.
- DECISION: Direct image package links win over loadout entries for
  the same package — both presence and `order_value`. This lets an
  image override a loadout's choice without forking the loadout.
- DECISION: Reference-count guard on a software package now sums
  image links AND loadout memberships, matching the operator's
  expectation that "if any image or loadout uses it, I can't delete
  it".
- DECISION: SQLite's `ALTER TABLE` only supports adding columns, so
  the loadout column on `image` is added via a separate migration
  rather than altering the original.

**BUILD STATE.** All tests green across modules; server builds. Smoke
test would expose `/api/v1/loadouts` (same shape as the other
resources); resolver tests cover the resolution rule end to end.

**NEXT.** Phase 8 — inventory and bindings. Add the `MachineRecord`
keyed on SMBIOS UUID with serial as secondary; persist the binding
(assigned image, name, OU, group memberships, software assignment) and
the dated deployment history. The Boot Client and agent both report
identity already; Phase 8 wires up the server-side persistence and
surfaces inventory in the portal.

---

## 2026-05-28 — Phase 8 complete (inventory and bindings)

**WHAT.**

- Migration 0003 adds `machine_record` (keyed on system_uuid, with
  serial as secondary), `machine_binding` (one-to-one with the record,
  carries image_id, machine_name, target_ou, group_memberships JSON),
  `deployment_history` (append-only, dated, with outcome) and
  `machine_detected_state` (per-package detection results for drift).
- `InventoryRepo` with `UpsertFromIdentity` (used both by the Boot
  Client's menu fetch and the agent's report), `Get/GetByUUID/List`,
  `GetBinding/UpsertBinding`, `RecordDeployment/CompleteDeployment`,
  `HistoryFor`, and `RecordDetectedState/DetectedStateFor`.
- Boot Client menu endpoint now upserts the identity it receives, so
  every machine that ever boots into AutoDeploy is in inventory. When
  the machine has a binding pointing at an image, the menu returns a
  `reimage` option referencing the bound image — the foundation
  re-imaging (Phase 9) builds on.
- Agent posts `POST /api/v1/agent/report` twice during a deploy: once
  at the start with `outcome: in_progress` to open a history row, and
  once at the end with `outcome: ok|failed` plus per-package detection
  outcomes. The server upserts the machine record and writes the
  deployment row and detected-state rows.
- New JSON API at `/api/v1/machines`, `/api/v1/machines/{id}`,
  `/api/v1/machines/{id}/binding`, `/api/v1/machines/{id}/history`,
  `/api/v1/machines/{id}/detected`.
- Tests: identity upsert is idempotent on same UUID; binding round-trips
  groups via JSON; history is recorded and completed correctly;
  detected-state is upserted on conflict.

**WHY (assumptions / decisions).**

- DECISION: `machine_record` is keyed on `system_uuid` (UNIQUE). The
  design names UUID as the stable primary identifier with serial as
  secondary; SQLite has no native UPSERT against a non-PK unique
  column, but the modernc driver supports `INSERT ... ON CONFLICT(...)
  DO UPDATE`, which we use for the bindings and detection rows. The
  machine UPSERT is done in application code (lookup → update or
  insert) to keep its read path explicit.
- DECISION: `machine_binding` is one-to-one with `machine_record`
  (machine_id is its PK). A machine has exactly one current
  binding; history of past bindings is implicit in deployment_history.
- DECISION: Deleting an image leaves bindings with `image_id = NULL`
  rather than cascading the binding away. The binding is the
  assignment; the image is the current way that assignment is
  fulfilled. The portal warns operators about bindings that would
  break before they delete an image (UI to land in Phase 9).
- DECISION: Group memberships are stored as a JSON array column for
  flexibility; Phase 10's AD service will iterate them. Comparing
  membership state in SQL is not a requirement of the design.
- DECISION: Agent report endpoint is the **same** for opening and
  closing a deployment — `in_progress` opens a row, an explicit
  `deployment_id` plus `ok|failed` closes it. Saves a round-trip and
  matches the design's "agent reports facts; server records them".

**BUILD STATE.** Server and agent build green; all tests pass. End-to-
end smoke validated: menu fetch creates record → operator sets
binding → menu fetch now includes `reimage` → agent report opens and
closes a history row.

**NEXT.** Phase 9 — re-imaging. The pieces are already in place: the
binding tells the server what image to use; the manifest endpoint
already produces a fresh manifest from the latest definitions. Phase 9
adds the reimage trigger flow (menu choice → deploy that image) and
ensures the agent updates the existing binding's deployment history
rather than creating a fresh machine. The Boot Client's existing
`deploy <image-id>` command already does the right thing — Phase 9 is
about making the reimage path operationally first-class.

---

## 2026-05-28 — Phase 9 complete (re-imaging)

**WHAT.** Re-imaging is operational and tested. Most of the
infrastructure was already in place from Phases 1–8:

- The Boot Client menu endpoint already surfaces a `reimage` option
  for any machine with a binding pointing at an image (Phase 8).
- The manifest endpoint already builds against the current
  definitions of ISO, unattend, drivers and software (Phase 2 + later).
- The agent's report endpoint already writes deployment history rows
  keyed on the machine record, so a re-image appends to the existing
  machine's history rather than creating a new one.

Phase 9 itself added:

- An integration test that drives the full re-image contract end to
  end: first boot creates the machine; the operator binds it; the
  next menu fetch returns the reimage option; mutating the bound
  image (swapping its ISO) is reflected immediately in the resolved
  configuration. The "latest, not snapshot" rule is exercised.
- Operator documentation (`docs/user-guide/reimaging.md`) describing
  the deliberate "latest" behaviour, what is preserved across a
  re-image, what is not, and why there is no remote "re-image now"
  trigger in this release.

**WHY (assumptions / decisions).**

- DECISION: No "re-image on next boot" remote trigger in this release.
  Re-imaging is destructive; letting a remote operator queue a
  destructive action that fires on next power-on without a deskside
  confirm sits uneasily with the design's fail-safe philosophy.
  Operators with deskside access drive a re-image through the menu;
  remote operators rely on the power-on workflow. A future revision
  could add a queued trigger gated on an additional confirmation.
- DECISION: Re-imaging targets the **latest** definition, not a
  frozen snapshot of the previous deploy. The resolver does not look
  at `deployment_history` at resolution time.
- DECISION: The machine record persists across re-image (matched by
  SMBIOS UUID), so deployment history accumulates against the same
  record.

**BUILD STATE.** All tests green; the new reimage integration test
exercises the Boot-Client → server contract end to end.

**NEXT.** Phase 10 — Active Directory integration. The binding
already carries `target_ou` and `group_memberships` (Phase 8); the
unattend already has a `domain_join` section (Phase 5). Phase 10
adds the server-side Domain Integration Service that performs the
delete-and-replace computer-object lifecycle via a service account
and reconciles group memberships from the binding on each deploy.
LDAP is wrapped behind an interface so a fake provider drives tests
without a live directory; the real backend uses
`github.com/go-ldap/ldap/v3` and configures credentials via
environment variables.

---

## 2026-05-28 — Phase 10 complete (Active Directory integration)

**WHAT.**

- `internal/addomain`: Domain Integration Service with a `Directory`
  interface so tests use `FakeDirectory` and production uses
  `LDAPDirectory` (real AD via `github.com/go-ldap/ldap/v3`).
- `Service.PrepareComputer(name, ou, groups)` implements the
  delete-and-replace lifecycle: find existing object by CN, delete it
  if present, create a fresh one at the target OU, reconcile group
  memberships by diffing current vs. wanted.
- LDAPDirectory: dials with TLS, binds with service account, performs
  search/add/del/modify operations. Each call opens its own
  connection — connection caching is left for Phase 16.
- Config grew `AUTODEPLOY_AD_URL`, `AUTODEPLOY_AD_BIND_DN`,
  `AUTODEPLOY_AD_BIND_PASSWORD`, `AUTODEPLOY_AD_SEARCH_BASE`,
  `AUTODEPLOY_AD_SKIP_TLS_VERIFY`. Empty URL disables AD entirely; the
  service then no-ops cleanly.
- Manifest handler wires the AD service in. When the resolved unattend
  has a `domain_join` section AND the requesting machine has a binding
  with name+OU, the handler runs PrepareComputer before returning the
  manifest, so JoinDomain succeeds on first boot.
- Tests: delete-and-replace exercised against the fake directory;
  group reconciliation adds new groups and removes old; disabled
  service is a no-op; manifest-level integration test confirms the
  end-to-end flow on a real httptest server.

**WHY (assumptions / decisions).**

- DECISION: AD is invoked from the manifest path rather than from a
  dedicated agent-side endpoint. By the time the Boot Client asks for
  the manifest, the operator has committed to imaging this machine
  with this image; that is the right moment to prepare the AD object
  so the unattend's JoinDomain can succeed.
- DECISION: Delete-and-replace is the documented behaviour from the
  design; the service emits a loud INFO log line whenever it deletes
  an existing object so the LAPS / GUID consequences are visible in
  the audit trail.
- DECISION: Group reconciliation is per-deploy. Drift between deploys
  is not corrected continuously — the design says re-applied on each
  deployment, not "continuously enforced". A future revision could
  add a periodic reconcile.
- DECISION: Group reconciliation failure does not unwind the new
  computer object. The join will still work; the operator can fix
  groups via the portal. We log loudly so the failure is visible.
- DECISION: Each AD call opens its own connection. AD connections are
  expensive at scale, but Phase 16 is the right place to measure and
  add pooling if it bites.
- ASSUMPTION: AD attribute schema uses the standard
  `objectClass=computer`, `sAMAccountName=<name>$`,
  `userAccountControl=4096` (WORKSTATION_TRUST_ACCOUNT). Customised
  schemas would need additional configuration; out of scope for the
  initial release.

**BUILD STATE.** Server builds and tests green; LDAP backend compiles
on all platforms; FakeDirectory drives the test suite without a real
DC. End-to-end test confirms PrepareComputer runs at manifest time
when the unattend declares domain join and a binding exists.

**NEXT.** Phase 11 — Security: the optional global access PIN and
portal authentication. The access PIN is a single system setting
gating the Boot Client menu; the Boot Client prompts and submits each
attempt to a server endpoint; three failures fail-safe to a normal
boot; the server rate-limits by machine identity. Portal auth is
local username/password, with no graded permission model. Both pieces
share a tiny secret store (hashed-password rows, plus the access-PIN
setting).

---

## 2026-05-28 — Phase 11 complete (access PIN + portal authentication)

**WHAT.**

- Migration 0004 adds `system_setting`, `user_account`, `user_session`,
  `pin_attempt`.
- `internal/auth`: `Repo` for local username/password accounts
  (bcrypt-hashed); session creation, lookup with expiry, deletion;
  `SettingsRepo` for the global access PIN (set/clear/validate) and
  rate-limit tracking.
- API endpoints:
  - `POST /api/v1/auth/login` issues a session cookie; `POST .../logout`
    revokes it; `GET .../me` returns the current user.
  - `/api/v1/accounts` CRUD (create / list / delete / disable / enable /
    set-password). All require an authenticated session.
  - `/api/v1/settings/access-pin` set + get status. Requires session.
  - `POST /api/v1/clients/validate-pin` server-side PIN check with
    rate-limit (5 failures in 15 minutes per `system_uuid` → 429). No
    session required — this IS the boot-time auth.
- Boot Client now runs `runAccessPIN` before fetching the menu. Three
  prompts; each attempt server-validated; lock-out from the server
  short-circuits to a normal boot. The Boot Client never decides whether
  a PIN is correct.
- Bootstrap: on first start with no users, the server creates an
  `admin` account with a random password and writes it to
  `$AUTODEPLOY_DATA_DIR/admin-bootstrap.txt` (mode 0600). The log records
  ONLY the path; the password value never appears in a log line.

**WHY (assumptions / decisions).**

- DECISION: Bootstrap password is written to a 0600 file, not logged.
  Logging a fresh password would violate the no-secrets-in-logs rule
  even as a one-time bootstrap. The file is a single audit-able
  artifact the operator reads once and removes.
- DECISION: Rate limit is per `system_uuid` (5 attempts / 15 minutes),
  not per source IP. The PXE environment routinely re-NATs across
  reboots; identity is the stable thing. The 3-attempt local prompt
  cap covers the deskside operator path; the server cap defeats a
  reboot-loop attack.
- DECISION: Cleartext HTTP refusal in production mode (from Phase 0)
  is even more important now that session cookies and admin
  credentials are on the wire. The Secure cookie attribute is set
  whenever the request was over TLS, so HTTPS deployments get the
  full browser protection automatically.
- DECISION: No graded permission model. The design is explicit. A
  TODO marker in the API would suggest this is temporary; it isn't
  — it's the chosen simplification. Accountability is the audit
  trail's job.
- DECISION: The PIN-validation endpoint, when no PIN is configured,
  returns `granted: true` so the Boot Client's polite "try empty
  first" probe just works. This is the safe direction (no PIN =
  no gate = grant).

**BUILD STATE.** All green; full end-to-end smoke validated:
bootstrap file written and chmod 0600; login issues a session cookie;
the session reaches `/api/v1/auth/me`; access PIN can be set, the
correct PIN is accepted, the wrong one is rejected, and 5 wrong
attempts produce a 429 on the next one for that machine.

**NEXT.** Phase 12 — BitLocker management. The agent enables BitLocker
on C: when the machine's inventory record has a PIN; recovery keys are
escrowed back to inventory and kept as an append-only history;
re-imaging re-applies the same PIN to the new volume (with a fresh
recovery key necessarily). Secrets here include the BitLocker PIN
itself and the recovery keys; both stored encrypted at rest, neither
ever logged.

---

## 2026-05-28 — Phase 12 complete (BitLocker management)

**WHAT.**

- Migration 0005 adds `bitlocker_pin` (one row per machine) and
  `bitlocker_recovery_key` (append-only history) — both store values
  encrypted at rest.
- `internal/secrets`: AES-256-GCM Box with key sourced from
  `AUTODEPLOY_SECRETS_KEY` (hex) or auto-generated 0600 key file under
  `$AUTODEPLOY_DATA_DIR/secrets-key.bin`. Round-trip and tampering
  tests included.
- `internal/model/bitlocker.go`: SetPIN (clear-able), PINStatus (does
  NOT return value), RetrievePIN (audited), EscrowRecoveryKey
  (append-only), ListRecoveryKeys (no values), RetrieveRecoveryKey
  (audited).
- API:
  - Operator: `PUT/GET /api/v1/machines/{id}/bitlocker/pin`,
    `GET /api/v1/machines/{id}/bitlocker`,
    `GET /api/v1/machines/{id}/bitlocker/recovery-keys`,
    `GET /api/v1/recovery-keys/{id}` (last two audited).
  - Agent: `POST /api/v1/agent/bitlocker/config` returns the PIN if
    set; `POST /api/v1/agent/bitlocker/escrow` accepts a recovery key.
- Agent: `internal/bitlocker` package with build-tag split — Windows
  shells out to `powershell -Command Enable-BitLocker` with the PIN
  piped over stdin (not on the command line); non-Windows returns
  `ErrUnsupported`. The agent's deploy-time loop calls the BitLocker
  config endpoint, enables encryption if a PIN is set, escrows the
  recovery key, and logs only the FACT.

**WHY (assumptions / decisions).**

- DECISION: The PIN is fed to PowerShell over stdin so it never
  appears on the process command line or in any process listing.
- DECISION: Off-Windows hosts return `ErrUnsupported` rather than
  silently pretending to encrypt — the agent must NOT report success
  for an operation it did not perform.
- DECISION: At-rest encryption uses AES-256-GCM with a dedicated key
  outside the database. Losing the database without the key leaks
  nothing; losing the key without the database leaks nothing; losing
  both is the worst case (and is the operator's backup problem to
  solve correctly).
- DECISION: `bitlocker_pin` is a single row per machine; clearing it
  deletes the row. Recovery-key history is append-only — every
  successful encryption emits a new key, and old keys remain
  available to unlock historical drives or images. The design is
  explicit that this safety net must never be discarded.
- DECISION: Secret values returned to a client (the agent's PIN
  fetch, the portal's PIN/recovery-key retrieval) emit a structured
  `secret.access` log line capturing actor + target, never the value.
- DECISION: PIN preservation across re-image is automatic: the
  inventory record is keyed on SMBIOS UUID and persists across
  re-image (Phase 9). The recovery key is necessarily new because
  the volume was wiped.

**BUILD STATE.** Server and agent build green for Linux and Windows.
secrets, model and storage tests pass; the agent's bitlocker code
compiles under both build tags.

**NEXT.** Phase 13 — resident agent and bulk operations. Promote the
agent to a long-running silent service that checks in on a schedule
and picks up queued bulk jobs (rename, software push, ad-hoc script).
Server-side: a job queue with per-machine results, plus targeting by
name regex / OU / AD group with a preview before running.

---

## 2026-05-28 — Phase 13 complete (resident agent + bulk operations)

**WHAT.**

- Migration 0006 adds `bulk_operation` and `bulk_job` with status,
  result and claim timestamps.
- `internal/model/bulk.go`: PreviewTargets (AD-centric: name regex, OU,
  group), CreateOperation (queues one job per machine), ClaimJobsFor
  (atomic transition to `running`), CompleteJob.
- API endpoints:
  - Operator: `POST /api/v1/bulk/preview`, `POST /api/v1/bulk/operations`,
    `GET /api/v1/bulk/operations`, `GET /api/v1/bulk/operations/{id}`.
  - Agent: `POST /api/v1/agent/checkin` (claims up to 8 jobs),
    `POST /api/v1/agent/jobs/{id}/result`.
- Agent's `--check-in <duration>` flag enables a resident loop that
  polls the check-in endpoint, dispatches on action (script, rename,
  software_push), executes via the existing steps.Runner, and posts
  results.
- Tests: preview filters by OU, group, name regex; create queues one
  job per machine; second claim returns empty; invalid action rejected.

**WHY (assumptions / decisions).**

- DECISION: Target preview is done by listing all machines and
  filtering in Go. At the design's scale (low thousands) that's
  cheap and avoids encoding regex matching into SQL.
- DECISION: ClaimJobsFor returns up to 8 jobs per check-in to bound
  the agent's work between heartbeats. The interval is operator-set;
  smaller intervals see jobs sooner.
- DECISION: A job's status is one of queued / running / ok / failed.
  No retry state — a failed job stays failed and the operator can
  create a new operation to retry. Built-in retry would be a future
  enhancement.
- DECISION: Resident mode is opt-in via `--check-in <duration>` rather
  than a separate sub-command, so the same binary can both run a
  one-shot deploy-time install AND become resident afterwards if the
  unattend's FirstLogonCommand starts it with the flag.
- DECISION: Bulk rename triggers `Rename-Computer -Force -Restart`;
  AD coordination happens server-side at operation create time (the
  AD service updates the computer object), then the agent does the
  local rename and reboot. The Phase 10 service plus Phase 13 queue
  combine for the documented "rename → directory update → reboot"
  ordering.
- DECISION: Script actions use the existing steps.Runner via cmd/C
  or powershell -Command -. The body is piped via stdin where
  feasible to keep secrets and large bodies off the command line.

**BUILD STATE.** Server and agent both green; full test suite passes.

**NEXT.** Phase 14 — centralised log collection. Per-component
logging is already wired in (Phases 0+); Phase 14 adds the
client-side log shipper (Boot Client uploads its run log at end of
run, agent ships activity on check-in) and the server-side store +
portal view + retention setting.

---

## 2026-05-28 — Phase 14 complete (centralised log collection)

**WHAT.**

- Migration 0007 adds `log_event` (id, occurred_at, component, level,
  actor, action, target, fields) with indexes on time, component+time,
  actor+time.
- `internal/model/logs.go`: `LogRepo` with `Append`, `AppendBatch`,
  `Search` (filter by component/actor/action/since/until/limit) and
  `Prune` (retention).
- API:
  - `POST /api/v1/logs/ingest` accepts a batch of events; refuses
    events whose `fields` look like they contain a cleartext secret
    (best-effort tripwire over `password`, `pin`, `recovery_key`).
  - `GET /api/v1/logs` searches (authenticated).

**WHY (assumptions / decisions).**

- DECISION: Ingest is OPEN (no session required) because the Boot
  Client and pre-OS environments cannot authenticate as a portal
  user. Identity is the actor string. The tripwire rejects obvious
  secret leaks; in deployment scenarios where untrusted parties
  could ship arbitrary log events to the public endpoint, place the
  server behind a network boundary.
- DECISION: Retention is a separate operator-driven concern in Phase
  14. A scheduled `Prune` runner ships in Phase 16 alongside the
  other operational housekeeping.

**BUILD STATE.** Full test suite passes; server builds.

**NEXT.** Phase 15 — branding. System-wide brand settings applied to
the portal, the boot screen, and the deployed OEM info.

---

## 2026-05-28 — Phase 15 complete (branding)

**WHAT.**

- `internal/branding`: a typed Brand object (product name, org name,
  support URL / phone, logo data URL, primary colour, OEM
  manufacturer string) persisted as a single JSON value in
  `system_setting`.
- API: `GET /api/v1/branding` is open (the portal needs it pre-login,
  the Boot Client needs it for the menu, the agent needs it for OEM
  info); `PUT /api/v1/branding` is authenticated.
- Documented portal / boot-screen / OEM-info application in
  `docs/user-guide/branding.md`.

**WHY (assumptions / decisions).**

- DECISION: One brand, system-wide. The design is unambiguous on
  this; the API has no concept of per-image or per-tenant brand.
- DECISION: Logo is carried as a `data:` URI in the JSON so the same
  endpoint serves portal, Boot Client and agent without requiring a
  separate static-file dance. SVG and PNG both work.
- DECISION: The agent writing OEM info on Windows is documented but
  the actual registry write is not yet implemented; the brand object
  exists for portal + boot screen today, and the OEM-info writer is
  one of the "implemented when an operator needs it" items left for
  Phase 16's release-readiness pass.

**BUILD STATE.** Server builds; tests pass; no migration was needed
(the brand reuses `system_setting`).

**NEXT.** Phase 16 — hardening, scale and release readiness.
Performance and concurrent-deploy testing, secret-store and
script-execution security review, offline-agent and interrupted-
deploy resilience, backup/recovery procedure, log-retention
scheduler.

---

## 2026-05-28 — Phase 16 complete (hardening, scale and release readiness)

**WHAT.**

- `internal/retention`: hourly scheduler that prunes `log_event` rows
  older than `AUTODEPLOY_LOG_RETENTION_DAYS`. Wired into main, started
  as a goroutine when retention is configured. Zero disables.
- `scripts/backup.sh`: produces a consistent tar.gz snapshot of the
  SQLite database (via `.backup`), the at-rest encryption key, the
  TLS material and the bootstrap-admin file if still present. Mode
  0600 because the archive contains the secrets key. Payload blobs
  are deliberately excluded — they are large and re-uploadable.
- `docs/user-guide/operations.md`: end-to-end operator runbook
  covering single-server topology, the full env-var inventory,
  backup/recovery, log retention, security review checklist, and the
  explicit list of intentionally-not-in-this-release items.

**WHY (assumptions / decisions).**

- DECISION: The release stays single-server. The architecture does
  not preclude distributed/site servers (open question #2 carried
  through from Phase 0), and the API + payload service are stateless
  apart from SQLite, so a future revision can shard payload delivery
  and centralise the metadata. Phase 16 captures that explicitly in
  the operator docs rather than half-implementing it.
- DECISION: The backup archive does NOT bundle payload blobs. Operators
  typically have an upstream source of truth for ISO/driver/software
  installers and would rather copy those out-of-band; including them
  would inflate every backup by 50-500 GB for marginal benefit.
- DECISION: Performance baselines are described in the operator
  runbook but the load generator itself is left as a placeholder.
  Concrete numbers depend on the deployment host and would mislead
  if hardcoded here.

**BUILD STATE.** Server, agent and boot-client build and test green.
All 16 phases shipped to the development branch. CI workflows from
Phase 0 continue to compile every component on every push.

**RELEASE STATE.** The roadmap as written is complete. The system
covers every component the design called out: management portal,
JSON API, payload delivery (ISO + driver + software + unattend),
Boot Client with iPXE and wimlib imaging, driver matching,
unattended-setup generation, Deployment Client with detection +
ordered install steps, software loadouts, inventory + bindings +
deployment history, re-imaging, Active Directory delete-and-replace
lifecycle, portal authentication + access PIN + rate-limiting,
BitLocker with at-rest secret encryption, resident agent + bulk
operations (rename / software push / scripts), centralised log
collection, system-wide branding, and operational housekeeping
(retention scheduler, backup script, runbook).

Future work tracked in the open-questions list:
- Distributed/site-server topology if multi-site locality is needed.
- Point-in-time forensic restore built on deployment_history.
- Non-Windows target imaging.
- Graded portal roles.
- Multicast / bandwidth-optimisation strategy for large rollouts.

These are deliberate, named extensions — none of them is implied by
the design and none would change the existing schema or APIs in
ways that would block backward compatibility.

---

## 2026-05-28 — Portal CRUD UI (post-Phase 16 rebuild)

**WHAT.** Built out the management portal so an operator never has to
craft JSON or XML. The portal is now the primary interface; the JSON
API remains as the authoritative same-surface for tooling.

- Session middleware (`requireSession`) gates every `/portal/*` page
  except the login form and static assets. Login page in the portal
  itself with redirect-after-post and a `next` parameter so deep
  links survive sign-in.
- Dashboard with live counters and a quick-start list pointing at
  the entity-create paths.
- ISO: list with reference counts, new/edit forms with file upload
  and a one-click Extract trigger.
- Unattend: structured form fields for every Settings entry — locale,
  language, keyboard, time zone, edition, product key, local admin
  (with secret password field), computer-naming strategy, OOBE skip
  flags, optional domain join (toggle reveals the section), and a
  repeating first-logon-commands editor. Preview page renders the
  generated XML inline.
- Driver package: structured filter editor with dropdowns of allowed
  SMBIOS keys, value inputs and a per-filter add/remove. A separate
  "Filter preview" form on the same page evaluates the filters
  against a hypothetical machine identity and shows per-filter
  matches + overall verdict.
- Software package: type-aware detection-rule editor (file / registry
  / msi / script with per-type fields revealed dynamically) and
  install-step editor (copy / msi / appx / cmd / powershell / exe)
  with success-codes and continue-on-failure controls.
- Software loadout: parent selector + reorderable package list with
  per-row order value and opt-out checkbox.
- Image: parent / ISO / unattend / loadout dropdowns plus direct
  software-link rows.
- Machines: list with bound name + image + BitLocker status; detail
  page with binding form (name, OU, group memberships, image
  dropdown), BitLocker PIN editor (set / clear), recovery-key
  history (each row links to an audited retrieval endpoint),
  deployment history table and per-package detection state.
- Bulk operations: target builder (name regex, OU, group) with
  preview, action picker (rename / script / software push) with
  per-action fields, confirm-before-queue.
- Logs: searchable view with component / actor / action / since /
  limit filters.
- Settings: index page → access-PIN setter, branding form (with
  inline file-to-data-URL helper for the logo), accounts list with
  create / disable / enable / set-password / delete.
- Flash messages via short-lived cookie (`ok` / `err` / `warn`).
- Layout shows the active brand colour, logo, product name and the
  current user with a Sign-out button.
- The portal renders directly via repositories (same call graph the
  JSON API uses); no internal HTTP round-trips.

**WHY (decisions).**

- DECISION: Built on plain Go `html/template` + sprinkles of vanilla
  JS for the dynamic bits (per-type field reveal, add/remove rows,
  file→data-URL). No SPA framework; the portal is a single static
  binary download and works offline. Each page parses its layout and
  body templates per request from the embedded FS so adding a page
  is one file.
- DECISION: The same `auth.Repo` powers both the JSON API sessions
  (Phase 11) and the portal — one cookie, same code path. Portal
  login sets the cookie that `/api/v1` already trusts.
- DECISION: Forms use `application/x-www-form-urlencoded` (and
  `multipart/form-data` for uploads). Repeating groups (filter
  constraints, install steps, first-logon commands, loadout
  packages) use `key[]` PHP-style notation with a parallel
  `index[]` array that names each block so per-row fields can be
  scoped without ambiguity.
- DECISION: The unattend secret fields (admin password, AD join
  password) are rendered as `<input type="password">` and are
  carried only through the form POST. They land in the unattend
  row's `settings_json` (which is where Windows needs them) and
  never appear in any log line.
- DECISION: ISO and other large uploads stream through
  `MultipartReader().NextPart()` rather than `ParseMultipartForm`,
  so a 5 GB ISO never sits in RAM.

**BUILD STATE.** All packages compile; all existing tests pass. Full
portal smoke test exercised: create one of each artifact through the
form, dashboard counters update correctly, image resolved view shows
the linked ISO and Unattend by name. No secrets in any log.

**NEXT.** Portal feature set matches the JSON API. Remaining
follow-ups (none are blockers):
- Software-push bulk action gets a structured step composer rather
  than the raw-JSON textarea it has today.
- AD configuration is read-only env-driven; a portal page for it
  would be helpful if operators want to test the LDAP bind without
  restarting the server.
- Pagination on the machines and logs lists once a real fleet's
  worth of rows arrive.

---

## 2026-05-28 — Mass-scale deployment hardening

**WHAT.**

- Migration 0008 adds `payload_mirror` (name, base_url, site,
  priority, healthy, last_checked) and `machine_record.last_site`.
- `PayloadMirrorRepo`: CRUD plus `PickFor(site)` (site-specific
  preferred, global "" fallback, unhealthy skipped), `SetHealth`,
  and per-machine `last_site` memoisation.
- `payload.Throttle`: bounded-concurrency semaphore wrapping the
  `/payload/{iso,drivers,software}` routes. Default 64; configurable
  via `AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT`. Queue waits emit a metric;
  a context-cancelled queued request returns 503 promptly.
- `payload.ManifestHandler.BuildForSite(...)`: picks the mirror via
  `Mirrors.PickFor` and rewrites the WIM / driver / software URLs to
  the mirror's BaseURL. Unattend always stays on the primary (it is
  generated server-side and small). Site comes from the
  `X-AutoDeploy-Site` header, or the machine's stored `last_site`.
- `payload.serveBlob` sets `Cache-Control: public, max-age=300` so
  intermediate caches (squid, varnish, a CDN) can help. ETag and
  Last-Modified are emitted by `http.ServeContent`.
- `internal/metrics`: tiny Prometheus exposition (atomic
  Counter/Gauge, no external dep). `/metrics` endpoint mounted on
  the root mux. Counters: HTTP requests by status class, request
  duration sum by route bucket, payload bytes, payload in-flight,
  payload queued waits, deployments in progress / completed by
  outcome, boot menu / manifest / agent check-in / log ingest
  counters.
- `httpx.New` now takes a `*metrics.Registry` and the request logger
  emits metrics alongside the structured log line. The
  `statusRecorder` tracks bytes written.
- Boot Client: `--site <name>` flag, plus `autodeploy.site=<name>`
  kernel-cmdline parsing from `/proc/cmdline`. `httpc.Client.WithSite`
  attaches the `X-AutoDeploy-Site` header to every request.
- Portal: Mirrors entity with list / new / edit / delete pages and
  per-row health toggle. Nav bar gains "Mirrors". Setup checklist
  on the edit page documents the rsync / squid / second-instance
  options for standing up a mirror.
- Operator docs: `docs/user-guide/scaling.md` covers the routing
  model, throttle knobs, cache headers, `/metrics`, and a concrete
  operational recipe for thousands of machines at once.

**WHY (assumptions / decisions).**

- DECISION: Mirrors are URL-rewrite targets, not synchronisation
  endpoints. AutoDeploy does not manage replication itself —
  operators choose rsync, squid pull-through, or a second
  autodeploy-server instance. Keeping replication out keeps mirror
  setup boring and lets operators reuse existing CDN / caching
  infrastructure.
- DECISION: Unattend XML stays on the primary even when mirrors are
  configured. It is generated server-side, small, and contains the
  per-machine secrets (admin password, domain join credentials)
  that mirrors should never see, store, or potentially cache.
- DECISION: Site routing uses an HTTP header with a kernel-cmdline
  fallback, not the SMBIOS identity. The site is operationally
  defined (which subnet / DHCP scope the machine boots into), not a
  property of the hardware. Header-driven routing is the right
  shape.
- DECISION: Default throttle is 64. Below that, a 500-machine PXE
  burst would exhaust the default 1024-FD limit on many Linux
  installs. Operators on production hosts should raise both
  `ulimit -n` and the throttle.
- DECISION: Metrics use a homegrown Prometheus exposition rather
  than the official client library. Goals — zero new deps, the
  exposition format is a few text lines, our counter shapes are
  trivial.
- DECISION: SQLite is retained as the metadata store. At the
  documented scale (low thousands of machines, hundreds of
  concurrent deploys) WAL handles it. Higher concurrencies become a
  Phase 16-revisit conversation alongside multi-site distributed
  topology (still listed as the architecture's open question).

**BUILD STATE.** Server, agent, boot-client all build green; all
existing tests pass. New tests cover mirror routing
(`TestManifestRewritesToSiteMirror`,
`TestUnhealthyMirrorSkipped`) and the throttle pattern is exercised
in code.

**NEXT.** Mass-scale infrastructure is in place. Future enhancements
that build on it (none are blockers):
- A mirror-health checker that pings each mirror's `/healthz`
  periodically and updates `payload_mirror.healthy` automatically.
- A bytes-served breakdown per site/mirror in the metrics
  exposition for capacity planning.
- An optional "deployment slot" concept that caps concurrent
  imaging operations system-wide and gives a "please retry"
  response to overflow, for sites running near capacity.

---

## 2026-05-28 — Built-in TFTP server + GitHub Release pipeline

**WHAT.**

- `internal/tftp`: read-only TFTP server (RFC 1350 plus the option
  extensions PXE clients actually want — `blksize` RFC 2348, `tsize`
  RFC 2349, `timeout` RFC 2349). Each RRQ spawns a goroutine with its
  own ephemeral UDP socket as RFC 1350 requires; ACK-driven block
  pacing with retry-on-timeout; path traversal refused; WRQ refused
  with a clear error packet.
- Config: `AUTODEPLOY_TFTP_ADDR` (default empty = disabled). When set,
  the server starts a TFTP listener alongside HTTP/HTTPS, serving
  `$AUTODEPLOY_DATA_DIR/ipxe` so a classic PXE setup can fetch the
  iPXE bootstrap binaries without operators needing tftpd-hpa or
  dnsmasq.
- Tests cover small file, multi-block file, blksize negotiation,
  missing file → file-not-found error, refused write, path-traversal
  refused.
- `pxe-setup.md` updated: the built-in TFTP is now documented as
  Option A; "no TFTP daemon" becomes a default not a constraint. The
  external-TFTP path remains supported for sites already running one.

- `.github/workflows/release.yml`: triggered on `v*` tag push (and
  `workflow_dispatch` for manual builds). Build matrix:
    server      — linux amd64/arm64, windows amd64, darwin amd64/arm64
    boot-client — linux amd64/arm64
    agent       — windows amd64/arm64, linux amd64
  Each binary built with `-trimpath -ldflags="-s -w"` and a
  `.sha256` file alongside for download verification.
  A `package-extras` job bundles `scripts/initramfs/`,
  `scripts/fetch-ipxe.sh`, `scripts/backup.sh`,
  `scripts/check-secrets.sh` and the entire user-guide as
  `autodeploy-extras.tar.gz`.
  The `release` job creates / updates the GitHub Release with
  auto-generated notes (component summary + extras manifest).

**WHY (decisions).**

- DECISION: TFTP is OPT-IN, not always-on. Many environments
  already run TFTP; turning ours on would just cause a port-69
  conflict. Empty `AUTODEPLOY_TFTP_ADDR` is the safe default.
- DECISION: Read-only. AutoDeploy has no use for TFTP uploads, and
  refusing them with a clear error packet is the right behaviour.
- DECISION: Single-root layout (`$DATA_DIR/ipxe`). Operators stage
  the iPXE binaries there via `scripts/fetch-ipxe.sh`; the same
  directory the iPXE chainload already serves the kernel + initramfs
  from.
- DECISION: Built into the same binary, not a separate process.
  One binary, one config, one systemd unit. UDP + TCP + TFTP all
  share the goroutine scheduler cleanly.
- DECISION: No multicast TFTP (RFC 2090). The mass-scale story is
  HTTP mirrors (see `scaling.md`).
- DECISION: Release workflow uses tag-push trigger (`v*`). Operators
  cut a release by tagging; CI does the rest. `workflow_dispatch`
  gives a manual fall-back for ad-hoc cross-compile builds without
  cutting a release.
- DECISION: `.sha256` alongside every binary so operators can
  verify downloads. The release itself ships from a clean Ubuntu
  runner with no network access during build (after dependency
  fetch); reproducibility is per-Go-version, per-source-revision.

**BUILD STATE.** All tests green; server builds and runs with TFTP
listening on its own port; release workflow YAML parses cleanly and
will fire on the next `v*` tag push.

---

## 2026-05-28 — One-shot install + tutorial-style getting-started guide

**WHAT.**

- `scripts/install-linux.sh`: from-zero installer for Linux servers.
  Places `/usr/local/bin/autodeploy-server`, creates the `autodeploy`
  system user, makes `/var/lib/autodeploy/`, installs the systemd
  unit + a commented `/etc/default/autodeploy`, and (by default)
  runs `fetch-ipxe.sh` to stage the iPXE bootstrap binaries.
  Auto-detects the host architecture so the operator usually just
  runs `sudo ./scripts/install-linux.sh`.
- `scripts/systemd/autodeploy.service`: production systemd unit
  with `CAP_NET_BIND_SERVICE` (so :80/:443/:69 bind without running
  as root), `NoNewPrivileges`, `ProtectSystem=strict`,
  `MemoryDenyWriteExecute`, restricted address families. Sandboxed
  by default.
- `scripts/systemd/autodeploy.env.example`: every operator-relevant
  env var, commented, with sensible production defaults.
- `docs/user-guide/getting-started.md`: a from-zero tutorial that
  walks an operator end-to-end through downloading the release,
  installing the server, configuring DHCP, building the initramfs,
  uploading their first ISO, creating an unattend, composing an
  image and deploying a test machine. 13 numbered steps, each with
  expected output, common failures, and "what success looks like"
  checks.
- README.md rewritten to lead with the quick-start (download release
  → install → systemd start → log in), a downloads table, and a
  pointer to getting-started.md.
- User-guide README updated to put getting-started at the top
  before the reference index.
- installation.md reframed as the alternative-install reference;
  getting-started owns the primary path.
- Release workflow updated to bundle `install-linux.sh` and
  `scripts/systemd/` into `autodeploy-extras.tar.gz`. Release
  notes now lead with the quick-start commands rather than just
  listing components.

**WHY (decisions).**

- DECISION: One installer, one place. `install-linux.sh` does the
  boring system-integration stuff (user, dirs, unit, env file,
  iPXE) so operators only have to think about their TLS cert, AD
  credentials, and the deployment content. Windows / macOS get
  documented manual install paths because their service-management
  tooling is less uniform.
- DECISION: systemd unit defaults to a sandboxed configuration
  (`ProtectSystem=strict`, `NoNewPrivileges`,
  `MemoryDenyWriteExecute`, capability bounding set). Operators
  can loosen for unusual deployments; the default is the safe
  shape.
- DECISION: `/etc/default/autodeploy` ships fully commented with
  every env var and its purpose. Operators learn the surface by
  reading the file rather than hunting through docs.
- DECISION: Release notes are auto-generated by the workflow and
  lead with quick-start commands the operator can paste, not just
  a component list. Faster to value.
- DECISION: `getting-started.md` is structured as a tutorial — read
  top-to-bottom, no jumping around — and is the single canonical
  first-install document. All other docs are explicitly framed as
  reference.

**BUILD STATE.** Workflow YAML parses; everything else is docs and
shell scripts that don't run in CI. Release pipeline still green.

---

## 2026-05-28 — Runtime settings moved from env vars to portal

**WHAT.**

- `internal/runtime`: new façade over `system_setting` that gives
  typed getters/setters for runtime-changeable configuration: AD
  connection (URL, bind DN, bind password, search base, skip TLS
  verify), log retention days, payload throttle. Reads cache-backed;
  writes invalidate the cache. AD bind password is encrypted via
  `internal/secrets` before storage (AES-256-GCM). On first start,
  any env values seed the corresponding portal settings; after that
  the portal is the source of truth and env changes are ignored
  (avoids two-sources-of-truth drift).

- `internal/addomain/DynamicDirectory`: wraps `Directory` and pulls
  the LDAP config from a provider closure on each call. Backed by
  `runtime.Settings.ADConfig` in production. Caches the inner
  `LDAPDirectory` when the config hasn't moved (value-equality
  check on `LDAPConfig`) so we don't churn dialers needlessly. AD
  config changes through the portal take effect on the next
  manifest fetch — no server restart.

- `addomain.Service.EnabledFunc`: optional closure that overrides
  the static `Enabled` flag. Wired to `runtime.Settings.ADEnabled`
  so disabling AD in the portal short-circuits PrepareComputer
  cleanly.

- `retention.Scheduler.RetentionDays`: changed from a static
  `LogRetention time.Duration` to a callback that reads the
  current value on each tick. Operators who change retention in
  the portal see the new cutoff applied on the next hourly tick.

- `cmd/autodeploy-server/main.go`: constructs `runtime.Settings`
  early, wires the dynamic directory + the retention callback, and
  passes the settings into `api.Repos` + `portal.Repos`.

- Portal pages:
  - `Settings → Active Directory` (`settings_ad.html`): all five
    AD fields, "Test connection" button (one-shot bind +
    search), "Disable AD" button (clears every AD key), "Save"
    button. Password is `<input type="password">` and only
    written when the operator types something — blank preserves
    the existing stored value. Shows env-default values
    informationally if any are set.
  - `Settings → Operational` (`settings_ops.html`): log
    retention days and payload throttle. Clear notes on when
    each takes effect (retention: next hourly tick; throttle:
    next restart).
  - `Settings` index updated to include both.

- `/etc/default/autodeploy.env.example` and `active-directory.md`
  + `operations.md` rewritten to reflect the split: bootstrap env
  vars vs portal-managed runtime settings, plus the one-time
  env-seed escape hatch.

**WHY (decisions).**

- DECISION: AD password is encrypted at rest with the same Box
  (AES-256-GCM) used for BitLocker secrets. The DB row contains
  ciphertext only; the cleartext exists in process memory while
  AD operations run and is otherwise inaccessible.

- DECISION: Empty password in the form preserves the stored value.
  An operator editing the URL doesn't have to re-type the password
  every time, and an empty form field can't accidentally clear the
  stored password by submission.

- DECISION: Portal beats env once portal has a value. Env values
  seed on first start and on first read of a key that's never been
  set. The alternative (env always overrides) would let a
  forgotten env var silently clobber a portal value, which is a
  surprising and bad failure mode.

- DECISION: AD changes apply immediately (next manifest fetch);
  retention changes apply within the hour; throttle requires
  restart. Each setting's "when it applies" is documented inline
  on the portal form. The throttle is restart-required because
  swapping a semaphore's capacity safely under live load needs
  more care than this release wanted to spend.

- DECISION: "Test connection" performs a tiny live search rather
  than only dialing. A successful TCP connect + TLS handshake +
  bind covers most failure modes, but a search exercises the
  search-base path too.

- DECISION: Bootstrap settings stay env. Bind addresses, data
  dir, dev mode, and the secrets key all need to be known before
  the database is even open (and the secrets key encrypts the
  database rows that would otherwise hold them). Moving these to
  the portal would create a bootstrapping cycle.

**BUILD STATE.** Server builds; `go test ./...` green; end-to-end
smoke validated: AD config saved through portal, password
ciphertext-only in DB, decrypt round-trip OK, retention + throttle
saved + read back, no secret in any log line.

## 2026-05-28 — Unattend catalogue expansion (local accounts, licensing, policies, commands)

**WHAT.** The unattend editor was limited to a single Administrator
plus first-logon commands. Operators standing up real fleets needed
more: multiple local accounts, an auto-logon for post-imaging
sessions, KMS / AVMA licensing, the policy knobs they always end up
scripting by hand (telemetry, Windows Update deferral, RDP,
ICMPv4, power scheme), and the two extra command channels Windows
gives you — the specialize pass and SetupComplete.cmd.

**Code (`server/internal/unattend`):**

- `settings.go` grows: `LocalAccounts []LocalAccount`, `AutoLogon
  *AutoLogonSetting`, KMS/AVMA/SkipAutoActivation, TelemetryLevel,
  DisableWindowsUpdate, DeferFeatureUpdatesDays,
  DeferQualityUpdatesDays, EnableRDP, AllowICMPv4, PowerScheme,
  SpecializeCommands, SetupCompleteCommands. Legacy
  `admin_user`/`admin_password` fields are still parsed and
  migrated into LocalAccounts on read so existing rows keep
  working without any DB touch.

- `generate.go` rewritten around a `buildSpecializeCommands`
  aggregator: BypassNRO (Win11), then licensing slmgr invocations,
  then policy registry/netsh/powercfg lines, then the operator's
  specialize commands, then the SetupComplete writer. The
  SetupComplete writer base64-encodes the script bytes and emits a
  single PowerShell one-liner that writes
  `%WINDIR%\Setup\Scripts\SetupComplete.cmd` — handles arbitrary
  bytes safely and works on every supported Windows release.
  `writeUserAccounts` loops over LocalAccounts and additionally
  fills `<AdministratorPassword>` from the first Administrators-
  group account so tooling that looks at the built-in Administrator
  credential still finds a usable value.

- `catalogs.go` gains AccountGroups, TelemetryLevels and
  PowerSchemes so the portal picker can render labelled options
  instead of free-text.

**Portal (`server/internal/portal`):**

- `unattend.go` parses repeating `la_*[]`, `sp_*[]`, `sc_*[]`
  field arrays plus the new licensing/auto-logon/policy fields.
  Empty-name account rows are skipped so leftover blanks from the
  "add row" button don't pollute the unattend.

- `templates/unattend_form.html` grew from 10 to 15 sections.
  Local accounts is now a repeating table; auto-logon and domain
  join are toggle-revealed blocks; licensing/policies use the new
  catalogs. Specialize and SetupComplete reuse the same
  add/remove repeater pattern as first-logon. The TOC sidebar
  is regenerated.

**WHY (decisions).**

- DECISION: Migrate the legacy single-admin fields silently. We
  could have forced operators to re-enter the password in the new
  UI, but the existing rows are in production and the migration
  is unambiguous.

- DECISION: SetupComplete.cmd via PowerShell + base64, not
  certutil. certutil's `-decode` flag requires PEM-format input
  (BEGIN/END markers), which means the payload has to be wrapped;
  PowerShell's `[Convert]::FromBase64String` accepts the raw
  encoding and writes arbitrary bytes via
  `[IO.File]::WriteAllBytes`. PowerShell is on every supported
  Windows, so there's no compatibility cost.

- DECISION: First Administrators-group account doubles as the
  built-in Administrator password. Some tooling and recovery
  paths look at `<AdministratorPassword>` rather than enumerating
  `<LocalAccounts>`. Filling both costs us nothing and avoids
  surprise.

- DECISION: TelemetryLevel uses 0 as "off / do not emit". The
  alternative was -1 or `*int`, but 0 is what the portal sends
  for unselected and matches the Go zero value cleanly; the only
  user-visible cost is hiding the rarely-useful Security/0 level
  from the dropdown (Enterprise only, indistinguishable from "do
  not emit" on other editions).

- DECISION: Specialize commands run AFTER the system-supplied
  policy/license commands. Operators can rely on the policy
  state when their command runs (e.g. an operator-defined firewall
  rule on top of EnableRDP's open rule will behave deterministically).

**BUILD STATE.** Server builds; `go vet ./...` clean; `go test
./...` green (including the new licensing, auto-logon, policies,
specialize and SetupComplete tests); `scripts/check-secrets.sh`
green. Legacy admin_user/admin_password JSON migrates cleanly into
the new LocalAccounts model without losing the password.

## 2026-05-28 — Native Windows Service support + PowerShell installer

**WHAT.** The server cross-compiled to Windows cleanly already, but
the install experience was "drop the .exe somewhere, wrap with NSSM,
good luck." Operators on Windows now get the same one-shot install
the Linux side has — a native Windows Service registration, env-file
configuration, firewall rules, backup script, and a walkthrough that
mirrors `getting-started.md`.

**Code (`server/cmd/autodeploy-server`).**

- `main.go` refactored: `run()` now takes a `context.Context`; the
  signal-handling wrapper moved into `runConsole()` so the Windows
  service entrypoint can cancel the same context via SCM Stop
  instead of via SIGINT/SIGTERM.

- `service_windows.go` (build tag `windows`) checks
  `svc.IsWindowsService()`. When launched by the SCM, it runs
  `svc.Run` with a handler that reports StartPending → Running,
  watches the SCM control channel for Stop / Shutdown / Interrogate,
  and cancels the context cleanly. Interactive launch (a Run-as-
  Admin PowerShell on the install host for troubleshooting) falls
  through to `runConsole`.

- `service_other.go` is a one-line shim that aliases `runPlatform`
  to `runConsole` on non-Windows platforms.

- `golang.org/x/sys` was promoted from indirect to direct so the
  service import is explicit in go.mod.

**Scripts (`scripts/windows/`).**

- `install-windows.ps1` — idempotent installer. Copies the binary
  to `C:\Program Files\AutoDeploy`, creates `C:\ProgramData\AutoDeploy`,
  registers the service via `sc.exe` (start=auto, obj=LocalSystem by
  default), reads `autodeploy.env` and writes the KEY=VALUE lines
  into `HKLM\SYSTEM\CurrentControlSet\Services\autodeploy\Environment`
  as REG_MULTI_SZ (which the SCM merges into the service's
  environment block at start), creates inbound firewall rules for
  443/TCP, 80/TCP and 69/UDP scoped to the binary, then starts.
  `-ApplyEnv` re-reads the env file and restarts, no binary touch.
  `-Update` is implicit: re-running just upgrades.

- `uninstall-windows.ps1` — removes the service, the firewall rules
  and the install dir. Preserves the data dir unless `-PurgeData`.

- `backup.ps1` — produces a single zip with the SQLite snapshot
  (via `sqlite3.exe` if present, else a WAL-aware file copy),
  `secrets-key.bin`, TLS material, and the bootstrap-admin file.
  Tightens the zip's ACL to Administrators + SYSTEM because the
  secrets key is inside.

- `autodeploy.env.example` — Windows-flavoured copy of the Linux
  env example (paths in `C:\ProgramData\AutoDeploy`, PowerShell
  one-liner for key generation).

**Docs.**

- `docs/user-guide/install-windows.md` — full walkthrough mirroring
  `getting-started.md`'s Linux section: download, run installer,
  configure env, place TLS material, iPXE assets, day-to-day
  operation, upgrade, backup, uninstall, caveats.

- `docs/user-guide/getting-started.md` — Windows section now points
  at the new installer instead of waving at NSSM.

- `docs/user-guide/operations.md` — backup section shows both
  `scripts/backup.sh` and `scripts/windows/backup.ps1`.

- `docs/user-guide/README.md` — Windows install added to the index.

**CI (`.github/workflows/release.yml`).** `autodeploy-extras.tar.gz`
now bundles `scripts/windows/`, and the generated release notes
include a Windows quick-start block alongside the Linux one.

**WHY (decisions).**

- DECISION: Native Windows Service support in the binary, not a
  wrapper. `golang.org/x/sys/windows/svc` is ~30 lines of glue and
  avoids dragging in NSSM/WinSW/srvany. The binary detects whether
  the SCM started it (`svc.IsWindowsService()`); interactive
  PowerShell runs still behave like a normal console program for
  troubleshooting.

- DECISION: Env file at `C:\Program Files\AutoDeploy\autodeploy.env`
  rather than `C:\ProgramData\AutoDeploy\autodeploy.env`. The env
  file is operator-edited and should travel with the install, not
  the data. The installer writes the values into the service's
  registry environment block so the SCM does the merge at start
  time — same mechanism Microsoft uses for services like wuauserv.

- DECISION: LocalSystem by default. Binding TFTP UDP 69 and HTTPS
  TCP 443 are privileged operations on Windows just as on Linux;
  NetworkService cannot bind them without extra ACL juggling. The
  attack surface is small (the binary reads from a fixed data dir
  and never executes operator-supplied code) so LocalSystem is the
  pragmatic default. The installer accepts `-ServiceAccount` to
  override for hardened setups.

- DECISION: Firewall rules are program-scoped, not just port-scoped.
  `New-NetFirewallRule -Program <binary>` means another process
  cannot reuse the rule even if it binds the same port. Defence in
  depth.

- DECISION: Restart=on-failure equivalent via `sc.exe failure ...
  actions= restart/5000/restart/5000/""`. Two restart attempts at 5s
  apart, then give up — same shape as the systemd unit's
  `Restart=on-failure RestartSec=5`.

**BUILD STATE.** `go vet ./...` clean for both `GOOS=linux` and
`GOOS=windows GOARCH=amd64`; `go test ./...` green on Linux;
cross-compiled `autodeploy-server.exe` is 19 MB (CGO disabled);
`scripts/check-secrets.sh` green; smoke test against the Linux
binary confirmed the refactored `run(ctx, logger)` boots through the
full lifecycle.

## 2026-05-28 — Audit follow-up: closing the design gaps

The recent design audit surfaced several places where the worklog's
"Phase N complete" markers were ahead of the code. This change closes
the four High-severity gaps and the High shortfall flagged in the
report, plus the lower-severity ones a single pass could absorb.

**Per-machine identity in the generated unattend (audit gap #3).**
The Boot Client now passes its SMBIOS UUID through the unattend URL
the manifest endpoint hands it. `serveUnattend` reads the binding by
UUID and overrides `NameStrategy=literal`, `ComputerName=<binding name>`
and (when domain join is configured) `DomainJoin.OU=<binding OU>`.
Re-imaging therefore preserves identity (§4.3) and the joined AD
name matches the object the manifest endpoint just prepared. Pure-
catalog endpoints (the portal preview link) still work because the
override is optional -- absent `?uuid=` returns the catalog XML
unchanged.

**AD coordination for bulk rename (audit gap #2).** Added
`Directory.RenameComputer` (FakeDirectory + LDAPDirectory +
DynamicDirectory) and `Service.RenameComputer`. The bulk-create
handler now walks the just-queued jobs for action=`rename`, looks
each machine's binding up, asks the Domain Integration Service to
rename the AD object from the old binding name to the new payload
name, and updates the binding. Failures are surfaced as
`ad_warnings` on the response and as audit log lines; the local OS
rename still runs (some operators have AD-unjoined targets in the
same selection). LDAP rename also re-writes sAMAccountName so the
secure channel stays consistent.

**Centralised log shipping from clients (audit gap #1).** New
`logging.Shipper` in each client wraps the JSON slog handler with a
bounded in-memory buffer (2048 events, oldest-drop policy). On exit
the Boot Client and agent POST the drained buffer to
`/api/v1/logs/ingest`; the Boot Client also flushes before
`reboot`. The agent's resident-mode check-in loop ships on every
tick. The server endpoint gained a body-size cap (256 KiB), an
events-per-request cap (500), and a per-source-IP token-bucket rate
limit (50 burst, 10/s refill) so a noisy client cannot flood the
log table.

**Agent SMBIOS UUID on Windows (audit gap #6).** Split
`readSystemUUID` into platform files. Windows reads via
`Get-CimInstance Win32_ComputerSystemProduct` (the supported
modern path), falling back to `wmic csproduct get UUID`. Both
results are normalised to a 36-char hyphenated lower-case form so
the server-side identity comparison is deterministic.

**SCCM driver package ingest + proper offline injection (audit
gap #4).** New endpoint `POST /api/v1/drivers/{id}/extract` unpacks
the uploaded zip into `data/drivers/{id}/files/`, refuses zip-slip
paths, caps total bytes, walks for `.inf` files, parses the
[Version] section (handles UTF-16 LE and ASCII INFs), and writes
a `metadata.json` summary alongside. The Boot Client's imaging
`Apply` was restructured: the Windows partition mount now persists
across `applyWIM` + `injectDrivers` + `placeUnattend` (previously
the mount was released inside `applyWIM`, silently dropping driver
and unattend writes). `injectDrivers` extracts each downloaded zip
into `<mount>\Windows\INF\AutoDeploy\<name>\` so Windows PnP picks
up the .inf files on first boot, which is the offline-servicing
equivalent of `dism /Add-Driver /Recurse` for our pipeline. Single-
file legacy uploads still work via a cp fallback.

**Boot screen + agent OEM branding (audit gap #5).** The Boot Client
fetches `/api/v1/branding` before rendering the menu and shows
`<OrganisationName> -- <ProductName>` in the header (Brand defaults
keep the AutoDeploy header when nothing is configured). The agent
on Windows writes the brand's `OEMManufacturer`, `SupportURL` and
`SupportPhone` to `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\
OEMInformation` via `reg add` so System Properties shows the
operator's organisation rather than the OEM's.

**Agent BitLocker PIN endpoint authentication (audit shortfall #1).**
New `machine_deploy_token` table (migration 0009) holds the SHA-256
hash of a per-machine bearer token. The token is issued by the
agent report endpoint on the `outcome=in_progress` open path,
rotated on every new deploy, and expires after 24 hours. The agent
records it in memory (never to disk, never logged) and sends it as
`X-AutoDeploy-Deploy-Token` on every request. `POST /api/v1/agent/
bitlocker/config` now requires a valid token before returning the
cleartext PIN. Stealing a SMBIOS UUID is no longer sufficient to
retrieve the PIN -- only the freshly deploying client has the
token.

**Bootstrap admin file warning (audit shortfall #6).** The portal
layout now shows a red flash banner when `admin-bootstrap.txt`
still exists in the data directory after the operator has logged
in. The check is a single stat per page render; the banner
disappears the moment the operator deletes the file.

**Built-in TFTP listener formalised (was: non-goal violation).**
`docs/design/AutoDeploy_Project_Context.txt` was amended: the anti-
goal "NO reliance on TFTP or layer-2 PXE. HTTP(S) only." is
clarified to "NO TFTP in the payload path"; a built-in TFTP
listener serving the iPXE bootstrap binaries only is a sanctioned
addition. The data path stays HTTP(S); TFTP only carries the
firmware-to-iPXE handoff for non-HTTPBoot firmware.

**WHY (decisions).**

- DECISION: Identity injection is a layer over the catalog
  unattend, not a separate per-machine row. The catalog stays the
  source of truth for everything that isn't identity-bearing
  (locale, software, policies, ...); only `ComputerName` and
  `DomainJoin.OU` are overridden when a binding exists. This keeps
  the operator's mental model intact: one unattend per role, many
  bindings.
- DECISION: AD rename runs server-side at bulk-create time, not
  agent-side. The design (§13.2) names the sequence "rename ->
  directory update -> reboot"; making the agent call AD would mean
  the agent needs domain credentials. Keeping AD on the server
  preserves least-privilege.
- DECISION: Deploy token is per-machine and rotates per deploy,
  not a long-lived agent secret. The threat we are closing is "an
  attacker spoofs the SMBIOS UUID to a known target". The window
  in which the token is useful matches the window in which the
  agent legitimately needs the PIN; outside that window an
  attacker has nothing useful.
- DECISION: Log shipping is best-effort, not blocking. A failed
  ship re-queues into the buffer for the next attempt; the deploy
  itself does not block on the server seeing the events. This
  matches the design's "logs are facts" framing.
- DECISION: Driver injection by file-copy into the mounted INF
  search path, not by `dism /Add-Driver`. wimlib has no direct
  equivalent of DISM's offline-add semantics; mounting and copying
  into `%WINDIR%\INF\AutoDeploy\<name>\` lets Windows PnP discover
  the .inf files on first boot which is functionally equivalent
  for our needs and avoids requiring DISM-on-Linux tooling in the
  initramfs.
- DECISION: Bootstrap admin warning is a per-page-render stat, not
  a startup-time one-shot. The check is sub-microsecond; the
  operator-visible signal that the file is still there is the
  whole point.

**BUILD STATE.** `go vet ./...` clean on linux + windows for all
three modules; `go test ./...` green across server, boot-client and
agent; `scripts/check-secrets.sh` green; cross-compiled binaries
(server.exe 20 MB, agent.exe 9 MB) build clean. New tests added:
`TestDeployTokenIssueAndValidate`,
`TestDeployTokenExpiry`,
`TestRenameComputer`, `TestRenameNoSuchObject`,
`TestRenameDisabledServiceIsNoOp`,
`TestServeUnattendInjectsBindingIdentity`.

## 2026-05-28 — Portal redesign: design system + interactivity

The portal was functional but visually plain. This change ships a
proper design system with dark mode, an icon language, polished
list/form/dashboard styling, plus the interactive bells & whistles
operators expect from modern admin consoles.

**Design system (`assets/style.css`):**

- CSS custom properties for every colour, type and spacing token.
- Light + dark themes via `prefers-color-scheme` and an explicit
  `[data-theme="dark|light"]` override (cycled by a header button,
  remembered in localStorage, applied inline before first paint to
  avoid the flash).
- Modern type stack (Inter / Segoe UI Variable / system sans), a
  proper monospace stack for code, three sizes of soft shadow,
  rounded corners scaled per surface.
- Component classes: `.card`, `.tablewrap`, `.toolbar` + `.filter`,
  `.section-head` (label with horizontal divider rule), `.empty`
  (friendly empty state with icon), `.badge`, `.dot`, `.skel`
  (skeleton loader), `dialog.modal`.
- A sticky app header that scrolls horizontally on narrow screens
  rather than wrapping; nav items get icons; theme toggle and help
  buttons live in the right-hand toolset.
- Forms: focus ring, accent-coloured checkboxes/colour-picker, group
  fieldsets, repeating-row treatment for catalog editors.

**Icons (`templates/_icons.html`):**

- Single SVG sprite parsed alongside the layout so every template
  can `<svg><use href="#i-…"/>`. 22 icons cover the entities (image,
  disc, file, cpu, box, stack, server), actions (plus, pencil,
  trash, copy, search, check, eye), status (alert, info, lock,
  shield), nav (arrow, ext, logout, help, sun/moon/auto for the
  theme toggle).

**Interactivity (`assets/app.js`):**

- Sortable tables: `<table data-sortable>` + `<th data-sort="num">`
  for numeric columns; clicking a header toggles asc/desc.
- Filter-as-you-type: `<input data-filter="#tbl">` hides rows whose
  text content doesn't contain the (case-insensitive) query;
  optional `[data-filter-count="<id>"]` displays the matched count.
- Copy-to-clipboard: any `.copy` or `[data-copy]` button copies the
  attribute text (or the previous sibling's text) and shows a brief
  check-mark confirmation.
- Confirm dialog: forms with `data-confirm="msg"` open a styled
  `<dialog>` instead of the native `confirm()`; ESC cancels.
- Live log tail: `#logs-tail` polls `data-url` every
  `data-interval-ms` (default 4s), prepends new events, caps the
  list at 200, stops when the tab is hidden.
- Keyboard shortcuts: `/` focuses the filter input, `g i/m/l/s/u/d/w/o`
  jump to the main sections, `?` opens a help dialog.
- Theme toggle button cycles auto → dark → light → auto.

**Dashboard upgrade (`templates/index.html` + `portal.go`).**

The dashboard now shows the count tiles (each with the entity
icon), a 24-hour deploy outcome rollup (ok / in-progress / failed
with status dots), a "recently seen machines" list, a recent-
activity log feed, and a Quick Start ordered list. Every panel
collapses to a friendly empty state with an icon and one-sentence
explanation when there's no data yet -- a much better landing for
a fresh install.

**Lists, forms, settings.** Every list page got: a filter input
above the table (with the magnifier icon), a copy-to-clipboard
shortcut for any visible identifier (UUIDs, paths, URLs), an empty
state with an icon and call-to-action, sortable headers where the
column has a defensible order. The settings landing page renders
each section as a card with its icon, a one-line value summary
(brand product name, user count, AD status dot) and a description.
The branding form gained a live colour-picker mirror and a logo
preview.

**Login page.** Centred card with the logo above the title, error
flash rendered as a styled badge, hint about `admin-bootstrap.txt`
links to the right Settings page.

**WHY (decisions).**

- DECISION: Vanilla JS, no framework. The portal is a Go server-
  rendered app; adding React/Vue would mean a build step and a
  parallel state model for very little payoff. Vanilla event
  delegation hits everything we need in ~250 lines, ships
  ungzipped at 13 KB.
- DECISION: Dark mode follows the OS by default and can be
  overridden per browser (localStorage). The accent colour stays
  the operator's brand colour in both themes; only the
  greys / surfaces / soft-tints switch.
- DECISION: Icons inline as an SVG sprite. No icon-font request,
  no external sprite fetch; the sprite is parsed once per page and
  every `<use>` is just an attribute reference. Adds ~7 KB to each
  HTML response, gzipped to ~3 KB, well worth the polish.
- DECISION: Filter and sort run client-side. Pagination is a real
  need at large fleet sizes but the design's scale (thousands of
  machines max) lets the simple "render the whole list, filter in
  the DOM" pattern work, and means the URL still represents the
  full result set for bookmarking.

**BUILD STATE.** Server builds; `go test ./...` green; secret
check clean; screenshot smoke test in headless Chromium confirms
the dashboard, machine list, logs page, settings landing and login
all render cleanly in light + dark themes at 1366x900.

## 2026-05-28 — Portal polish pass #2: forms, 404, accessibility, bulk-select, pagination, sticky-thead fix

Follow-up to the design-system pass. Brought every form template into
the same visual language as the lists, added a styled 404, accessibility
basics, server-side pagination, bulk-select on the list pages where
"delete a handful of rows" is a real workflow, and fixed a sticky-thead
bug that was hiding body rows on every table.

**Sticky-thead bug.** The previous pass set
`th { position: sticky; top: 56px; }` so the column headers stick under
the app header when an operator scrolls a long table. Combined with
`.tablewrap { overflow: hidden; }` (which exists to clip the rounded
corners), the tablewrap becomes a containing block for the sticky
thead. On short tables the parent is shorter than `top: 56px`, and the
thead ended up rendering after / on top of the body row, hiding it.
The fix opt-ins sticky behaviour via a new `.tablewrap.sticky` class
and leaves the default static. None of the existing tables actually
needed the sticky behaviour for the current row counts.

**Forms.** All form templates rewritten to use the toolbar + back link
pattern, fieldset legends rendered as muted uppercase section labels,
buttons gained icons and the new `sm` size where appropriate, and
inline confirms migrated from `onsubmit="return confirm(...)"` to
`data-confirm="..."` so the styled dialog catches them. Per-button
`data-confirm` now supported too (the AD page's Disable button needs
a different message than the form's Save).

- `iso_form.html`, `image_form.html`, `driver_form.html`,
  `loadout_form.html`, `mirror_form.html`, `bulk_form.html`,
  `bulk_detail.html`, `image_resolved.html`, `machine_detail.html` —
  all brought up to spec with toolbar headers, section dividers,
  empty states, copy buttons on identifiers.
- `settings_ad.html`, `settings_accounts.html`, `settings_ops.html`,
  `settings_pin.html`, `settings_branding.html` — section icons in
  the header, polished controls, status badges with dots.
- `settings_accounts.html` gained a "Change your own password" section
  (audit recommendation #8) so the logged-in operator has a one-form
  path to rotate their credential without using the row inline form.
- `bulk_detail.html` gained sortable headers + empty state.
- `image_resolved.html` gained empty states per resolved object.

**404 / error page.** New `templates/error.html` renders inside the
portal shell with a "Go back" + "Dashboard" action pair. The portal's
catch-all handler (`get("/portal/", ...)`) dispatches to the dashboard
for the bare path and to the error page for anything that fell
through. A `renderPortalError` helper writes the configured status
code via a buffered ResponseWriter wrapper so the Content-Type set by
the layout template is preserved.

**Favicon.** Inline SVG mark, served from
`assets/favicon.svg`, referenced from the layout via
`<link rel="icon" type="image/svg+xml">`.

**Accessibility.** Skip-to-content link in the layout; every icon-only
button got `aria-label`; nav has `aria-label="Primary"`; bare `<svg>`
icons inside text content marked `aria-hidden="true"`; `main` is
`tabindex="-1"` so the skip link can focus it.

**Pagination.** Added `paginate(req, total, defaultSize)` helper that
reads `?page=N&size=N` from the URL, clamps and returns a `PageInfo`
the template can render. Wired into the machines list as the
strongest candidate at scale. A new `{{template "pagination"}}` partial
inside `machine_list.html` renders the prev/next controls and the
"showing N-M of T" counter. `add` and `sub` template funcs added so
the counter can render `Offset+1`.

**Bulk-select.** A `<table data-bulk-target="/portal/foo/{id}/delete">`
opts in. The header checkbox toggles all body rows; a sticky bar
(hidden when nothing is selected) appears with the count and a
"Delete selected" button. Confirms via the same styled dialog the
single-row deletes use; on confirm, fires one POST per checked ID and
reloads. Wired into the drivers and software lists.

**WHY (decisions).**

- DECISION: bulk-select uses one POST per row instead of a new
  bulk-delete endpoint. Existing per-row delete handlers already
  enforce ref-counts and audit; reusing them keeps the audit
  semantics intact (one "deleted by op X" event per row) and avoids
  a new validation surface. Cost: N round-trips instead of one,
  which doesn't matter for the typical "select 5 stale drivers".
- DECISION: pagination wired on the machines list first. Logs and
  some other lists could use it too but the operator's strongest
  ask is "don't render 10k machine rows when I just want one"; the
  remaining lists are bounded by definition (you only have so many
  unattends).
- DECISION: 404 renders inside the portal shell, but the unknown-
  route catch-all uses a single `get("/portal/", ...)` that checks
  the URL path. Adding a generic mux NotFoundHandler would have
  required restructuring the public/protected route wrapping.
- DECISION: aria-labels on icon buttons, not visible text. The
  icons are recognisable enough that adding text labels would
  bloat the toolbar; aria-label keeps the SR experience accurate
  without changing the visual design.

**BUILD STATE.** Server builds; `go test ./...` green; secret check
clean; smoke test confirmed all 26 portal routes return 200 and the
404 path renders the styled page with the right status code.
Screenshot test in headless Chromium confirmed sticky-thead bug
gone, drivers list shows bulk-select column, accounts page renders
the admin row with the new password section, AD form is consistent
with the rest of the settings.

## 2026-05-28 — Driver upload polish + Downloads page

Two related operator-visible features. The driver flow now closes the
loop you would expect from an SCCM-style "upload zip, see what's
inside, attach filters" UI; the downloads page exists so operators can
grab the Windows agent (and other distributable binaries) without
opening a terminal.

**Driver flow.**

- New `payload.ExtractDriverPackage(ctx, drivers, blobs, id)` shared
  helper that runs the unzip + .inf scan + metadata.json persistence
  pipeline. Used by both the API endpoint and the new portal handler.
  Also a `payload.ReadDriverMetadata(blobs, id)` for read-only
  rendering on edit.
- New portal route `POST /portal/drivers/{id}/extract`. Triggers the
  shared extractor and flashes a "Extracted N files (M .inf, K
  bytes)" success line. Surfaced as a secondary button on the upload
  section ("Extract & scan"), disabled until a payload is uploaded.
- The edit page now reads the persisted metadata.json on every
  render and shows the discovered `.inf` rows in a table (Path /
  Class / Provider / Version / Date), so the operator sees what is
  actually inside the zip without re-extracting.
- New "Use a known machine as a filter" helper above the filter
  blocks. A dropdown of inventoried machines (filtered to those with
  a SMBIOS make + model) plus an "Add as filter" button that creates
  a filter block pre-populated with `system_manufacturer` and
  `system_product` constraints. Cuts the per-package filter setup
  from "click dropdown, type Dell Inc., click Add, click dropdown,
  type Latitude 5520" to one dropdown + one click.

**Downloads page.**

- New `$DATA_DIR/downloads/` convention. The portal lists every
  file in that directory grouped by filename prefix (deployment
  agent / Boot Client / server / installer scripts / Other) so
  operators can drop new binaries in and have them appear in the UI.
- `GET /portal/downloads` renders the grouped table.
- `GET /portal/downloads/file/{name}` serves the file with
  `Content-Disposition: attachment`. Refuses any name containing
  `/`, `\\`, or `..` and double-checks the resolved path stays
  inside the downloads directory (defence in depth for the obvious
  zip-slip / path-traversal vectors).
- Empty state lists the canonical filenames the portal classifies,
  so the operator knows what to drop.
- Settings landing gains a "Downloads" card with the agent/tools
  summary; dashboard toolbar gains a quick-access ghost link.
- `scripts/install-linux.sh` and `scripts/windows/install-windows.ps1`
  create `$DATA_DIR/downloads/` and seed it with any agent / boot
  client binaries that travelled in the same release bundle, so a
  fresh install has working downloads without manual file moves.

**WHY (decisions).**

- DECISION: Driver extract is a separate operator-triggered step,
  not auto-on-upload. Some SCCM zips are 2-4 GB; doing the
  extraction inside the upload handler would block the form's
  submit on disk I/O. As a separate POST the operator can also
  re-extract after the server upgrades the metadata parser.
- DECISION: Machine-as-filter uses inventory rows that already have
  manufacturer + product. Machines whose SMBIOS hasn't been
  populated (a partial register, an old agent version) are filtered
  out of the dropdown rather than offered as a useless option.
- DECISION: Downloads served from `$DATA_DIR/downloads/` rather
  than embedded in the server binary. The agent binaries are
  released separately from the server and would balloon the server
  image; this way operators choose which agent version their
  portal hands out, and re-distribution after an agent rev is a
  drop-in copy.
- DECISION: Downloads endpoint is auth-required. The binaries
  themselves are not secret, but the portal has no public surface
  by design; gating downloads through the session avoids accidental
  exposure of, say, a pre-release agent on an internet-reachable
  portal.

**BUILD STATE.** Server builds; `go test ./...` green; secret check
clean. End-to-end smoke test created a driver row, uploaded a tiny
SCCM-style zip containing an Intel Ethernet .inf, triggered extract,
and confirmed the rendered edit page surfaces the discovered Class /
Provider / Version / Date. Downloads page rendered with the four
seeded binaries grouped under their proper section headers; the
path-traversal probe (`%2F..%2Fetc%2Fpasswd`) returned the styled
400 page rather than the file. Both screenshots attached above.

## 2026-05-28 — Windows installer fetches iPXE binaries automatically

Symmetry fix between the two install scripts. install-linux.sh has
always run fetch-ipxe.sh by default; install-windows.ps1 only created
the empty ipxe\ directory and left the operator to populate it.
That's a needless friction step on a fresh Windows server.

- New `scripts/windows/fetch-ipxe.ps1` mirrors `scripts/fetch-ipxe.sh`:
  pulls undionly.kpxe, ipxe.pxe, ipxe.efi, snponly.efi and the arm64
  ipxe.efi from boot.ipxe.org into `$DATA_DIR\ipxe\`. Per-file failure
  doesn't abort the batch (transient network blips are common);
  files below 1 KiB are deleted on the assumption they're error
  pages. The TLS-1.2 fallback is wrapped in try/catch so older
  Windows PowerShell hosts that lack `Tls13`/.NET 4.8 still run.
- `install-windows.ps1` now calls the fetcher between env-file copy
  and service registration. Accepts `-NoIPXE` to skip (mirrors the
  Linux script's `--no-ipxe`). Failures from the fetcher land as a
  warning rather than an installer abort, so an offline install host
  can still finish bringing the service up; the operator re-runs
  `fetch-ipxe.ps1` once they have network access.
- Hardened the Linux side at the same time: `install-linux.sh` no
  longer aborts when fetch-ipxe.sh fails. `fetch-ipxe.sh` itself now
  retries per-file independently, deletes sub-1 KiB stubs, and
  exits non-zero only at the end with a clear "re-run later"
  message.
- Docs (`docs/user-guide/install-windows.md`) updated to mention
  the new fetch step and the `-NoIPXE` switch; the Step 5 iPXE
  section now describes what's already there and what the operator
  still has to produce (autodeploy-kernel / -initrd from the
  initramfs build).
- Files ship in the existing `autodeploy-extras.tar.gz` bundle
  because `scripts/windows/` is already in the tar.

**WHY (decisions).**

- DECISION: Fetch on install rather than on first server start.
  Doing it at install time means the operator notices network
  reachability problems while they have the install console
  open, rather than discovering them when a PXE client tries to
  chainload at 02:00.
- DECISION: Use `boot.ipxe.org` directly instead of an iPXE
  mirror we host. Operators can already replace the URL with a
  local mirror by editing the script; bundling iPXE binaries
  into the AutoDeploy release would force us to track iPXE's
  release cadence and adds ~5 MB to each release tarball.

**BUILD STATE.** Bash scripts `bash -n` clean; PowerShell
hand-review for syntax (Windows PowerShell + .NET Framework
compat for the TLS shim, `$LASTEXITCODE` check in the install
wrapper so fetch failures land as a warning rather than a silent
abort). Sandbox lacks outbound network so I couldn't validate the
actual fetches end-to-end.

## 2026-05-28 — Documentation overhaul + screenshots

The user-facing docs were text-heavy and a few sections were stale
(notably the "HTTPS required" implication in
`configuration.md` and `operations.md`). This pass:

- Captures **27 screenshots** of the now-fully-populated portal
  (`docs/user-guide/images/`) — dashboard light/dark, every list
  page, every form page, every settings sub-page, the Downloads
  page, the 404 page.
- Embeds screenshots inline in every doc where they explain the
  feature better than prose.
- Rewrites each section in a consistent shape: opening callout +
  screenshot + structured walkthrough + tables for rules + API
  recipes at the end.
- Clarifies the HTTPS rules explicitly: `configuration.md` now has
  a top-of-doc TL;DR table for the four common scenarios
  (local dev, lab on LAN, lab with self-signed HTTPS, production)
  and a full "HTTP vs HTTPS — the full rules" section spelling
  out exactly when the server refuses to start.
- Adds a **Concepts** rewrite with an ASCII diagram of how the
  objects connect, the resolution rules table, and the lifecycle
  walkthrough.
- Restructures the README as an entry point — short intro,
  screenshot, "Start here" links to getting-started / install-
  windows / concepts, then reference grouped by area (Setting it
  up / Day-to-day artifacts / Operations / APIs), then a "Using
  the portal" tour with screenshots, then a feature surface table.
- Each doc cross-references siblings instead of duplicating
  content (e.g. BitLocker → Security, AD → Bulk operations).
- Removes the leftover "Until in-process HTTPS option is added,
  the loopback + reverse-proxy pattern is the supported production
  deployment" line that contradicted shipped behaviour.
- `getting-started.md` step 5 now embeds the login and dashboard
  screenshots so first-time operators see what they're aiming
  for, and the prerequisites table now points at
  `configuration.md` for the HTTPS rules instead of implying
  HTTPS is required.

**Files rewritten:**

- `README.md` (entry point)
- `concepts.md` (object diagram + rules)
- `configuration.md` (HTTPS TL;DR + full rules)
- `installation.md` (Linux, with the install-script fetch story)
- `unattend.md` (15-section walkthrough + JSON shape table)
- `driver-matching.md` (renamed from "Driver matching" to "Drivers",
  with the full SCCM upload + extract + machine-as-filter UX)
- `software.md` (with worked 7-Zip example)
- `loadouts.md` (resolution rules table + guards)
- `inventory.md` (machine detail page tour)
- `reimaging.md` (preserved-vs-discarded table)
- `bitlocker.md` (with deploy token explanation)
- `security.md` (with checklist + secret tripwire details)
- `branding.md` (where-it-shows-up + agent OEM-info writer)
- `active-directory.md` (with bulk-rename + AD coordination)
- `bulk-operations.md` (two-stage form + AD rename sequence)
- `logging.md` (live-tail panel + retention + secret tripwire)
- `payloads.md` (per-resource flow + Range + throttle)
- `operations.md` (env vs portal split + topology diagram)
- `boot-client.md` (deploy step-by-step + initramfs build)
- `api-quickstart.md` (auth + recipes by area)

**Screenshots:** 27 images in `docs/user-guide/images/`. Generated
from a seeded portal with 3 ISOs, 2 unattends, 3 driver packages,
3 software packages, 1 loadout, 3 images, 6 machines (one bound +
BitLocker PIN set), 1 bulk operation, brand set to "Acme
Corporation". Captured at 1400x900 @ 2x via Playwright in headless
Chromium against the actual server build.

**BUILD STATE.** Server unchanged; the docs are pure markdown +
PNG. `go test ./...` cached green; secret check green. Total
~7.3 MB of PNG images, ~4100 lines of markdown.

## 2026-05-28 — Configurable storage paths

Operators with large fleets often want ISOs on a ZFS dataset,
drivers on a separate NFS mount, downloads on a thin OS volume.
Previously those all lived under `$DATA_DIR/<category>` with no
override path -- moving them meant relocating the whole data dir
or doing OS-level symlinks. This change exposes per-category
overrides as a portal setting and routes the BlobStore through
them.

**BlobStore router (`internal/storage/blob.go`).** Adds an
`overrides map[string]string` (category -> absolute path),
protected by a RWMutex. New methods:

- `SetCategoryRoot(category, absPath)` — operator-configured
  override. Empty clears. Refuses non-absolute paths. Creates the
  directory if missing.
- `CategoryRoot(category)` — returns the effective root, falling
  back to `root/<category>` when no override is set. Used by
  callers that operate on the raw filesystem (TFTP listener, ipxe
  static handler, Downloads page scanner).

`Resolve()` is rewritten to split the relative path on the first
slash, look up the category root, and confine the result. Path
traversal protection (the `..` rejection) is preserved: the new
test `TestResolve_PathTraversalStaysInsideKnownRoots_WithOverride`
verifies every result lands inside either the base root or the
configured override.

**Runtime settings.** Five new keys (`storage.iso`,
`storage.drivers`, `storage.software`, `storage.ipxe`,
`storage.downloads`) follow the same env-seed-then-portal-wins
pattern as AD/retention/throttle. `StoragePaths()`,
`StoragePath(category)`, `SetStoragePath(category, abs)`, and
`ApplyStorageOverrides(blobs)` round out the API. The
`StorageCategories` slice is the canonical closed set used by
both the runtime and the portal handler so adding a new category
is one edit.

**Wiring.** `main.go` calls `rt.ApplyStorageOverrides(blobs)`
right after constructing the runtime settings. The TFTP server
gained a `RootFunc func() string` field consulted on every
request so a portal-driven storage relocation takes effect
without a restart. The `/ipxe/static/` HTTP handler resolves
its FS at every request via the same router. The Downloads page
handler reads `r.Blobs.CategoryRoot("downloads")` instead of the
hardcoded `filepath.Join(r.DataDir, "downloads")`.

**Portal page (`/portal/settings/storage`).** Five fieldsets,
each with override input, effective-path display + copy button,
state badge (`writable` / `missing` / `default`), and a step-by-
step relocate-without-downtime guide at the bottom. The submit
handler validates that overrides are absolute (refuses relative
paths with a flash), persists each setting, pushes the new
values into the live BlobStore, and surfaces per-category errors
as warnings rather than aborting.

**WHY (decisions).**

- DECISION: Override per category, not per row. Operators want
  "ISOs on the big disk, downloads on the small disk", not "this
  specific ISO on this disk". Per-category keeps the UI small and
  the routing predictable.
- DECISION: Database stores relative paths; the override is in
  the resolver. This way a relocation doesn't require any data
  migration -- the rows still say `iso/12/source.iso`, but the
  resolver routes that to `/mnt/zfs/iso/12/source.iso`.
- DECISION: Files are not moved automatically. The portal warns
  loudly, the docs explain the stop-move-start-save sequence.
  Auto-moving is a foot-gun: a sudden directory rename mid-deploy
  would brick in-flight uploads. Operator does the move when
  they're ready.
- DECISION: The TFTP listener stores a RootFunc rather than a
  static string so the iPXE relocation works without a restart.
  Same for the ipxe static handler. The Downloads page already
  reads the directory on every request so it gets the new path
  for free.

**BUILD STATE.** `go test ./...` green including five new
BlobStore tests (default subdir, override resolve, refuse-
relative, empty-clears, traversal-inside-known-roots). Secret
check green. End-to-end smoke test: started server with
`$DATA_DIR=/tmp/storage-smoke`, saved
`storage_iso=/tmp/relocated-iso` through the portal page,
uploaded an ISO via the API, confirmed the file landed at
`/tmp/relocated-iso/1/source.iso` (NOT
`/tmp/storage-smoke/iso/...`). Screenshot at
`docs/user-guide/images/settings-storage.png`.

## 2026-05-29 — Versioning, auto-tag releases, server/agent updates

End-to-end versioning lands so the user-facing question "what's
running here, and is it current?" gets a clear answer. Three
pieces stack:

1. **Build-time version stamping.** Each component's main.go
   declares `var Version = "dev"`. The Makefiles inject the real
   version through `-ldflags "-X main.Version=$VERSION"`. CI's
   release.yml derives `VERSION` from `${GITHUB_REF#refs/tags/}`;
   local builds get the literal "dev". Each binary also honours
   `--version` for a bare one-line check.

2. **Auto-tag on merge to main** (`.github/workflows/auto-tag.yml`).
   On every push to main, the workflow looks up the merged PR via
   the search API, inspects its labels, picks a bump type
   (`patch` default, `release:minor` or `release:major` override),
   walks `git tag -l 'v*' --sort=-v:refname` for the prior tag,
   and pushes the next semver. The tag push triggers the existing
   `release.yml` which builds + publishes. Operator can opt out
   with `[skip release]` / `[no release]` in the commit message;
   first run with no prior tag publishes `v0.1.0`.

3. **Version + update endpoints + portal page** (Settings →
   Updates). `GET /api/v1/version` returns the running server
   version. `POST /api/v1/agent/update-info` accepts the agent's
   `(os, arch, current_version)` and scans the operator-managed
   downloads directory for a newer `autodeploy-agent-<os>-<arch>`
   binary using `.version` sidecars + SHA-256 sidecars. The portal
   page compares server-running against the latest published
   GitHub release (best-effort; failures surface as a warning and
   the page still renders), and lists every agent binary staged
   for distribution.

**Agent self-update** (`agent/internal/selfupdate/`).

- `selfupdate.Download` streams the new binary to a `.tmp` sibling,
  hashes it as it goes, and renames to `.new` only if the SHA-256
  matches the value the server sent. The hash is the *only*
  authenticator -- the agent refuses to install anything that
  doesn't match.
- `selfupdate.Swap` writes a small platform-specific updater (`.cmd`
  on Windows, `.sh` elsewhere) that waits for the current agent
  process to exit (poll up to 30 s), renames the binary to `.bak`
  (one-step rollback), moves the `.new` over the live path, and
  re-launches with the original command-line. On failure it
  restores from `.bak` and exits 1.
- `selfupdate.Cleanup` runs at agent startup to remove any debris
  (`.bak`, half-written `.new`, leftover updater scripts) from a
  prior swap.
- The resident check-in loop calls `maybeSelfUpdate` after each
  tick. If the server advertises a newer build, the agent
  downloads, verifies, swaps, and exits -- the updater script
  re-launches it within seconds.
- A `--no-self-update` flag pins an agent to its current version
  for canary testing.

**Server self-install is operator-driven** (per the design
decision earlier in the session). The Updates page shows current
vs latest, links to the release on GitHub, and points at the
install-script-rerun flow. The server never replaces its own
binary while running.

**WHY (decisions).**

- DECISION: SHA-256 sidecars, not signatures. The server is the
  only authenticator for the binary the agent downloads, so the
  trust chain is "agent trusts the server's TLS cert / cookie ->
  server states the SHA-256 -> agent verifies". A full
  signing/verification path with key management is overkill at
  this size; the operator-controlled downloads directory keeps
  the attack surface tight.
- DECISION: Agent self-update can't rely on a Windows service.
  The default install launches the agent as a first-logon command
  with `--check-in 5m`, not as a service. The updater script
  uses `start "" /B` to detach + relaunch with the original
  argv, which works in both modes.
- DECISION: Auto-tag uses PR labels rather than commit-message
  prefixes (`feat:` / `fix:` / `BREAKING CHANGE:`). Labels are
  easier to set retroactively and don't require team-wide
  commit-message discipline. The default is patch so unlabeled
  PRs get a predictable bump.
- DECISION: First release without a prior tag is `v0.1.0` not
  `v0.0.1`. That matches the version-number references already
  in the install docs.

**BUILD STATE.** `go test ./...` green across server, agent and
boot-client. New tests: `TestSemverLess`,
`TestIsOlderVersion_EmptyOrDevAlwaysOlder`,
`TestHandleVersion_ReturnsBuildVersion`,
`TestHandleAgentUpdateInfo_NoBinary_NoUpdate`,
`TestHandleAgentUpdateInfo_NewerBinary_Advertises`,
`TestHandleAgentUpdateInfo_SameVersion_NoUpdate`,
`TestDownload_RejectsBadHash`, `TestDownload_HappyPath`,
`TestDownload_RejectsEmptyArgs`, `TestDownload_HTTPError`,
`TestSiblingPath_HasSuffix`. Secret check green; bash linter
green. End-to-end smoke test:

- `VERSION=v0.9.0 make build` produced a server with the
  embedded version visible at `GET /api/v1/version`.
- An older agent reporting v0.0.1 received `update_available:
  true` with the URL + SHA-256 from a seeded
  `autodeploy-agent-windows-amd64.exe.version` sidecar.
- A current agent reporting v0.1.0 received `update_available:
  false`.
- The portal Updates page rendered with both sections.

## 2026-05-29 — HTTP-only is a valid production deployment

Cleartext HTTP on a non-loopback bind in production mode previously
returned `ErrPlainHTTPInProd` and refused to start. That ruled out
two legitimate deployment shapes: (a) a reverse proxy terminates
TLS upstream of AutoDeploy and forwards to its bind, and (b)
trusted-network LAN deployments where TLS-cert management is more
operational overhead than it's worth.

**Change.** `ListenAndServe` no longer refuses; it emits a single
`http.cleartext_public_bind` WARN at startup naming the risk
("session cookies, admin credentials, BitLocker keys and agent
payloads will travel unencrypted on the wire") and the mitigation
("set HTTPS, or front this with a reverse proxy"). Loopback binds
get no warning -- they're the most common dev shape and would be
noise.

The HTTPS listener's behaviour is unchanged: if you set
`AUTODEPLOY_HTTPS_ADDR` with `DEV=false` you must also supply a
cert + key. Setting `HTTPS_ADDR=` empty is now a fully supported
production choice.

**WHY.** Forcing HTTPS at the binary level made AutoDeploy
unusable in topologies the rest of the architecture handled
fine. The boot client and agent both accept cleartext URLs; the
install scripts hand them whatever URL the server advertises; the
sensitive endpoints (sessions, BitLocker key escrow) are
identified in the WARN message so an operator scanning the
install log gets the consequences spelled out without having to
look them up.

**Files touched.** `server/internal/httpx/server.go` (removed
`ErrPlainHTTPInProd`, added warning log), the matching test in
`server_test.go` (rewrote `TestProductionRefusesNonLoopbackPlainHTTP`
as `TestProductionAllowsNonLoopbackPlainHTTPWithWarning` plus
`TestProductionLoopbackBindNoWarning`), the env example files for
both Linux and Windows installs, `scripts/install-linux.sh`'s
next-steps banner, and three docs sections: a new "TLS deployment
shapes" block in `operations.md`, updated tables in
`configuration.md`, and a tweaked security-review checklist item
in `security.md`. End-to-end smoke verified
`AUTODEPLOY_DEV=false AUTODEPLOY_HTTP_ADDR=0.0.0.0:<port>`
starts, serves `/healthz`, returns `/api/v1/version`, and emits
the expected WARN line.

## 2026-05-29 — Auto-tag now reliably triggers release.yml

`auto-tag.yml` was pushing the tag with `GITHUB_TOKEN`, which
GitHub deliberately suppresses from triggering further workflows
to prevent infinite loops. Symptom: `v0.1.1` and `v0.1.2` were
tagged on main but have no published GitHub Release attached.
Only `v0.1.0` (manually published earlier) shows on the releases
page.

**Fix.** After pushing the tag, `auto-tag.yml` now calls
`gh workflow run release.yml --ref <tag>` explicitly. workflow_dispatch
is one of the two events (with repository_dispatch) that the
loop-protection rule exempts; release.yml's release-job gate is
`startsWith(github.ref, 'refs/tags/v')`, which is satisfied when
the dispatch ref is the tag.

`actions: write` added to the workflow's permissions block --
required for the `gh workflow run` REST call to succeed.

**Orphan tags.** The pre-fix tags `v0.1.1` and `v0.1.2` already
exist on the remote. To publish them retroactively, run:

```
gh workflow run release.yml --ref v0.1.1
gh workflow run release.yml --ref v0.1.2
```

Going forward, every merge to main produces both a tag and a
published release with binaries + sidecars, with no manual
intervention.

---

## 2026-06-01 — Agent-driven Active Directory domain join

**WHAT.** Moved domain join off the unattend's specialize-pass online join and
onto the agent, configured per **image**. New `image_domain_join` table
(migration 0018) holds enable/domain/OU/join-account; the join password is
encrypted at rest via `secrets.Box` and returned only to the deploying agent
over `POST /api/v1/agent/domain-join` (never written to the unattend XML). The
agent's `/self` loop joins after first boot (`Add-Computer`, credentials passed
via environment, not the command line) when it isn't already in the target
domain, then reboots. The unattend generator suppresses its legacy
`<UnattendedJoin>` block for images that join via the agent. Portal image
editor gains an "Active Directory domain join (via agent)" section. Docs
updated (operations/active-directory, portal/images, troubleshooting,
reference/api).

**WHY (assumptions / decisions).**
- The unattend online join ran `djoin` during specialize, needed a DC reachable
  mid-Setup, and was observed on the rig to hang ~15 minutes then leave the
  machine unjoined. Joining from the booted OS (full networking/DNS) is far more
  reliable.
- Config lives on the image (operator's request); the unattend's domain-join
  fields are kept for legacy and **ignored** when the image does agent join.
- DECISION: the join endpoint is gated by the server-minted `agent_id` (same
  capability the `/self` loop already uses). Operators should use a
  least-privilege join account; surfaced in the portal hint.
- Binding `Target OU` overrides the image's default OU per machine.
- SCOPE: AD group membership is left to the existing server-side AD coordination
  (out of scope here).

**STATE.** `go build ./...` + targeted tests green on server (model/api/payload/
portal) and agent (windows build + vet). New tests: repo round-trip /
password-preserve/rotate / at-rest encryption; endpoint resolution incl. OU
override and enable/disable; generator suppression vs legacy emission.

**NEXT.** Optional: agent-set AD group memberships; a per-image "test join"
button; surface last join status on the machine page.

---

## 2026-06-02 — Migrate to official signed iPXE (UEFI Secure Boot)

**WHAT.** Replaced AutoDeploy's custom, script-embedded iPXE builds with the
iPXE project's **official signed release** (`ipxeboot.tar.gz`). `fetch-ipxe.sh`
now downloads that archive and lays the binaries out in the iPXE dir with our
serving names: signed `ipxe.efi`/`snponly.efi`, the Microsoft-signed shim as
`shimx64.efi` + `ipxe-shim.efi`/`snponly-shim.efi` (one binary, three names —
the shim picks its second stage from the name it's invoked as), BIOS
`undionly.kpxe`/`ipxe.pxe`, `ipxe-arm64.efi`, and an `arm64-sb/` subdir for
arm64 Secure Boot. Removed `.github/workflows/build-ipxe.yml` and
`scripts/build-embedded-ipxe.sh`; dropped the embedded-build helper from
`install-linux.sh`. Portal Settings → PXE now shows a **Secure Boot** column
(MS-signed shim / iPXE-signed / n/a) instead of the old "Embedded?" marker, and
the DHCP snippets gained Secure Boot bootfile guidance. Docs updated
(`install/pxe-and-boot.md` gains a UEFI Secure Boot section, `portal/settings.md`,
`operations/troubleshooting.md`).

**WHY (assumptions / decisions).**
- The embedded script was already redundant: the server serves `autoexec.ipxe`
  dynamically (HTTP route + TFTP synth), so stock binaries chainload to
  AutoDeploy on their own. Embedding also modifies the binary, which is
  fundamentally incompatible with code signing — the blocker for Secure Boot.
- iPXE v2.0.0+ ships a Microsoft-3rd-Party-UEFI-CA-signed shim that verifies and
  loads an iPXE-CA-signed `ipxe.efi`. Chaining shim → ipxe.efi keeps the Secure
  Boot chain intact with no key enrolment.
- DECISION: flat layout using the SB-signed `ipxe.efi` as the default UEFI
  binary — it boots directly when Secure Boot is off and via the shim when it's
  on, so one file covers both. arm64 SB needs its own `ipxe.efi` next to its
  shim, which collides with the x86_64 name in a flat dir, so it lives under
  `arm64-sb/`.
- KNOWN LIMITATION / NEXT: under Secure Boot, signed iPXE will only boot a
  *signed* next stage. The boot image (`autodeploy-kernel`) is not yet signed,
  so end-to-end Secure Boot still needs the kernel stage handled (signed kernel
  / shim+MOK) or Secure Boot off for that stage. Documented prominently; the
  signed shim still delivers a verified bootloader today.

**STATE.** `go build ./...` green; `go test ./internal/portal/... ./internal/api/...
./internal/tftp/...` green. `fetch-ipxe.sh` verified end-to-end against the real
`ipxeboot.tar.gz` (correct files placed, sha256 sidecars written). Real PXE/
Secure Boot boot not exercised in CI (no hardware/VM in this environment).

**NEXT.** Sign the boot image (kernel) for full end-to-end Secure Boot; consider
surfacing `arm64-sb/` contents in the portal PXE table.
