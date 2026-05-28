# Configuring the server

> **Status.** Phase 0. The server's settings are limited to the environment
> variables below; system-level settings configured from inside the portal
> (branding, access PIN, log retention, AD integration) arrive in their
> respective later phases.

## Environment variables

| Variable                | Default              | Meaning                                                                 |
|-------------------------|----------------------|-------------------------------------------------------------------------|
| `AUTODEPLOY_HTTP_ADDR`  | `127.0.0.1:8080`     | Cleartext HTTP bind. Empty disables. In production mode only loopback is permitted. |
| `AUTODEPLOY_HTTPS_ADDR` | `` (unset)           | HTTPS bind. Empty disables.                                              |
| `AUTODEPLOY_TLS_CERT`   | `` (unset)           | PEM cert path for HTTPS. In dev mode if both this and the key are empty, a self-signed cert is generated under `AUTODEPLOY_DATA_DIR/tls/`. In production both must be set. |
| `AUTODEPLOY_TLS_KEY`    | `` (unset)           | PEM key path for HTTPS. See above.                                       |
| `AUTODEPLOY_DATA_DIR`   | `./data`             | Root for stored payloads and the SQLite database.                        |
| `AUTODEPLOY_DEV`        | `true`               | When `false`, the server refuses cleartext HTTP on non-loopback addresses and disables dev-cert generation. |

## On-disk layout

The server keeps payload blobs and supporting files under
`AUTODEPLOY_DATA_DIR` (default `./data`). The directories appear as the
relevant features land; in Phase 0 the directory is created on demand but is
empty.

```
$AUTODEPLOY_DATA_DIR/
  autodeploy.sqlite    # the relational store
  tls/                 # self-signed dev TLS material (Phase 2; dev mode only)
  iso/{id}/
    source.iso         # the uploaded ISO file
    files/...          # extracted ISO contents (Phase 2)
  drivers/{id}/payload.bin    # uploaded driver-package blob (Phase 2)
  software/{id}/payload.bin   # uploaded installer payload (Phase 2)
  unattend/                   # generated answer files (Phase 5)
```

## HTTPS and production safety

In `AUTODEPLOY_DEV=false` mode the server will refuse to bind cleartext HTTP
to a non-loopback address. Production deployments must either:

- Bind to `127.0.0.1` and front the server with an HTTPS-terminating reverse
  proxy (nginx, Caddy, etc.), or
- (Later phase) Terminate TLS in the server itself with a configured
  certificate and key.

Until the in-process HTTPS option is added, the loopback + reverse-proxy
pattern is the supported production deployment.
