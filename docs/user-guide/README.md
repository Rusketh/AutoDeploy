# AutoDeploy Operator Guide

This guide documents how to install, configure and operate AutoDeploy. It is
written for the people who will run the system day to day — not for its
internals. For the design, see `docs/design/`.

The guide grows alongside the software: features appear here as they appear in
the product, and never before.

## Contents

1. [Concepts](concepts.md) — what AutoDeploy is, and the objects you'll work with.
2. [Installation](installation.md) — getting the server, Boot Client and agent built and running. *(in progress)*
3. [Configuring the server](configuration.md) — environment variables and on-disk layout. *(in progress)*

Sections will be added as the corresponding features are implemented. If a
section you expect is missing, the feature it documents has not yet shipped.

## Current product surface (Phase 0)

The current build provides only the foundational skeleton:

- A server binary that listens on HTTP and exposes `/healthz`.
- A Boot Client binary that reads SMBIOS identity and logs it.
- An agent binary that starts and exits.

There is no portal UI, no API beyond the health endpoint, no boot menu and no
software deployment yet. These arrive in later phases (see
`docs/design/roadmap.txt`).
