# AutoDeploy

A remote operating-system deployment and configuration platform for Windows
machines. A single replacement for WDS, MDT, SCCM and FOG, delivered entirely
over HTTP(S) and driven from a web portal.

See `docs/design/` for the authoritative design documents, `docs/user-guide/`
for the operator guide, and `docs/design/WORKLOG.md` for the running
development log.

## Repository layout

| Path           | Component                                                   |
|----------------|-------------------------------------------------------------|
| `server/`      | Management portal, HTTP API, Deployment Service, Domain Integration Service (Go) |
| `boot-client/` | Linux pre-OS imaging client, chainloaded over HTTP via iPXE (Go) |
| `agent/`       | In-OS resident Deployment Client for Windows (Go)           |
| `scripts/`     | Build, ipxe and lab helper scripts                          |
| `docs/`        | Design documents, worklog and user guide                    |

## Building

Each component is a separate Go module and is built independently:

```
make -C server build
make -C boot-client build
make -C agent build
```

CI (GitHub Actions) builds and tests all three on every push.

## Status

Under active development. Track progress in `docs/design/WORKLOG.md` and the
operator-facing documentation in `docs/user-guide/`.
