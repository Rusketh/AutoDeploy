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
