# Configuring the server

> **Status.** Phase 0. The server's settings are limited to the environment
> variables below; system-level settings configured from inside the portal
> (branding, access PIN, log retention, AD integration) arrive in their
> respective later phases.

## Environment variables

See [Installation → Environment variables](installation.md#environment-variables).

## On-disk layout

The server keeps payload blobs and supporting files under
`AUTODEPLOY_DATA_DIR` (default `./data`). The directories appear as the
relevant features land; in Phase 0 the directory is created on demand but is
empty.

```
$AUTODEPLOY_DATA_DIR/
  iso/         # extracted ISO contents (Phase 1)
  drivers/     # ingested driver packages (Phase 4)
  software/    # uploaded installer payloads (Phase 6)
  unattend/    # generated answer files cached for delivery (Phase 5)
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
