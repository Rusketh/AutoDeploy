# Installation

> **Status.** This is the Phase 0 development build. Only the foundational
> skeleton exists; the portal, API and imaging flow are not yet present. This
> page describes how to build and run what is currently shipped so a developer
> can verify the toolchain works end to end.

## Prerequisites

- Go 1.22 or newer.
- GNU `make`.
- For real imaging (later phases): an iPXE-capable target machine or VM, a
  PostgreSQL server, and a Linux server to host the AutoDeploy server.

## Building

Each component is a separate Go module under `server/`, `boot-client/` and
`agent/`. Each has a `Makefile` with `build`, `test`, `vet` and `clean`
targets.

```sh
# Server (host OS, by default).
make -C server build

# Boot Client (always linux/amd64 static for the initramfs).
make -C boot-client build

# Agent (host build, plus a windows/amd64 EXE).
make -C agent build         # autodeploy-agent on the host
make -C agent build-windows # autodeploy-agent.exe for deployed Windows machines
```

The CI workflow in `.github/workflows/ci.yml` runs the same `vet`, `test` and
`build` targets on every push and uploads the resulting binaries as
build artifacts.

## Running the server

```sh
AUTODEPLOY_HTTP_ADDR=127.0.0.1:8080 \
AUTODEPLOY_DATA_DIR=./data \
AUTODEPLOY_DEV=true \
  ./server/bin/autodeploy-server
```

Then:

```sh
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

### Environment variables

| Variable                | Default              | Meaning                                                                 |
|-------------------------|----------------------|-------------------------------------------------------------------------|
| `AUTODEPLOY_HTTP_ADDR`  | `127.0.0.1:8080`     | Bind address.                                                            |
| `AUTODEPLOY_DATA_DIR`   | `./data`             | Root for stored payloads (extracted ISO contents, drivers, installers). |
| `AUTODEPLOY_DEV`        | `true`               | When `false`, the server refuses to bind cleartext HTTP to a non-loopback address. Production deployments must terminate TLS upstream or set up HTTPS in front of the server. |

## Running the Boot Client (Phase 0 sanity check)

```sh
./boot-client/bin/autodeploy-boot --server http://127.0.0.1:8080
```

The Phase 0 binary reads SMBIOS identity from `/sys/class/dmi/id` and logs it,
then exits without doing anything destructive. Real PXE chainloading, menu
rendering and imaging arrive in Phase 3.

## Running the agent (Phase 0 sanity check)

```sh
./agent/bin/autodeploy-agent --server http://127.0.0.1:8080
```

Logs its start, identifies the host OS and architecture, and exits. The full
resident lifecycle (software install at deploy time, queued bulk jobs,
BitLocker, inventory reporting) lands from Phase 6 onward.
