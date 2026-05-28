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

Sections will be added as the corresponding features are implemented. If a
section you expect is missing, the feature it documents has not yet shipped.

## Current product surface (Phase 1)

- **Server** — runs the management portal and JSON API. SQLite-backed.
- **Portal** at `/portal/` — read-only views of every artifact and image,
  with each image's resolved configuration viewable per row.
- **JSON API** at `/api/v1/` — full CRUD on ISOs, Unattends, Driver packages,
  Software packages and Images. Image inheritance with cycle prevention and
  reference-count guards on shared objects.
- **Boot Client** — reads SMBIOS identity and exits safely (real imaging
  arrives in Phase 3).
- **Agent** — starts and exits (lifecycle arrives in Phase 6).

Still to come (sequenced by `docs/design/roadmap.txt`): payload upload and
delivery (Phase 2), the actual PXE boot/image-apply flow (Phase 3+), software
loadouts (Phase 7), inventory and re-imaging (Phase 8–9), AD integration
(Phase 10), the access PIN and authentication (Phase 11), BitLocker
(Phase 12), bulk operations (Phase 13), centralised logging (Phase 14),
branding (Phase 15).
