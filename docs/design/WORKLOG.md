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
