# AutoDeploy Operator Guide

This guide documents how to install, configure and operate AutoDeploy. It is
written for the people who will run the system day to day — not for its
internals. For the design, see `docs/design/`.

The guide grows alongside the software: features appear here as they appear in
the product, and never before.

## Contents

1. [Concepts](concepts.md) — what AutoDeploy is, and the objects you'll work with.
2. [Installation](installation.md) — getting the server, Boot Client and agent built and running.
3. [Configuring the server](configuration.md) — environment variables and on-disk layout.
4. [API quick-start](api-quickstart.md) — `curl` recipes against the JSON API.
5. [Payload uploads and delivery](payloads.md) — uploading ISOs and packages, HTTPS, the manifest endpoint.
6. [Boot Client and PXE](boot-client.md) — building the initramfs, the iPXE chainload, the deploy flow.
7. [Driver matching](driver-matching.md) — SMBIOS filter shapes, preview endpoint, manifest integration.
8. [Unattend configuration](unattend.md) — settings model, generated XML, secrets handling.
9. [Software packages](software.md) — detection rules, ordered install steps, the agent flow.
10. [Software loadouts](loadouts.md) — inheritable collections of packages, opt-outs, precedence.
11. [Inventory and bindings](inventory.md) — machine records, bindings, deployment history, drift.

Sections will be added as the corresponding features are implemented. If a
section you expect is missing, the feature it documents has not yet shipped.

## Current product surface (Phase 8)

- **Server** — runs the management portal and JSON API, with HTTPS support.
  SQLite-backed.
- **Portal** at `/portal/` — read-only views of every artifact and image,
  with each image's resolved configuration viewable per row.
- **JSON API** at `/api/v1/` — full CRUD on ISOs, Unattends, Driver
  packages, Software packages and Images, plus payload upload endpoints,
  ISO extraction, and a manifest endpoint that returns the Boot Client's
  payload URLs derived from the resolved configuration.
- **Payload delivery** at `/payload/iso/{id}/{path}`,
  `/payload/drivers/{id}`, `/payload/software/{id}` — streamed with HTTP
  Range support so a Boot Client can resume an interrupted fetch.
- **HTTPS** — production cert/key via env vars, or auto-generated
  self-signed under `AUTODEPLOY_DATA_DIR/tls/` in dev mode.
- **Boot Client** — reads SMBIOS identity, calls the server for the
  deployment menu, downloads the manifest's payloads and applies a WIM
  to the target disk via `wimlib-imagex`. Includes a `--dry-run` mode
  that logs every destructive step without executing it. Fails safe on
  any error (boots the existing OS).
- **iPXE** at `/ipxe/boot.ipxe` — chainload script and static asset tree
  for kernel/initrd, with a reference initramfs build script under
  `scripts/initramfs/`.
- **Agent** — at deploy time, fetches the effective software set from
  the server, evaluates each package's detection rules, downloads the
  installer for packages not already installed, runs the typed install
  steps with success-code and continue-on-failure handling, and reports
  the outcome. Cross-compiles to Windows. (Resident check-in mode and
  bulk operations arrive in Phase 13.)

Still to come (sequenced by `docs/design/roadmap.txt`): re-imaging
(Phase 9), AD integration (Phase 10), the access PIN and authentication
(Phase 11), BitLocker (Phase 12), bulk operations (Phase 13),
centralised logging (Phase 14), branding (Phase 15).
