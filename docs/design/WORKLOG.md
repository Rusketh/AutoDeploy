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
