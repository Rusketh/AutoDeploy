# Configuring the server

The server is configured by environment variables at boot. Runtime
operational settings (AD config, retention, throttle, branding, the
access PIN) live in the portal — see [Operating AutoDeploy](operations.md)
for the split between bootstrap (env) and runtime (portal) settings.

## TL;DR — does HTTPS matter for me?

| Scenario | What to set | What the server does |
|---|---|---|
| **Local dev / lab on your laptop** | Nothing | Plain HTTP on `127.0.0.1:8080`. No TLS material needed. |
| **Lab over the LAN, no PII** | `AUTODEPLOY_HTTP_ADDR=0.0.0.0:8080` | Still plain HTTP, but bound to all interfaces. **Requires `AUTODEPLOY_DEV=true`** (the default) — otherwise the server refuses to start. |
| **Lab with self-signed HTTPS** | `AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443` + `AUTODEPLOY_DEV=true` | HTTPS with a self-signed cert auto-generated under `$DATA_DIR/tls/`. Browsers warn; clients can `-k`. |
| **Production** | `AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443`, `AUTODEPLOY_TLS_CERT`, `AUTODEPLOY_TLS_KEY`, `AUTODEPLOY_DEV=false` | HTTPS with your real cert. Cleartext HTTP must be loopback-only or empty. |

**HTTPS is fully optional.** The only "must use HTTPS" rule is: in
`AUTODEPLOY_DEV=false` (production) mode the server refuses to bind
cleartext HTTP to a non-loopback address. Local installs run on plain
HTTP without any TLS material at all — that's the default.

## Environment variables

### Bootstrap (must be known before the server starts)

| Variable                          | Default              | Meaning |
|-----------------------------------|----------------------|---------|
| `AUTODEPLOY_HTTP_ADDR`            | `127.0.0.1:8080`     | Cleartext HTTP bind. Empty disables. In production mode only loopback is permitted. |
| `AUTODEPLOY_HTTPS_ADDR`           | `` (empty)           | HTTPS bind. Empty disables. |
| `AUTODEPLOY_TLS_CERT`             | `` (empty)           | PEM cert path for HTTPS. In dev mode if both this and the key are empty, a self-signed cert is auto-generated under `$DATA_DIR/tls/`. In production both must be set. |
| `AUTODEPLOY_TLS_KEY`              | `` (empty)           | PEM key path for HTTPS. |
| `AUTODEPLOY_TFTP_ADDR`            | `` (empty)           | UDP TFTP listener for the iPXE bootstrap (e.g. `:69`). Empty disables. Serves `$DATA_DIR/ipxe/` read-only. |
| `AUTODEPLOY_DATA_DIR`             | `./data`             | Root for the SQLite database, payload blobs, secrets key, etc. |
| `AUTODEPLOY_DEV`                  | `true`               | When `false`, the server refuses cleartext HTTP on non-loopback addresses and disables dev-cert generation. |
| `AUTODEPLOY_SECRETS_KEY`          | `` (empty)           | Hex-encoded 32-byte at-rest encryption key for BitLocker PINs / recovery keys / AD bind password. Empty auto-generates a key file under `$DATA_DIR/secrets-key.bin` (mode 0600). |

### Runtime settings seeded by env (one-time)

These env vars **seed** values you can also edit through the portal.
After you save anything via the portal, the portal becomes the source
of truth and changes to the env file are ignored. See
[Operating AutoDeploy](operations.md) for the full split.

```
AUTODEPLOY_AD_URL, AUTODEPLOY_AD_BIND_DN, AUTODEPLOY_AD_BIND_PASSWORD,
AUTODEPLOY_AD_SEARCH_BASE, AUTODEPLOY_AD_SKIP_TLS_VERIFY,
AUTODEPLOY_LOG_RETENTION_DAYS, AUTODEPLOY_PAYLOAD_MAX_IN_FLIGHT
```

## On-disk layout

```
$AUTODEPLOY_DATA_DIR/
├── autodeploy.sqlite         # the relational store
├── secrets-key.bin           # AES-256-GCM key for at-rest encryption (mode 0600)
├── admin-bootstrap.txt       # one-time bootstrap admin password (delete after first login)
├── tls/                      # self-signed dev TLS material (dev mode only)
│   ├── dev-cert.pem
│   └── dev-key.pem
├── iso/{id}/
│   ├── source.iso            # the uploaded ISO
│   └── files/...             # extracted ISO contents (post-extract)
├── drivers/{id}/
│   ├── payload.bin           # the uploaded driver zip
│   └── files/                # extracted tree (post-extract)
│       └── metadata.json     # scanned .inf metadata
├── software/{id}/payload.bin # uploaded installer payload
├── ipxe/                     # iPXE bootstrap binaries + Boot Client kernel/initrd
│   ├── undionly.kpxe
│   ├── ipxe.efi
│   ├── snponly.efi
│   ├── autodeploy-kernel
│   └── autodeploy-initrd
└── downloads/                # binaries the portal Downloads page serves
    ├── autodeploy-agent-windows-amd64.exe
    └── autodeploy-boot-linux-amd64
```

## HTTP vs HTTPS — the full rules

The server has two listeners (`ListenAndServe` and
`ListenAndServeTLS`) that run independently. You configure whichever
you need. Source: `server/internal/httpx/server.go`.

**Plain HTTP listener** (`AUTODEPLOY_HTTP_ADDR`):

- Empty → no HTTP listener started.
- Set + `AUTODEPLOY_DEV=true` → listens on the configured address, any
  interface allowed.
- Set + `AUTODEPLOY_DEV=false` + bound to loopback (`127.0.0.1` / `::1`
  / `localhost`) → listens; intended for use behind a TLS-terminating
  reverse proxy.
- Set + `AUTODEPLOY_DEV=false` + bound to a non-loopback address →
  **server refuses to start** with `cleartext HTTP refused in
  production mode`.

**HTTPS listener** (`AUTODEPLOY_HTTPS_ADDR`):

- Empty → no HTTPS listener started.
- Set + cert/key empty + `AUTODEPLOY_DEV=true` → auto-generates a
  self-signed cert under `$DATA_DIR/tls/` and listens. Browsers and
  clients warn; that's expected.
- Set + cert/key empty + `AUTODEPLOY_DEV=false` → server refuses to
  start with `AUTODEPLOY_TLS_CERT and AUTODEPLOY_TLS_KEY must be set
  in production mode`.
- Set + cert/key files supplied → listens with your cert.

Both listeners can run side by side. The common production pattern is
HTTPS on `0.0.0.0:443` with HTTP loopback-only on `127.0.0.1:8080`
for local health checks.

## Recommended production setup

```sh
AUTODEPLOY_HTTPS_ADDR=0.0.0.0:443
AUTODEPLOY_HTTP_ADDR=127.0.0.1:8080
AUTODEPLOY_TLS_CERT=/etc/autodeploy/tls/server.crt
AUTODEPLOY_TLS_KEY=/etc/autodeploy/tls/server.key
AUTODEPLOY_DEV=false
AUTODEPLOY_DATA_DIR=/var/lib/autodeploy
AUTODEPLOY_SECRETS_KEY=<openssl rand -hex 32>
AUTODEPLOY_TFTP_ADDR=:69
```

Operators always reach the portal at `https://your-host/portal/`.
The loopback HTTP listener gives you a way to monitor `/healthz`
locally without a TLS round trip.
